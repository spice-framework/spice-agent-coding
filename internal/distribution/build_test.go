package distribution

import (
	"bytes"
	"errors"
	"testing"
)

func TestDevelopmentIdentityIsHonest(t *testing.T) {
	t.Parallel()
	if Version != "0.1.0-preview.1-dev" || Commit != "development" {
		t.Fatalf("development identity = %q %q", Version, Commit)
	}
}

func TestWriteVersion(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := NewBuild().WriteVersion(&output, TerminalComponent); err != nil {
		t.Fatal(err)
	}
	want := TerminalComponent + " " + Version + " (" + Commit + ")\n"
	if output.String() != want {
		t.Fatalf("version output = %q, want %q", output.String(), want)
	}
}

func TestWriteVersionRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()
	if err := NewBuild().WriteVersion(nil, TerminalComponent); err == nil {
		t.Fatal("nil output accepted")
	}
	if err := NewBuild().WriteVersion(&failingWriter{}, "other"); err == nil {
		t.Fatal("unknown component accepted")
	}
	if err := NewBuild().WriteVersion(&failingWriter{}, DaemonComponent); !errors.Is(err, errWrite) {
		t.Fatalf("write error = %v", err)
	}
}

func TestBuildRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()
	if err := (Build{}).WriteVersion(&bytes.Buffer{}, TerminalComponent); err == nil {
		t.Fatal("incomplete identity accepted")
	}
}

var errWrite = errors.New("write failed")

type failingWriter struct{}

func (*failingWriter) Write([]byte) (int, error) { return 0, errWrite }
