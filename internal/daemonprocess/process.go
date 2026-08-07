// Package daemonprocess owns the distribution's managed spice-agentd child
// lifetime. It never invokes a shell or places endpoint credentials in process
// arguments, errors, formatting, or diagnostics.
package daemonprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent/client/managed"
)

const daemonArgument = "serve"

// Candidate and CandidateStarter are aliases of the core managed ownership
// contract. Result is the child outcome; Wait is the containment/resource join
// and returns nil only when the caller may safely release ownership.
type (
	Candidate        = managed.Candidate
	CandidateStarter = managed.Starter
)

// RootRegistry is the daemon-side lifecycle handle for optional supervisor
// descendant registration. Generated daemon entrypoints adopt it before any
// child-capable bean starts and close it during application cleanup.
type RootRegistry interface {
	Close() error
}

type inactiveRootRegistry struct{}

func (inactiveRootRegistry) Close() error { return nil }

// Config controls only non-secret process mechanics. Environment is passed
// byte-for-byte to the child but is never formatted or included in an error.
type Config struct {
	Directory       string
	Environment     []string
	StderrBytes     int
	GracefulTimeout time.Duration
	TerminateDelay  time.Duration
}

// Starter launches the absolute spice-agentd sibling of the current binary.
type Starter struct {
	executable  string
	directory   string
	environment []string
	stderrBytes int
	graceful    time.Duration
	terminate   time.Duration
}

