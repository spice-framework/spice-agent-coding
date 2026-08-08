//go:build windows

package processplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/testpath"

	agentprocess "github.com/spice-framework/spice-agent/process"
	"golang.org/x/sys/windows"
)

func TestWindowsKernelWaitAndEnvironmentBoundaries(t *testing.T) {
	t.Parallel()

	for _, handle := range []windows.Handle{0, windows.InvalidHandle} {
		if err := waitWindowsHandle(handle, time.Second); err == nil {
			t.Fatalf("waitWindowsHandle(%v) succeeded", handle)
		}
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := closeWindowsHandle(event); closeErr != nil {
			t.Errorf("close event: %v", closeErr)
		}
	})
	if err = waitWindowsHandle(event, time.Nanosecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unsignaled event wait = %v", err)
	}
	if err = windows.SetEvent(event); err != nil {
		t.Fatal(err)
	}
	if err = waitWindowsHandle(event, time.Second); err != nil {
		t.Fatalf("signaled event wait = %v", err)
	}

	job, err := newPlatformJob()
	if err != nil {
		t.Fatal(err)
	}
	if err = waitWindowsJobEmpty(job); err != nil {
		closeErr := closeWindowsHandle(job)
		t.Fatalf("empty job wait: %v; close: %v", err, closeErr)
	}
	if err = closeWindowsHandle(job); err != nil {
		t.Fatal(err)
	}
	if err = waitWindowsJobEmpty(0); err == nil {
		t.Fatal("nil job wait succeeded")
	}

	empty, err := windowsEnvironmentBlock(nil)
	if err != nil || len(empty) != 2 || empty[0] != 0 || empty[1] != 0 {
		t.Fatalf("empty environment = %#v, %v", empty, err)
	}
	if _, err = windowsEnvironmentBlock([]string{"PRIVATE=before\x00after"}); err == nil {
		t.Fatal("nul environment succeeded")
	}
	if err = closeWindowsHandles(0, windows.InvalidHandle); err != nil || closeWindowsFile(nil) != nil {
		t.Fatalf("empty close helpers = %v", err)
	}
}

func TestWindowsAbortTransfersEveryLiveHandle(t *testing.T) {
	t.Parallel()
	for _, assigned := range []bool{false, true} {
		t.Run(fmt.Sprintf("assigned=%t", assigned), func(t *testing.T) {
			t.Parallel()
			root := testpath.TempDir(t)
			executable := installProcessHelper(t, root, "suspended")
			spec := helperSpec(t, executable, root, "blocked", strings.NewReader(""), io.Discard, io.Discard, nil)
			job, err := newPlatformJob()
			if err != nil {
				t.Fatal(err)
			}
			pipes, err := newPlatformPipes()
			if err != nil {
				closeErr := closeWindowsHandle(job)
				t.Fatalf("create pipes: %v; close job: %v", err, closeErr)
			}
			information, err := createSuspendedWindowsProcess(spec, pipes)
			if err != nil {
				pipeErr := pipes.closeAll()
				jobErr := closeWindowsHandle(job)
				t.Fatalf("create suspended process: %v; pipes: %v; job: %v", err, pipeErr, jobErr)
			}
			if assigned {
				if err = windows.AssignProcessToJobObject(job, information.Process); err != nil {
					terminateErr := windows.TerminateProcess(information.Process, windowsKillExitCode)
					waitErr := waitWindowsHandle(information.Process, 5*time.Second)
					closeErr := errors.Join(
						pipes.closeAll(), closeWindowsHandle(information.Thread),
						closeWindowsHandle(information.Process), closeWindowsHandle(job),
					)
					t.Fatalf("assign suspended process: %v; terminate: %v; wait: %v; close: %v",
						err, terminateErr, waitErr, closeErr)
				}
			}
			sentinel := errors.New("intentional launch abort")
			candidate, abortErr := abortWindowsLaunch(job, pipes, information, assigned, spec, sentinel, nil)
			if candidate == nil || !errors.Is(abortErr, sentinel) {
				t.Fatalf("abort = %T, %v", candidate, abortErr)
			}
			owned, ok := candidate.(*windowsProcess)
			if !ok {
				t.Fatalf("candidate = %T", candidate)
			}
			joined, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err = owned.Wait(joined); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := owned.Result()
			if resultErr != nil || outcome.Kind() != agentprocess.OutcomeSignaled {
				t.Fatalf("aborted outcome = %#v, %v", outcome, resultErr)
			}
			owned.mu.Lock()
			defer owned.mu.Unlock()
			if !owned.closed || owned.process != 0 || owned.job != 0 || owned.thread != 0 {
				t.Fatalf("aborted process retained handles: %#v", owned)
			}
		})
	}
}

func TestWindowsWaitJoinsBlockedStdinCopy(t *testing.T) {
	t.Parallel()

	root := testpath.TempDir(t)
	executable := installProcessHelper(t, root, "early-exit")
	input := &gatedEOFReader{started: make(chan struct{}), release: make(chan struct{})}
	spec := helperSpec(t, executable, root, "early", input, io.Discard, io.Discard, nil)
	owned, err := mustLauncher(t).Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-input.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stdin copy did not start")
	}
	waitForDone(t, owned.Done())

	short, cancelShort := context.WithTimeout(t.Context(), 25*time.Millisecond)
	err = owned.Wait(short)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait returned before stdin ownership joined: %v", err)
	}

	close(input.release)
	joined, cancelJoin := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelJoin()
	if err = owned.Wait(joined); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsReservedTerminationCodeIsAnOrdinaryUnsignaledExit(t *testing.T) {
	t.Parallel()

	root := testpath.TempDir(t)
	executable := installProcessHelper(t, root, "reserved-exit")
	spec := helperSpec(t, executable, root, "reserved-exit", strings.NewReader(""), io.Discard, io.Discard, nil)
	owned, err := mustLauncher(t).Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	joined, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = owned.Wait(joined); err != nil {
		t.Fatal(err)
	}
	outcome, err := owned.Result()
	code, hasCode := outcome.ExitCode()
	if err != nil || outcome.Kind() != agentprocess.OutcomeExited || !hasCode || code != helperReservedExitCode {
		t.Fatalf("reserved-code outcome = %#v, %d, %t, %v", outcome, code, hasCode, err)
	}
}

type gatedEOFReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (reader *gatedEOFReader) Read([]byte) (int, error) {
	reader.once.Do(func() { close(reader.started) })
	<-reader.release
	return 0, io.EOF
}

func TestWindowsProcessNilAndFormattingBoundaries(t *testing.T) {
	t.Parallel()

	var owned *windowsProcess
	if owned.Done() != nil {
		t.Fatal("nil process exposed Done")
	}
	if _, err := owned.Result(); err == nil {
		t.Fatal("nil process exposed Result")
	}
	if err := owned.RequestStop(t.Context()); err == nil {
		t.Fatal("nil process stop succeeded")
	}
	if err := owned.ForceKill(t.Context()); err == nil {
		t.Fatal("nil process kill succeeded")
	}
	if err := owned.Wait(t.Context()); err == nil {
		t.Fatal("nil process wait succeeded")
	}
	owned = &windowsProcess{}
	for _, rendered := range []string{
		fmt.Sprint(owned), fmt.Sprintf("%#v", owned), fmt.Sprintf("%+v", owned), owned.LogValue().String(),
	} {
		if rendered != "processplatform.Process([REDACTED])" {
			t.Fatalf("process formatting = %q", rendered)
		}
	}
}
