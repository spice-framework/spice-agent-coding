package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

var (
	errDigestMismatch   = errors.New("plugin executable digest mismatch")
	errIdentityMismatch = errors.New("plugin executable identity mismatch")
	errLeaseClosed      = errors.New("plugin executable verification lease is closed")
)

// SHA256 is an exact executable content identity. Its bytes are private so a
// caller cannot mutate a validated digest through a shared backing array.
type SHA256 struct{ value [sha256.Size]byte }

// ParseSHA256 accepts exactly 64 lowercase hexadecimal characters.
func ParseSHA256(value string) (SHA256, error) {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return SHA256{}, configFailure("sha256", -1, ProblemMalformed)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return SHA256{}, configFailure("sha256", -1, ProblemMalformed)
	}
	var result SHA256
	copy(result.value[:], decoded)
	return result, nil
}

// String returns the canonical lowercase hexadecimal digest.
func (digest SHA256) String() string { return hex.EncodeToString(digest.value[:]) }

func newSHA256(value [sha256.Size]byte) SHA256 { return SHA256{value: value} }

func (digest SHA256) equal(other SHA256) bool { return digest.value == other.value }
func (digest SHA256) isZero() bool            { return digest.value == [sha256.Size]byte{} }

// verifiedExecutable holds the verified file open across process launch. The
// pathname-based Launcher contract cannot universally execute this exact open
// handle, so recheck must run immediately after Start; the held identity and
// digest make replacement detectable on every supported platform.
type verifiedExecutable struct {
	mutex      sync.Mutex
	file       *os.File
	identity   fileIdentity
	executable Executable
}

func openVerifiedExecutable(ctx context.Context, executable Executable) (*verifiedExecutable, error) {
	if ctx == nil {
		return nil, verificationFailure(verificationOpen, errors.New("nil context"))
	}
	if err := executable.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, verificationFailure(verificationOpen, err)
	}
	file, identity, err := openExecutableFile(executable.Path())
	if err != nil {
		return nil, verificationFailure(verificationOpen, err)
	}
	lease := &verifiedExecutable{file: file, identity: identity, executable: executable.Clone()}
	digest, err := hashExecutable(ctx, file)
	if err != nil {
		_ = file.Close()
		return nil, verificationFailure(verificationHash, err)
	}
	if !digest.equal(executable.SHA256()) {
		_ = file.Close()
		return nil, verificationFailure(verificationHash, errDigestMismatch)
	}
	return lease, nil
}

func (lease *verifiedExecutable) Recheck(ctx context.Context) error {
	if ctx == nil {
		return verificationFailure(verificationRecheck, errors.New("nil context"))
	}
	if lease == nil {
		return verificationFailure(verificationRecheck, errLeaseClosed)
	}
	if err := ctx.Err(); err != nil {
		return verificationFailure(verificationRecheck, err)
	}
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	if lease.file == nil {
		return verificationFailure(verificationRecheck, errLeaseClosed)
	}
	current, identity, err := openExecutableFile(lease.executable.Path())
	if err != nil {
		return verificationFailure(verificationRecheck, err)
	}
	defer func() { _ = current.Close() }()
	if !lease.identity.equal(identity) {
		return verificationFailure(verificationRecheck, errIdentityMismatch)
	}
	digest, err := hashExecutable(ctx, current)
	if err != nil {
		return verificationFailure(verificationRecheck, err)
	}
	if !digest.equal(lease.executable.SHA256()) {
		return verificationFailure(verificationRecheck, errDigestMismatch)
	}
	return nil
}

func (lease *verifiedExecutable) Close() error {
	if lease == nil {
		return nil
	}
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	if lease.file == nil {
		return nil
	}
	err := lease.file.Close()
	lease.file = nil
	if err != nil {
		return verificationFailure(verificationClose, err)
	}
	return nil
}

func hashExecutable(ctx context.Context, file *os.File) (SHA256, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return SHA256{}, err
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return SHA256{}, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return SHA256{}, readErr
		}
	}
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return newSHA256(sum), nil
}