// New constructs a starter without launching a process.
func New(config Config) (*Starter, error) {
	current, err := os.Executable()
	if err != nil {
		return nil, redacted("locate distribution executable", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return nil, redacted("resolve distribution executable", err)
	}
	executable := filepath.Join(filepath.Dir(current), daemonExecutableName())
	return newStarter(config, executable, current)
}

func newStarter(config Config, executable, currentExecutable string) (*Starter, error) {
	if err := validateDistributionPaths(executable, currentExecutable); err != nil {
		return nil, err
	}
	if !validProcessBounds(config) {
		return nil, errors.New("managed daemon process bounds are invalid")
	}
	directory := config.Directory
	if directory == "" {
		directory = filepath.Dir(executable)
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("managed daemon working directory must be absolute and canonical")
	}
	environment := slices.Clone(config.Environment)
	if environment == nil {
		environment = os.Environ()
	}
	if !validEnvironment(environment) {
		return nil, errors.New("managed daemon environment is invalid")
	}
	return &Starter{
		executable: executable, directory: directory, environment: environment,
		stderrBytes: config.StderrBytes, graceful: config.GracefulTimeout,
		terminate: config.TerminateDelay,
	}, nil
}

func validateDistributionPaths(executable, currentExecutable string) error {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(currentExecutable) ||
		filepath.Clean(executable) != executable || filepath.Clean(currentExecutable) != currentExecutable ||
		filepath.Base(executable) != daemonExecutableName() ||
		filepath.Dir(executable) != filepath.Dir(currentExecutable) {
		return errors.New("managed daemon executable must be the absolute distribution sibling")
	}
	return nil
}

func validProcessBounds(config Config) bool {
	return config.StderrBytes > 0 && config.StderrBytes <= 1<<20 &&
		config.GracefulTimeout > 0 && config.TerminateDelay > 0
}

func validEnvironment(environment []string) bool {
	for _, value := range environment {
		if strings.IndexByte(value, 0) >= 0 {
			return false
		}
	}
	return true
}

// Start launches one independently owned daemon process using discrete fixed
// arguments. The caller owns every non-nil candidate even when an error is
// returned.
func (starter *Starter) Start(ctx context.Context) (Candidate, error) {
	if starter == nil || starter.executable == "" {
		return nil, errors.New("managed daemon starter is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("managed daemon start context is required")
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	diagnostics := newBoundedBuffer(starter.stderrBytes)
	child, err := startProcess(processSpec{
		executable:  starter.executable,
		argument:    daemonArgument,
		directory:   starter.directory,
		environment: slices.Clone(starter.environment),
		stderr:      diagnostics,
		waitDelay:   starter.terminate,
	})
	if child == nil {
		if err == nil {
			err = errors.New("platform launcher returned no managed daemon process")
		}
		return nil, redacted("start managed daemon process", err)
	}
	process := &Process{
		child: child, diagnostics: diagnostics,
		done: make(chan struct{}), graceful: starter.graceful, terminate: starter.terminate,
	}
	// startProcess publishes only a running child whose platform containment is
	// already active. Reaping therefore cannot race containment attachment.
	go process.reap()
	if err != nil {
		return process, redacted("start managed daemon process", err)
	}
	if cause := context.Cause(ctx); cause != nil {
		return process, redacted("managed daemon launch was canceled", cause)
	}
	return process, nil
}

// Process is one owned daemon candidate. Reaping begins immediately and occurs
// exactly once independently of Wait calls.
type Process struct {
	child       launchedProcess
	diagnostics *boundedBuffer
	done        chan struct{}
	graceful    time.Duration
	terminate   time.Duration

	mu       sync.Mutex
	result   error
	cleanup  error
	shutdown sync.Once
	beginErr error
}

func (process *Process) reap() {
	result := process.child.Wait()
	// Close returns the complete platform containment failure history, including
	// any Terminate or Kill failure observed by the asynchronous escalation.
	// Never publish Done with a successful result if containment cleanup failed.
	cleanup := containmentFailure(process.child.Close())
	process.mu.Lock()
	process.result = result
	process.cleanup = cleanup
	process.mu.Unlock()
	close(process.done)
}

// Done closes exactly once after the process and its stderr drain are reaped.
func (process *Process) Done() <-chan struct{} {
	if process == nil {
		return nil
	}
	return process.done
}

// Result reports the final process result after Done closes.
func (process *Process) Result() error {
	if process == nil {
		return errors.New("managed daemon candidate is unavailable")
	}
	select {
	case <-process.done:
		process.mu.Lock()
		defer process.mu.Unlock()
		return process.result
	default:
		return errors.New("managed daemon candidate is still running")
	}
}

// BeginShutdown idempotently closes the parent-control pipe and starts the
// bounded platform escalation sequence. It never waits for process exit.
func (process *Process) BeginShutdown() error {
	if process == nil || process.done == nil {
		return errors.New("managed daemon candidate is unavailable")
	}
	process.shutdown.Do(func() {
		process.beginErr = redacted("request managed daemon shutdown", process.child.CloseInput())
		go process.escalate()
	})
	return process.beginErr
}

func (process *Process) escalate() {
	if waitDone(process.done, process.graceful) {
		return
	}
	if err := process.child.Terminate(); err != nil {
		// The platform child journals the failure for Close/ContainmentError.
		if killErr := process.child.Kill(); killErr != nil {
			return
		}
		return
	}
	if waitDone(process.done, process.terminate) {
		return
	}
	if err := process.child.Kill(); err != nil {
		return
	}
}

// Wait joins containment and owned resource cleanup while honoring ctx. It does
// not return the child outcome; callers inspect Result separately after Done.
func (process *Process) Wait(ctx context.Context) error {
	if process == nil || process.done == nil {
		return errors.New("managed daemon candidate is unavailable")
	}
	if ctx == nil {
		return errors.New("managed daemon wait context is required")
	}
	select {
	case <-process.done:
		return process.cleanupResult()
	default:
	}
	select {
	case <-process.done:
		return process.cleanupResult()
	case <-ctx.Done():
		select {
		case <-process.done:
			return process.cleanupResult()
		default:
			return redacted("wait for managed daemon process", context.Cause(ctx))
		}
	}
}

func (process *Process) cleanupResult() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.cleanup
}

// ProtectedStderr returns a defensive copy of the bounded retained stderr tail.
// Callers must keep it in protected diagnostics; it never enters public errors.
func (process *Process) ProtectedStderr() []byte {
	if process == nil || process.diagnostics == nil {
		return nil
	}
	return process.diagnostics.Bytes()
}

func waitDone(done <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

type boundedBuffer struct {
	mu      sync.Mutex
	maximum int
	value   []byte
}

type processSpec struct {
	executable  string
	argument    string
	directory   string
	environment []string
	stderr      io.Writer
	waitDelay   time.Duration
}

// launchedProcess is implemented independently on each supported platform.
// Close must return the joined history of all containment failures previously
// returned by Terminate or Kill as well as its own final cleanup failure.
type launchedProcess interface {
	Wait() error
	CloseInput() error
	Terminate() error
	Kill() error
	Close() error
}

func newBoundedBuffer(maximum int) *boundedBuffer { return &boundedBuffer{maximum: maximum} }

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(value) >= buffer.maximum {
		buffer.value = append(buffer.value[:0], value[len(value)-buffer.maximum:]...)
		return len(value), nil
	}
	overflow := len(buffer.value) + len(value) - buffer.maximum
	if overflow > 0 {
		copy(buffer.value, buffer.value[overflow:])
		buffer.value = buffer.value[:len(buffer.value)-overflow]
	}
	buffer.value = append(buffer.value, value...)
	return len(value), nil
}

func (buffer *boundedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return slices.Clone(buffer.value)
}

type redactedError struct {
	message string
	cause   error
}

// ContainmentError reports that the daemon process boundary could not prove
// complete cleanup. A managed owner must retain recovery metadata when this is
// returned by Candidate.Wait, even though Done has closed and the root process
// outcome is already available through Result. This supervisor releases or
// invalidates its platform handles during the terminal cleanup attempt, so the
// failure is explicitly not retryable; recovery requires operator or a future
// platform-specific recovery implementation.
type ContainmentError struct{ cause error }

func (*ContainmentError) Error() string { return "release managed daemon process isolation" }

// Retryable lets managed ownership distinguish terminal retained state from a
// future candidate whose containment implementation supports another attempt.
func (*ContainmentError) Retryable() bool { return false }

func (failure *ContainmentError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func containmentFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return &ContainmentError{cause: cause}
}

func (failure *redactedError) Error() string { return failure.message }
func (failure *redactedError) Unwrap() error { return failure.cause }

func redacted(message string, cause error) error {
	if cause == nil {
		return nil
	}
	return &redactedError{message: message, cause: cause}
}

func (*Starter) String() string           { return "daemonprocess.Starter([REDACTED])" }
func (starter *Starter) GoString() string { return starter.String() }
func (*Starter) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "daemonprocess.Starter([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (starter *Starter) LogValue() slog.Value { return slog.StringValue(starter.String()) }

func (*Process) String() string           { return "daemonprocess.Process([REDACTED])" }
func (process *Process) GoString() string { return process.String() }
func (*Process) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "daemonprocess.Process([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (process *Process) LogValue() slog.Value { return slog.StringValue(process.String()) }

func daemonExecutableName() string {
	if runtime.GOOS == "windows" {
		return "spice-agentd.exe"
	}
	return "spice-agentd"
}

var (
	_ Candidate        = (*Process)(nil)
	_ CandidateStarter = (*Starter)(nil)
)
