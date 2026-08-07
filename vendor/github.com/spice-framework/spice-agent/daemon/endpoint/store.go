package endpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent/daemon/internal/userstorage"
)

const (
	metadataFileName = "endpoint.json"
	metadataLockName = "endpoint.lock"
	daemonLockName   = "daemon.lock"
	startupLockName  = "startup.lock"
)

var (
	// ErrNotFound reports that no active daemon endpoint is published. It is
	// returned as an exact error so managed startup never mistakes malformed or
	// untrusted state for absence.
	ErrNotFound = errors.New("local daemon endpoint is not found")
	// ErrActive reports an already active endpoint or process instance that may
	// not be replaced.
	ErrActive = errors.New("local daemon endpoint is active")
	// ErrClosed reports use after a Store began closing.
	ErrClosed = errors.New("local daemon endpoint store is closed")
)

// StoreConfig configures retained current-user endpoint storage.
type StoreConfig struct {
	Directory    string
	PollInterval time.Duration
}

// Store securely publishes and discovers one current-user daemon endpoint.
// Close prevents new operations immediately and releases the retained
// directory after every outstanding startup lease and publication closes.
type Store struct {
	mu           sync.Mutex
	directory    *userstorage.Directory
	pollInterval time.Duration
	references   uint64
	closing      bool
	finalizing   bool
	closed       bool
	closeErr     error
	closeDone    chan struct{}
}

// StartupLease owns the cross-process lock that serializes attach-or-start
// decisions. Release is idempotent, concurrency-safe, and does not wait.
type StartupLease struct {
	once    sync.Once
	lock    *userstorage.Lock
	release func() error
	err     error
}

// Publication owns the stable liveness lock for one published daemon.
// Close removes metadata only when it is still byte-identical to the record
// written by this publication.
type Publication struct {
	mu           sync.Mutex
	directory    *userstorage.Directory
	liveness     *userstorage.Lock
	encoded      []byte
	pollInterval time.Duration
	release      func() error
	closing      bool
	closed       bool
	done         chan struct{}
	err          error
}

// OpenStore binds one exact secure directory without reading or publishing an
// endpoint. PollInterval controls context-aware lock contention polling.
func OpenStore(config StoreConfig) (*Store, error) {
	if config.PollInterval <= 0 {
		return nil, errors.New("endpoint store poll interval must be positive")
	}
	directory, err := userstorage.Bind(config.Directory)
	if err != nil {
		return nil, fmt.Errorf("open endpoint store: %w", err)
	}
	return &Store{
		directory: directory, pollInterval: config.PollInterval, closeDone: make(chan struct{}),
	}, nil
}

// AcquireStartup acquires the cross-process startup lock using non-blocking
// lock attempts. Caller cancellation and deadlines bound all contention.
func (store *Store) AcquireStartup(ctx context.Context) (*StartupLease, error) {
	directory, release, err := store.retain(ctx)
	if err != nil {
		return nil, err
	}
	lock, err := store.acquireLock(ctx, directory, startupLockName)
	if err != nil {
		return nil, errors.Join(err, release())
	}
	return &StartupLease{lock: lock, release: release}, nil
}

// Discover returns the active endpoint. Exact ErrNotFound means metadata was
// absent or proved stale and was safely removed. Malformed, untrusted, and
// wrong-platform records always return distinct hard failures.
func (store *Store) Discover(ctx context.Context) (result Metadata, resultErr error) {
	directory, release, err := store.retain(ctx)
	if err != nil {
		return Metadata{}, err
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			resultErr = errors.Join(resultErr, releaseErr)
		}
	}()

	metadataLock, err := store.acquireLock(ctx, directory, metadataLockName)
	if err != nil {
		return Metadata{}, err
	}
	encoded, metadata, readErr := readRecord(ctx, directory)
	if readErr != nil {
		closeErr := metadataLock.Close()
		if errors.Is(readErr, ErrNotFound) && closeErr == nil {
			return Metadata{}, ErrNotFound
		}
		return Metadata{}, errors.Join(readErr, closeErr)
	}
	if err = context.Cause(ctx); err != nil {
		return Metadata{}, errors.Join(err, metadataLock.Close())
	}
	liveness, lockErr := directory.AcquireLock(daemonLockName)
	if errors.Is(lockErr, userstorage.ErrLockBusy) {
		if closeErr := metadataLock.Close(); closeErr != nil {
			return Metadata{}, closeErr
		}
		return metadata, nil
	}
	if lockErr != nil {
		return Metadata{}, errors.Join(
			fmt.Errorf("probe daemon liveness: %w", lockErr), metadataLock.Close(),
		)
	}
	cleanupErr := removeExactRecord(directory, encoded)
	cleanupErr = errors.Join(cleanupErr, liveness.Close(), metadataLock.Close())
	if cleanupErr != nil {
		return Metadata{}, cleanupErr
	}
	return Metadata{}, ErrNotFound
}

