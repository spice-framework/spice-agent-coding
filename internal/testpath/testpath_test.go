package testpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempDirIsCanonicalAndExisting(t *testing.T) {
	directory := TempDir(t)
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		t.Fatalf("canonical temporary directory = %q", directory)
	}
	if resolved := Resolve(t, directory); resolved != directory {
		t.Fatalf("resolved temporary directory = %q, want %q", resolved, directory)
	}
}

func TestEnvironmentReplacesInheritedValuesExactlyOnce(t *testing.T) {
	t.Setenv("SPICE_TESTPATH_ENVIRONMENT", "inherited")
	environment := Environment(map[string]string{
		"SPICE_TESTPATH_ENVIRONMENT": "replacement",
		"SPICE_TESTPATH_SECOND":      "second",
	})
	var selected []string
	for _, entry := range environment {
		if strings.HasPrefix(entry, "SPICE_TESTPATH_ENVIRONMENT=") {
			selected = append(selected, entry)
		}
	}
	if len(selected) != 1 || selected[0] != "SPICE_TESTPATH_ENVIRONMENT=replacement" {
		t.Fatalf("selected environment = %v", selected)
	}
	if os.Getenv("SPICE_TESTPATH_ENVIRONMENT") != "inherited" {
		t.Fatal("Environment mutated the parent process")
	}
}
