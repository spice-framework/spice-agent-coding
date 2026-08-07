package daemonprocess

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewAndStarterRejectInvalidProcessBoundaries(t *testing.T) {
	t.Parallel()

	valid := Config{
		Environment:     []string{"PATH=value"},
		StderrBytes:     64,
		GracefulTimeout: time.Second,
		TerminateDelay:  time.Second,
	}
	if starter, err := New(valid); err != nil || starter == nil {
		t.Fatalf("New(valid) = %v, %v", starter, err)
	}

	directory := t.TempDir()
	executable := filepath.Join(directory, daemonExecutableName())
	launcher := filepath.Join(directory, launcherExecutableName())
	valid.Directory = directory
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "relative directory", mutate: func(config *Config) { config.Directory = "relative" }},
		{name: "noncanonical directory", mutate: func(config *Config) { config.Directory += string(filepath.Separator) + "." }},
		{name: "empty stderr bound", mutate: func(config *Config) { config.StderrBytes = 0 }},
		{name: "excessive stderr bound", mutate: func(config *Config) { config.StderrBytes = 1<<20 + 1 }},
		{name: "empty graceful timeout", mutate: func(config *Config) { config.GracefulTimeout = 0 }},
		{name: "empty terminate delay", mutate: func(config *Config) { config.TerminateDelay = 0 }},
		{name: "nul environment", mutate: func(config *Config) { config.Environment = []string{"SECRET=before\x00after"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := valid
			test.mutate(&config)
			if starter, err := newStarter(config, executable, launcher); err == nil || starter != nil {
				t.Fatalf("newStarter(invalid) = %v, %v", starter, err)
			}
		})
	}

	ambient := valid
	ambient.Environment = nil
	starter, err := newStarter(ambient, executable, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(starter.environment) == 0 {
		t.Fatal("nil environment did not inherit the parent environment")
	}
}

func TestProcessEscalationControlBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		child         *controlledLaunchedProcess
		doneInitially bool
		wantTerminate int
		wantKill      int
	}{
		{name: "already done", child: &controlledLaunchedProcess{}, doneInitially: true},
		{
			name: "termination failure falls through to kill",
			child: &controlledLaunchedProcess{
				terminateErr: errors.New("terminate failed"),
				killErr:      errors.New("kill failed"),
			},
			wantTerminate: 1,
			wantKill:      1,
		},
		{
			name:          "termination timeout escalates to kill",
			child:         &controlledLaunchedProcess{},
			wantTerminate: 1,
			wantKill:      1,
		},
		{
			name:          "termination completion avoids kill",
			child:         &controlledLaunchedProcess{completeOnTerminate: true},
			wantTerminate: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			done := make(chan struct{})
			if test.doneInitially {
				close(done)
			}
			test.child.done = done
			graceful := -time.Nanosecond
			if test.doneInitially {
				graceful = time.Second
			}
			terminate := -time.Nanosecond
			if test.child.completeOnTerminate {
				terminate = time.Second
			}
			process := &Process{child: test.child, done: done, graceful: graceful, terminate: terminate}
			process.escalate()
			if test.child.terminateCalls != test.wantTerminate || test.child.killCalls != test.wantKill {
				t.Fatalf("calls = terminate %d, kill %d; want %d, %d",
					test.child.terminateCalls, test.child.killCalls, test.wantTerminate, test.wantKill)
			}
		})
	}
}

func TestProcessBoundaryFormattingAndBuffers(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	process := &Process{done: done}
	if result := process.Result(); result == nil || !strings.Contains(result.Error(), "still running") {
		t.Fatalf("running Result() = %v", result)
	}
	if err := process.Wait(nil); err == nil { //nolint:staticcheck // Boundary deliberately verifies nil-context rejection.
		t.Fatal("Wait(nil) succeeded")
	}

	for _, rendered := range []string{
		fmt.Sprint(process),
		fmt.Sprintf("%#v", process),
		fmt.Sprintf("%v", process),
		process.LogValue().String(),
	} {
		if rendered != "daemonprocess.Process([REDACTED])" {
			t.Fatalf("process formatting = %q", rendered)
		}
	}

	buffer := newBoundedBuffer(5)
	if written, err := buffer.Write([]byte("ab")); err != nil || written != 2 {
		t.Fatalf("first write = %d, %v", written, err)
	}
	if written, err := buffer.Write([]byte("cdef")); err != nil || written != 4 {
		t.Fatalf("overflow write = %d, %v", written, err)
	}
	if got := string(buffer.Bytes()); got != "bcdef" {
		t.Fatalf("overflow tail = %q", got)
	}
	if written, err := buffer.Write([]byte("123456")); err != nil || written != 6 {
		t.Fatalf("replacement write = %d, %v", written, err)
	}
	if got := string(buffer.Bytes()); got != "23456" {
		t.Fatalf("replacement tail = %q", got)
	}

	var containment *ContainmentError
	if containment.Unwrap() != nil {
		t.Fatal("nil containment error has a cause")
	}
	registry := inactiveRootRegistry{}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (*Process)(nil).Wait(context.Background()); err == nil {
		t.Fatal("nil process wait succeeded")
	}
}

type controlledLaunchedProcess struct {
	done                chan struct{}
	terminateErr        error
	killErr             error
	completeOnTerminate bool
	terminateCalls      int
	killCalls           int
}

func (*controlledLaunchedProcess) Wait() error       { return nil }
func (*controlledLaunchedProcess) CloseInput() error { return nil }
func (child *controlledLaunchedProcess) Terminate() error {
	child.terminateCalls++
	if child.completeOnTerminate {
		close(child.done)
	}
	return child.terminateErr
}

func (child *controlledLaunchedProcess) Kill() error {
	child.killCalls++
	return child.killErr
}
func (*controlledLaunchedProcess) Close() error { return nil }