// Publish atomically publishes metadata while holding its unique process
// liveness lock. The metadata lock is always acquired first, so a new daemon
// can never make an older metadata record appear active. An active endpoint is
// never overwritten; a stale canonical record may be replaced.
func (store *Store) Publish(ctx context.Context, metadata Metadata) (*Publication, error) {
	if ctx == nil {
		return nil, errors.New("endpoint publication context is required")
	}
	if err := validateCurrentPlatform(metadata); err != nil {
		return nil, err
	}
	encoded, err := encodeMetadata(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode endpoint publication: %w", err)
	}
	directory, release, err := store.retain(ctx)
	if err != nil {
		return nil, err
	}
	metadataLock, err := store.acquireLock(ctx, directory, metadataLockName)
	if err != nil {
		return nil, errors.Join(err, release())
	}
	failBeforeLiveness := func(primary error) error {
		return errors.Join(primary, metadataLock.Close(), release())
	}
	if _, _, err = readRecord(ctx, directory); err != nil && !errors.Is(err, ErrNotFound) {
		return nil, failBeforeLiveness(err)
	}
	if err = context.Cause(ctx); err != nil {
		return nil, failBeforeLiveness(err)
	}
	liveness, err := directory.AcquireLock(daemonLockName)
	if errors.Is(err, userstorage.ErrLockBusy) {
		return nil, failBeforeLiveness(ErrActive)
	}
	if err != nil {
		return nil, failBeforeLiveness(fmt.Errorf("acquire daemon liveness: %w", err))
	}
	fail := func(primary error) error {
		return errors.Join(primary, liveness.Close(), metadataLock.Close(), release())
	}
	if err = directory.WriteFileAtomic(metadataFileName, encoded); err != nil {
		return nil, fail(fmt.Errorf("publish local endpoint metadata: %w", err))
	}
	if err = metadataLock.Close(); err != nil {
		// Atomic publication may already be visible. Releasing the liveness lock
		// makes that uncertain record stale; discovery will validate and remove it.
		return nil, fail(fmt.Errorf("release endpoint metadata publication lock: %w", err))
	}
	return &Publication{
		directory: directory, liveness: liveness, encoded: bytes.Clone(encoded),
		pollInterval: store.pollInterval, release: release,
	}, nil
}

// Close prevents new operations. Outstanding leases and publications retain
// the bound directory until they release, so Close itself never waits on
// another process or destroys an active lease.
func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	if store.closed {
		err := store.closeErr
		store.mu.Unlock()
		return err
	}
	if store.finalizing {
		done := store.closeDone
		store.mu.Unlock()
		<-done
		store.mu.Lock()
		err := store.closeErr
		store.mu.Unlock()
		return err
	}
	store.closing = true
	directory := store.takeDirectoryIfDrained()
	store.mu.Unlock()
	if directory != nil {
		store.finishClose(directory)
	}
	store.mu.Lock()
	err := store.closeErr
	store.mu.Unlock()
	return err
}

// Release gives up one startup lease. It is safe to call repeatedly and from
// concurrent goroutines.
func (lease *StartupLease) Release() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		if lease.lock != nil {
			lease.err = lease.lock.Close()
		}
		if lease.release != nil {
			lease.err = errors.Join(lease.err, lease.release())
		}
	})
	return lease.err
}

// Close withdraws this exact publication, releases daemon liveness while still
// holding the metadata lock, and then releases the metadata lock. It may wait
// for another short metadata operation; CloseContext provides a caller bound.
func (publication *Publication) Close() error {
	return publication.CloseContext(context.Background())
}

