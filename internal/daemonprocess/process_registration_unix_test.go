//go:build linux || darwin

package daemonprocess

import (
	"errors"
	"io"
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAwaitDescendantRegistrationContract(t *testing.T) {
	original, existed := os.LookupEnv(descendantGateEnvironment)
	t.Cleanup(func() {
		var err error
		if existed {
			err = os.Setenv(descendantGateEnvironment, original)
		} else {
			err = os.Unsetenv(descendantGateEnvironment)
		}
		if err != nil {
			t.Errorf("restore descendant gate environment: %v", err)
		}
	})

	if err := os.Unsetenv(descendantGateEnvironment); err != nil {
		t.Fatal(err)
	}
	if err := AwaitDescendantRegistration(); err == nil {
		t.Fatal("missing descendant gate succeeded")
	}
	if err := os.Setenv(descendantGateEnvironment, "invalid"); err != nil {
		t.Fatal(err)
	}
	if err := AwaitDescendantRegistration(); err == nil {
		t.Fatal("malformed descendant gate succeeded")
	}
	if err := os.Setenv(descendantGateEnvironment, "2"); err != nil {
		t.Fatal(err)
	}
	if err := AwaitDescendantRegistration(); err == nil {
		t.Fatal("reserved descriptor descendant gate succeeded")
	}
	if err := os.Setenv(descendantGateEnvironment, "999999"); err != nil {
		t.Fatal(err)
	}
	if err := AwaitDescendantRegistration(); err == nil {
		t.Fatal("closed descendant gate succeeded")
	}

	if err := exchangeDescendantGate(t, 0); err == nil {
		t.Fatal("invalid descendant release succeeded")
	}
	if err := exchangeDescendantGate(t, 1); err != nil {
		t.Fatalf("valid descendant registration: %v", err)
	}
}

func TestDescendantRegistrationReadFailure(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := os.NewFile(uintptr(fds[0]), "descendant-supervisor-test")
	if supervisor == nil {
		_ = unix.Close(fds[0]) //nolint:errcheck // Fatal cleanup after file-wrapper construction failure.
		_ = unix.Close(fds[1]) //nolint:errcheck // Fatal cleanup after file-wrapper construction failure.
		t.Fatal("construct supervisor gate file")
	}
	t.Setenv(descendantGateEnvironment, strconv.Itoa(fds[1]))

	received := make(chan error, 1)
	go func() {
		ready := []byte{0}
		_, readErr := io.ReadFull(supervisor, ready)
		received <- errors.Join(readErr, supervisor.Close())
	}()
	if err = AwaitDescendantRegistration(); err == nil {
		t.Fatal("closed supervisor release succeeded")
	}
	if exchangeErr := <-received; exchangeErr != nil {
		t.Fatalf("receive descendant acknowledgement: %v", exchangeErr)
	}
}

func TestUnixBoundaryFormattingKillAndRegistryClose(t *testing.T) {
	var starter *Starter
	var process *Process
	if starter.GoString() != starter.String() || process.GoString() != process.String() {
		t.Fatal("redacted Go formatting differs from string formatting")
	}
	if err := (*unixLaunchedProcess)(nil).Kill(); err == nil {
		t.Fatal("nil Unix process kill succeeded")
	}
	if err := (*DescendantRegistry)(nil).Close(); err != nil {
		t.Fatalf("close nil descendant registry: %v", err)
	}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	registryFile := os.NewFile(uintptr(fds[0]), "descendant-registry-test")
	peer := os.NewFile(uintptr(fds[1]), "descendant-registry-peer-test")
	if registryFile == nil || peer == nil {
		if registryFile != nil {
			_ = registryFile.Close() //nolint:errcheck // Fatal cleanup after peer-wrapper construction failure.
		}
		if peer != nil {
			_ = peer.Close() //nolint:errcheck // Fatal cleanup after peer-wrapper construction failure.
		}
		t.Fatal("construct descendant registry files")
	}
	defer peer.Close() //nolint:errcheck // Test cleanup owns the peer.
	registry := &DescendantRegistry{file: registryFile}
	if err = registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err = registry.Close(); err != nil {
		t.Fatalf("idempotent descendant registry close: %v", err)
	}
}

func exchangeDescendantGate(t *testing.T, release byte) error {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := os.NewFile(uintptr(fds[0]), "descendant-supervisor-test")
	if supervisor == nil {
		_ = unix.Close(fds[0]) //nolint:errcheck // Fatal cleanup after file-wrapper construction failure.
		_ = unix.Close(fds[1]) //nolint:errcheck // Fatal cleanup after file-wrapper construction failure.
		t.Fatal("construct supervisor gate file")
	}
	if err = os.Setenv(descendantGateEnvironment, strconv.Itoa(fds[1])); err != nil {
		_ = supervisor.Close() //nolint:errcheck // Fatal cleanup after environment setup failure.
		_ = unix.Close(fds[1]) //nolint:errcheck // Fatal cleanup after environment setup failure.
		t.Fatal(err)
	}

	exchanged := make(chan error, 1)
	go func() {
		ready := []byte{0}
		_, readErr := io.ReadFull(supervisor, ready)
		if readErr == nil && ready[0] != 1 {
			readErr = errors.New("invalid descendant acknowledgement")
		}
		if readErr == nil {
			_, readErr = supervisor.Write([]byte{release})
		}
		exchanged <- errors.Join(readErr, supervisor.Close())
	}()
	awaitErr := AwaitDescendantRegistration()
	if exchangeErr := <-exchanged; exchangeErr != nil {
		t.Fatalf("exchange descendant gate: %v", exchangeErr)
	}
	return awaitErr
}
