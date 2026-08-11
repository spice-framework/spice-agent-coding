package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// testVerifier owns lint, security, test, coverage, and offline verification.
type testVerifier struct{}

func (owner testVerifier) lint(ctx context.Context, root string) error {
	golangci, err := (commandRunner{}).toolPath(ctx, root, "golangci-lint")
	if err != nil {
		return err
	}
	if lintErr := (commandRunner{}).command(ctx, root, nil, golangci, "run", "--timeout=10m"); lintErr != nil {
		return lintErr
	}
	nilaway, err := (commandRunner{}).toolPath(ctx, root, "nilaway")
	if err != nil {
		return err
	}
	return (commandRunner{}).command(ctx, root, nil, nilaway, "-include-pkgs="+modulePath, "./...")
}

func (owner testVerifier) security(ctx context.Context, root string) error {
	gosec, err := (commandRunner{}).toolPath(ctx, root, "gosec")
	if err != nil {
		return err
	}
	if securityErr := (commandRunner{}).command(ctx, root, nil, gosec, "-quiet", "-exclude-generated", "./..."); securityErr != nil {
		return securityErr
	}
	govulncheck, err := (commandRunner{}).toolPath(ctx, root, "govulncheck")
	if err != nil {
		return err
	}
	return (commandRunner{}).command(ctx, root, nil, govulncheck, "./...")
}

func (owner testVerifier) productPackages(ctx context.Context, root string) ([]string, error) {
	content, err := (commandRunner{}).capture(ctx, root, "go", "list", "-f={{.ImportPath}}", "./...")
	if err != nil {
		return nil, err
	}
	gate := modulePath + "/internal/qualitygate"
	result := make([]string, 0)
	for candidate := range strings.FieldsSeq(content) {
		if candidate != gate {
			result = append(result, candidate)
		}
	}
	slices.Sort(result)
	if len(result) == 0 {
		return nil, errors.New("repository has no product packages")
	}
	return result, nil
}

func (owner testVerifier) tests(ctx context.Context, root string, race, includeAcceptance bool) error {
	packages, err := (testVerifier{}).productPackages(ctx, root)
	if err != nil {
		return err
	}
	if !includeAcceptance {
		packages = (testVerifier{}).coverageTestPackages(packages)
	}
	arguments := []string{"test"}
	if race {
		arguments = append(arguments, "-race")
	}
	arguments = append(arguments, "-shuffle=on", "-count=1")
	prefixLength := len(arguments)
	if err := (commandRunner{}).command(ctx, root, nil, "go", append(arguments, packages...)...); err != nil {
		return err
	}
	return (commandRunner{}).command(ctx, root, nil, "go", append(arguments[:prefixLength], "./internal/qualitygate")...)
}

func (owner testVerifier) coverage(ctx context.Context, root string) (resultErr error) {
	packages, err := (testVerifier{}).productPackages(ctx, root)
	if err != nil {
		return err
	}
	profile, err := os.CreateTemp("", "spice-agent-coding-coverage-*.out")
	if err != nil {
		return err
	}
	path := profile.Name()
	if closeErr := profile.Close(); closeErr != nil {
		return closeErr
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(path)) }()
	testPackages := (testVerifier{}).coverageTestPackages(packages)
	arguments := []string{
		"test", "-covermode=atomic", "-coverpkg=" + strings.Join(testPackages, ","), "-coverprofile=" + path,
	}
	if coverageErr := (commandRunner{}).command(ctx, root, nil, "go", append(arguments, testPackages...)...); coverageErr != nil {
		return coverageErr
	}
	if err = (testVerifier{}).excludeGeneratedCoverage(path); err != nil {
		return err
	}
	profileContent, err := os.ReadFile(path) // #nosec G304 -- temporary path created above.
	if err != nil {
		return err
	}
	if len(strings.Split(strings.TrimSpace(string(profileContent)), "\n")) == 1 {
		_, err = fmt.Fprintln(os.Stdout, "product coverage: no executable statements")
		return err
	}
	report, err := (commandRunner{}).capture(ctx, root, "go", "tool", "cover", "-func="+path)
	if err != nil {
		return err
	}
	percentage, err := (testVerifier{}).totalCoverage(report)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "product coverage %.1f%% (minimum %.1f%%)\n", percentage, minimumCoverage); err != nil {
		return err
	}
	if percentage < minimumCoverage {
		return fmt.Errorf("product coverage %.1f%% is below %.1f%%", percentage, minimumCoverage)
	}
	return nil
}

func (owner testVerifier) coverageTestPackages(packages []string) []string {
	return slices.DeleteFunc(slices.Clone(packages), func(candidate string) bool {
		return candidate == modulePath+"/internal/devacceptance" ||
			candidate == modulePath+"/internal/installedacceptance"
	})
}

func (owner testVerifier) excludeGeneratedCoverage(path string) error {
	content, err := os.ReadFile(path) // #nosec G304 -- gate-owned temporary profile.
	if err != nil {
		return fmt.Errorf("read coverage profile: %w", err)
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "mode: ") {
		return errors.New("coverage profile has no mode header")
	}
	filtered := make([]string, 0, len(lines))
	filtered = append(filtered, lines[0])
	generatedPrefix := modulePath + "/internal/spicegen/"
	for _, line := range lines[1:] {
		if line == "" || strings.Contains(line, generatedPrefix) {
			continue
		}
		filtered = append(filtered, line)
	}
	// #nosec G304,G703 -- path is the gate-owned temporary profile created above.
	return os.WriteFile(path, []byte(strings.Join(filtered, "\n")+"\n"), 0o600)
}

func (owner testVerifier) totalCoverage(report string) (float64, error) {
	fields := strings.Fields(strings.TrimSpace(report))
	if len(fields) == 0 || !strings.HasSuffix(fields[len(fields)-1], "%") {
		return 0, errors.New("coverage report has no total percentage")
	}
	return strconv.ParseFloat(strings.TrimSuffix(fields[len(fields)-1], "%"), 64)
}

func (owner testVerifier) offline(ctx context.Context, root string) error {
	packages, err := (testVerifier{}).productPackages(ctx, root)
	if err != nil {
		return err
	}
	environment := map[string]string{"GOFLAGS": "-mod=vendor"}
	if err := (commandRunner{}).command(ctx, root, environment, "go", append([]string{"test", "-count=1"}, packages...)...); err != nil {
		return err
	}
	return (commandRunner{}).command(ctx, root, environment, "go", append([]string{"build", "-trimpath"}, packages...)...)
}