// CloseContext closes one publication using strict metadata-lock then
// liveness-lock ordering. If ctx expires before the metadata lock is acquired,
// the publication remains active and a later call may retry safely.
func (publication *Publication) CloseContext(ctx context.Context) error {
	if publication == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("endpoint publication close context is required")
	}
	for {
		publication.mu.Lock()
		if publication.closed {
			err := publication.err
			publication.mu.Unlock()
			return err
		}
		if publication.directory == nil {
			publication.closed = true
			publication.mu.Unlock()
			return nil
		}
		if publication.closing {
			done := publication.done
			publication.mu.Unlock()
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-done:
			}
			continue
		}
		publication.closing = true
		publication.done = make(chan struct{})
		publication.mu.Unlock()

		metadataLock, err := acquireDirectoryLock(
			ctx, publication.directory, metadataLockName, publication.pollInterval,
		)
		if err != nil {
			publication.finishCloseAttempt(err, false)
			return err
		}
		cleanupErr := removeExactRecord(publication.directory, publication.encoded)
		if publication.liveness != nil {
			cleanupErr = errors.Join(cleanupErr, publication.liveness.Close())
		}
		cleanupErr = errors.Join(cleanupErr, metadataLock.Close())
		if publication.release != nil {
			cleanupErr = errors.Join(cleanupErr, publication.release())
		}
		publication.finishCloseAttempt(cleanupErr, true)
		return cleanupErr
	}
}

func (publication *Publication) finishCloseAttempt(err error, final bool) {
	publication.mu.Lock()
	publication.closing = false
	if final {
		publication.closed = true
		publication.err = err
	}
	close(publication.done)
	publication.mu.Unlock()
}

func readRecord(ctx context.Context, directory *userstorage.Directory) ([]byte, Metadata, error) {
	if ctx == nil {
		return nil, Metadata{}, errors.New("endpoint discovery context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, Metadata{}, err
	}
	encoded, err := directory.ReadFile(metadataFileName, maximumMetadataSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil, Metadata{}, ErrNotFound
	}
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("read local endpoint metadata: %w", err)
	}
	metadata, err := decodeMetadata(encoded)
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("validate local endpoint metadata: %w", err)
	}
	if err = validateCurrentPlatform(metadata); err != nil {
		return nil, Metadata{}, err
	}
	return encoded, metadata, nil
}

func (store *Store) acquireLock(
	ctx context.Context,
	directory *userstorage.Directory,
	name string,
) (*userstorage.Lock, error) {
	if store == nil {
		return nil, ErrClosed
	}
	return acquireDirectoryLock(ctx, directory, name, store.pollInterval)
}

func acquireDirectoryLock(
	ctx context.Context,
	directory *userstorage.Directory,
	name string,
	pollInterval time.Duration,
) (*userstorage.Lock, error) {
	if ctx == nil {
		return nil, errors.New("endpoint lock context is required")
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		lock, err := directory.AcquireLock(name)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, userstorage.ErrLockBusy) {
			return nil, fmt.Errorf("acquire endpoint lock %q: %w", name, err)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
	}
}

func (store *Store) retain(ctx context.Context) (*userstorage.Directory, func() error, error) {
	if store == nil {
		return nil, nil, ErrClosed
	}
	if ctx == nil {
		return nil, nil, errors.New("endpoint store context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closing || store.closed || store.directory == nil {
		return nil, nil, ErrClosed
	}
	store.references++
	var once sync.Once
	var releaseErr error
	return store.directory, func() error {
		once.Do(func() { releaseErr = store.releaseReference() })
		return releaseErr
	}, nil
}

func (store *Store) releaseReference() error {
	store.mu.Lock()
	if store.references > 0 {
		store.references--
	}
	directory := store.takeDirectoryIfDrained()
	store.mu.Unlock()
	if directory != nil {
		store.finishClose(directory)
	}
	store.mu.Lock()
	err := store.closeErr
	store.mu.Unlock()
	return err
}

func (store *Store) takeDirectoryIfDrained() *userstorage.Directory {
	if !store.closing || store.closed || store.finalizing || store.references != 0 || store.directory == nil {
		return nil
	}
	store.finalizing = true
	directory := store.directory
	store.directory = nil
	return directory
}

func (store *Store) finishClose(directory *userstorage.Directory) {
	err := directory.Close()
	store.mu.Lock()
	store.closeErr = errors.Join(store.closeErr, err)
	store.finalizing = false
	store.closed = true
	close(store.closeDone)
	store.mu.Unlock()
}

func removeExactRecord(directory *userstorage.Directory, expected []byte) error {
	current, err := directory.ReadFile(metadataFileName, maximumMetadataSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-read local endpoint metadata: %w", err)
	}
	if !bytes.Equal(current, expected) {
		return errors.New("local endpoint metadata changed outside its coordination lock")
	}
	if err = directory.RemoveFile(metadataFileName); err != nil {
		return fmt.Errorf("remove local endpoint metadata: %w", err)
	}
	return nil
}
