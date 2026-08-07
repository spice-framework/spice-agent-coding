package runidentity

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestSourceDeterministicShapeAndReaderFailure(t *testing.T) {
	t.Parallel()
	source, err := New(bytes.NewReader(make([]byte, entropyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := source.Next("run")
	if err != nil {
		t.Fatal(err)
	}
	if id != "run-"+strings.Repeat("A", 32) {
		t.Fatalf("deterministic ID = %q", id)
	}
	failing, err := New(failingReader{})
	if err != nil {
		t.Fatal(err)
	}
	if id, err = failing.Next("run"); id != "" || err == nil || err.Error() != "generate agent ID entropy" {
		t.Fatalf("failed ID = %q, %v", id, err)
	}
}

func TestSourceRejectsNonCanonicalPrefixes(t *testing.T) {
	t.Parallel()
	source, err := New(bytes.NewReader(make([]byte, entropyBytes*16)))
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{
		"", "Run", "1run", "run_1", "run--child", "run-", " run", "run ",
		strings.Repeat("a", maximumPrefixBytes+1),
	} {
		if id, nextErr := source.Next(prefix); id != "" || nextErr == nil {
			t.Fatalf("Next(%q) = %q, %v; want rejection", prefix, id, nextErr)
		}
	}
	if source, err = New(nil); source != nil || err == nil {
		t.Fatalf("New(nil) = %v, %v", source, err)
	}
	var nilSource *Source
	if id, nextErr := nilSource.Next("run"); id != "" || nextErr == nil {
		t.Fatalf("nil Next() = %q, %v", id, nextErr)
	}
}

func TestCryptoSourceConcurrentIDsAreCanonicalAndUnique(t *testing.T) {
	t.Parallel()
	const count = 128
	source := NewCrypto()
	pattern := regexp.MustCompile(`^run-[A-Za-z0-9_-]{32}$`)
	ids := make(chan string, count)
	errorsFound := make(chan error, count)
	var workers sync.WaitGroup
	workers.Add(count)
	for range count {
		go func() {
			defer workers.Done()
			id, err := source.Next("run")
			if err != nil {
				errorsFound <- err
				return
			}
			ids <- id
		}()
	}
	workers.Wait()
	close(ids)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("Next() error = %v", err)
	}
	seen := make(map[string]struct{}, count)
	for id := range ids {
		if !pattern.MatchString(id) {
			t.Errorf("noncanonical ID %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Errorf("duplicate ID %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("unique IDs = %d, want %d", len(seen), count)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
