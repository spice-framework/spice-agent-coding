// Command cancelhelper creates a small parent/child process tree for installed
// cancellation acceptance. It is test data and is never included in release
// archives.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const readinessTimeout = 10 * time.Second

func main() {
	if len(os.Args) != 3 {
		fail(errors.New("usage: cancelhelper <root|child> <directory>"))
	}
	directory, err := filepath.Abs(os.Args[2])
	if err != nil || directory != filepath.Clean(os.Args[2]) {
		fail(errors.New("cancellation directory must be canonical and absolute"))
	}
	if err = os.MkdirAll(directory, 0o700); err != nil {
		fail(fmt.Errorf("create cancellation directory: %w", err))
	}
	switch os.Args[1] {
	case "root":
		runRoot(directory)
	case "child":
		runChild(directory)
	default:
		fail(errors.New("cancellation helper role is unsupported"))
	}
}

func runRoot(directory string) {
	command := exec.Command(os.Args[0], "child", directory) // #nosec G204,G702 -- exact test binary and canonical test-owned directory.
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		fail(fmt.Errorf("start child: %w", err))
	}
	if err := writeMarker(directory, "root.pid", strconv.Itoa(os.Getpid())); err != nil {
		fail(err)
	}
	deadline := time.Now().Add(readinessTimeout)
	for {
		if _, err := os.Stat(filepath.Join(directory, "child.ready")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			fail(fmt.Errorf("inspect child readiness: %w", err))
		}
		if time.Now().After(deadline) {
			fail(errors.New("child readiness timed out"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := writeMarker(directory, "shell.ready", "ready"); err != nil {
		fail(err)
	}
	if err := command.Wait(); err != nil {
		fail(fmt.Errorf("wait for child: %w", err))
	}
	fail(errors.New("child exited without cancellation"))
}

func runChild(directory string) {
	if err := writeMarker(directory, "child.pid", strconv.Itoa(os.Getpid())); err != nil {
		fail(err)
	}
	if err := writeMarker(directory, "child.ready", "ready"); err != nil {
		fail(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func writeMarker(directory, name, value string) error {
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
