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
	"time"
)

// Starter launches the absolute spice-agentd sibling of the current binary.
type Starter struct {
	executable  string
	directory   string
	environment []string
	stderrBytes int
	graceful    time.Duration
	terminate   time.Duration
}

// NewStarter constructs a starter without launching a process.
func NewStarter(config Config) (*Starter, error) {
	current, err := os.Executable()
	if err != nil {
		return nil, (redactedError{}).wrap("locate distribution executable", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return nil, (redactedError{}).wrap("resolve distribution executable", err)
	}
	starter := &Starter{}
	executable := filepath.Join(filepath.Dir(current), starter.daemonExecutableName())
	if err = starter.configure(config, executable, current); err != nil {
		return nil, err
	}
	return starter, nil
}

func (starter *Starter) configure(config Config, executable, currentExecutable string) error {
	if err := starter.validateDistributionPaths(executable, currentExecutable); err != nil {
		return err
	}
	if !config.validBounds() {
		return errors.New("managed daemon process bounds are invalid")
	}
	directory := config.Directory
	if directory == "" {
		directory = filepath.Dir(executable)
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("managed daemon working directory must be absolute and canonical")
	}
	environment := slices.Clone(config.Environment)
	if environment == nil {
		environment = os.Environ()
	}
	if !config.validEnvironment(environment) {
		return errors.New("managed daemon environment is invalid")
	}
	starter.executable = executable
	starter.directory = directory
	starter.environment = environment
	starter.stderrBytes = config.StderrBytes
	starter.graceful = config.GracefulTimeout
	starter.terminate = config.TerminateDelay
	return nil
}

func (starter *Starter) validateDistributionPaths(executable, currentExecutable string) error {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(currentExecutable) ||
		filepath.Clean(executable) != executable || filepath.Clean(currentExecutable) != currentExecutable ||
		filepath.Base(executable) != starter.daemonExecutableName() ||
		filepath.Dir(executable) != filepath.Dir(currentExecutable) {
		return errors.New("managed daemon executable must be the absolute distribution sibling")
	}
	return nil
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
	diagnostics := &boundedBuffer{maximum: starter.stderrBytes}
	child, err := (processFactory{}).start(processSpec{
		executable: starter.executable, argument: daemonArgument, directory: starter.directory,
		environment: slices.Clone(starter.environment), stderr: diagnostics, waitDelay: starter.terminate,
	})
	if child == nil {
		if err == nil {
			err = errors.New("platform launcher returned no managed daemon process")
		}
		return nil, (redactedError{}).wrap("start managed daemon process", err)
	}
	process := &Process{
		child: child, diagnostics: diagnostics,
		done: make(chan struct{}), graceful: starter.graceful, terminate: starter.terminate,
	}
	go process.reap()
	if err != nil {
		return process, (redactedError{}).wrap("start managed daemon process", err)
	}
	if cause := context.Cause(ctx); cause != nil {
		return process, (redactedError{}).wrap("managed daemon launch was canceled", cause)
	}
	return process, nil
}

func (*Starter) daemonExecutableName() string {
	if runtime.GOOS == "windows" {
		return "spice-agentd.exe"
	}
	return "spice-agentd"
}

func (*Starter) String() string           { return "daemonprocess.Starter([REDACTED])" }
func (starter *Starter) GoString() string { return starter.String() }
func (*Starter) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "daemonprocess.Starter([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (starter *Starter) LogValue() slog.Value { return slog.StringValue(starter.String()) }
