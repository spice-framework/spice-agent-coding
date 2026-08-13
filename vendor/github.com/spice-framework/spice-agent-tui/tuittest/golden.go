package tuittest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// UpdateGoldenEnv enables rewriting golden files when set to a truthy value.
	UpdateGoldenEnv = "UPDATE_GOLDEN"
	styledSuffix    = ".styled.golden"
	plainSuffix     = ".plain.golden"
	reportSuffix    = ".report.txt"
)

// GoldenPaths are the on-disk artifacts for one named screen.
type GoldenPaths struct {
	Styled string
	Plain  string
	Report string
}

// PathsFor returns conventional golden paths under dir for name.
func PathsFor(dir, name string) GoldenPaths {
	base := filepath.Join(dir, name)
	return GoldenPaths{
		Styled: base + styledSuffix,
		Plain:  base + plainSuffix,
		Report: base + reportSuffix,
	}
}

// UpdateGolden reports whether golden rewrite mode is enabled.
func UpdateGolden() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(UpdateGoldenEnv)))
	return value == "1" || value == "true" || value == "yes"
}

// AssertGolden compares screen against styled+plain goldens.
// When UPDATE_GOLDEN is enabled it rewrites the goldens and the agent report.
func (screen Screen) AssertGolden(tb testing.TB, dir, name string) {
	tb.Helper()
	if err := screen.CompareGolden(dir, name); err != nil {
		tb.Fatal(err)
	}
}

// CompareGolden performs the golden comparison without requiring testing.TB.
// Agents and non-test runners can call it directly.
func (screen Screen) CompareGolden(dir, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("golden name must not be empty")
	}
	paths := PathsFor(dir, name)
	if UpdateGolden() {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create golden directory: %w", err)
		}
		if err := writeFile(paths.Styled, screen.styled+"\n"); err != nil {
			return err
		}
		if err := writeFile(paths.Plain, screen.plain+"\n"); err != nil {
			return err
		}
		if err := writeFile(paths.Report, screen.AgentReport()); err != nil {
			return err
		}
		return nil
	}
	wantStyled, err := readGolden(paths.Styled)
	if err != nil {
		return err
	}
	wantPlain, err := readGolden(paths.Plain)
	if err != nil {
		return err
	}
	wantReport, err := readReport(paths.Report)
	if err != nil {
		return err
	}
	want := Screen{
		width:         screen.width,
		height:        screen.height,
		styled:        wantStyled,
		plain:         wantPlain,
		cursorX:       screen.cursorX,
		cursorY:       screen.cursorY,
		cursorVisible: screen.cursorVisible,
	}
	// Pixel content is compared first so layout failures receive a focused diff.
	// The report below carries cursor and semantic metadata.
	if screen.styled != wantStyled || screen.plain != wantPlain {
		diff := screen.Diff(want)
		return fmt.Errorf("%s\nset %s=1 to refresh goldens", diff, UpdateGoldenEnv)
	}
	if gotReport := screen.AgentReport(); gotReport != wantReport {
		return fmt.Errorf(
			"screen metadata mismatch\n--- got report ---\n%s\n--- want report ---\n%s\nset %s=1 to refresh goldens",
			gotReport,
			wantReport,
			UpdateGoldenEnv,
		)
	}
	return nil
}

// LoadGoldenScreen loads styled+plain goldens as a Screen for offline compare.
func LoadGoldenScreen(dir, name string) (Screen, error) {
	paths := PathsFor(dir, name)
	styled, err := readGolden(paths.Styled)
	if err != nil {
		return Screen{}, err
	}
	plain, err := readGolden(paths.Plain)
	if err != nil {
		return Screen{}, err
	}
	plainLines := splitLines(plain)
	height := len(plainLines)
	width := 0
	for _, line := range plainLines {
		if w := CellWidth(line); w > width {
			width = w
		}
	}
	return Screen{
		name:       name,
		width:      width,
		height:     height,
		styled:     styled,
		plain:      plain,
		plainLines: plainLines,
	}, nil
}

func readGolden(path string) (string, error) {
	content, err := os.ReadFile(path) // #nosec G304 -- caller-controlled test fixture path.
	if err != nil {
		return "", fmt.Errorf("read golden %s: %w (set %s=1 to create)", path, err, UpdateGoldenEnv)
	}
	return strings.TrimSuffix(string(content), "\n"), nil
}

func readReport(path string) (string, error) {
	content, err := os.ReadFile(path) // #nosec G304 -- caller-controlled test fixture path.
	if err != nil {
		return "", fmt.Errorf("read golden %s: %w (set %s=1 to create)", path, err, UpdateGoldenEnv)
	}
	return string(content), nil
}

func writeFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { // #nosec G306 -- test fixtures.
		return fmt.Errorf("write golden %s: %w", path, err)
	}
	return nil
}
