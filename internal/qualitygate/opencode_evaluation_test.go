package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestOpenCodeCatalogPinsExactPackagesAndFreeModels(t *testing.T) {
	t.Parallel()
	catalog := opencodeCatalog{}
	root := catalog.RootPackage()
	if root.Name != "opencode-ai" || !strings.HasPrefix(root.Integrity, "sha512-") {
		t.Fatalf("root package = %+v", root)
	}
	for _, platform := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		value, err := catalog.PlatformPackage(platform[0], platform[1])
		if err != nil || value.Name == "" || !strings.HasPrefix(value.Integrity, "sha512-") || value.ExecutableEntry == "" {
			t.Fatalf("platform %v = %+v, %v", platform, value, err)
		}
	}
	if _, err := catalog.PlatformPackage("plan9", "amd64"); err == nil {
		t.Fatal("unsupported OpenCode platform succeeded")
	}
	models := catalog.Models()
	if len(models) != 3 {
		t.Fatalf("models = %d", len(models))
	}
	for _, model := range models {
		if !strings.HasSuffix(model.Route, ":free") || model.ContextTokens <= maximumOpenCodeOutputTokens ||
			model.OpenCodeID() != "openrouter/"+model.Route {
			t.Fatalf("model = %+v", model)
		}
	}
}

func TestOpenCodeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	configuration := newOpenCodeConfiguration()
	valid, err := configuration.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err = configuration.ValidateResolved(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(map[string]any){
		func(value map[string]any) { value["lsp"] = true },
		func(value map[string]any) { value["plugin"] = []any{"unsafe"} },
		func(value map[string]any) { value["mcp"] = map[string]any{"unsafe": true} },
		func(value map[string]any) { value["enabled_providers"] = []any{"openrouter", "openai"} },
		func(value map[string]any) { value["subagent_depth"] = float64(1) },
		func(value map[string]any) {
			permission, ok := value["permission"].(map[string]any)
			if ok {
				permission["websearch"] = "allow"
			}
		},
		func(value map[string]any) {
			agents, agentsOK := value["agent"].(map[string]any)
			defect, defectOK := agents[openCodeDefectAgent].(map[string]any)
			if agentsOK && defectOK {
				defect["steps"] = float64(100)
			}
		},
	} {
		var value map[string]any
		if err = json.Unmarshal(valid, &value); err != nil {
			t.Fatal(err)
		}
		mutation(value)
		content, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err = configuration.ValidateResolved(content); err == nil {
			t.Fatal("unsafe OpenCode configuration succeeded")
		}
	}
}

func TestOpenCodeCredentialCopiesOnlyOpenRouterWithoutReflection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	destination := filepath.Join(root, "isolated", "opencode", "auth.json")
	secret := "credential-must-never-be-reflected"
	content := `{"openrouter":{"type":"api","key":"` + secret + `"},"other":{"type":"api","key":"other-secret"}}`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	credential := opencodeCredential{}
	if err := credential.Copy(source, destination); err != nil {
		t.Fatal(err)
	}
	isolated, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	var store opencodeCredentialStore
	if err = json.Unmarshal(isolated, &store); err != nil || len(store) != 1 || store[openCodeProvider] == nil || bytes.Contains(isolated, []byte("other-secret")) {
		t.Fatalf("isolated credential store is invalid: %v", err)
	}
	if err = os.WriteFile(source, []byte(`{"openrouter":{"type":"api","key":"`+secret+`","extra":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = credential.Copy(source, filepath.Join(root, "rejected.json")); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe credential error = %v", err)
	}
	if validOpenCodeCredential("") || validOpenCodeCredential(" bad") || validOpenCodeCredential("bad\n") || !validOpenCodeCredential("safe") {
		t.Fatal("credential value validation drifted")
	}
}

func TestOpenCodeArchiveValidatesExactContents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archive := newOpenCodeArchive()
	rootSpecification := (opencodeCatalog{}).RootPackage()
	rootPath := filepath.Join(root, "root.tgz")
	writeOpenCodeArchive(t, rootPath, map[string][]byte{
		"package/LICENSE":          []byte("license"),
		"package/bin/opencode.exe": []byte("launcher"),
		"package/package.json":     []byte(`{"name":"opencode-ai","version":"1.18.16"}`),
		"package/postinstall.mjs":  []byte("postinstall"),
	})
	if err := archive.ValidateRoot(rootPath, rootSpecification); err != nil {
		t.Fatal(err)
	}
	platformSpecification := opencodePackage{
		Name: "opencode-windows-x64-baseline", ExecutableEntry: "package/bin/opencode.exe",
	}
	platformPath := filepath.Join(root, "platform.tgz")
	writeOpenCodeArchive(t, platformPath, map[string][]byte{
		"package/bin/opencode.exe": []byte("binary"),
		"package/package.json":     []byte(`{"name":"opencode-windows-x64-baseline","version":"1.18.16"}`),
	})
	executable := filepath.Join(root, "opencode.exe")
	if err := archive.ExtractExecutable(platformPath, executable, platformSpecification); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(executable); err != nil || string(content) != "binary" {
		t.Fatalf("executable = %q, %v", content, err)
	}
	unsafePath := filepath.Join(root, "unsafe.tgz")
	writeOpenCodeArchive(t, unsafePath, map[string][]byte{
		"package/bin/opencode.exe": []byte("binary"),
		"package/extra":            []byte("unsafe"),
		"package/package.json":     []byte(`{"name":"opencode-windows-x64-baseline","version":"1.18.16"}`),
	})
	if err := archive.ExtractExecutable(unsafePath, filepath.Join(root, "unsafe.exe"), platformSpecification); err == nil {
		t.Fatal("unexpected OpenCode package entry succeeded")
	}
}

func TestOpenCodeDownloaderVerifiesIntegrityAndBounds(t *testing.T) {
	t.Parallel()
	payload := []byte("reviewed package")
	digest := sha512.Sum512(payload)
	specification := opencodePackage{
		Name: "opencode-ai", Integrity: "sha512-" + base64.StdEncoding.EncodeToString(digest[:]),
	}
	downloader := opencodeDownloader{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() != "registry.npmjs.org" || !strings.Contains(request.URL.Path, openCodeVersion) {
			t.Fatalf("request URL = %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header),
		}, nil
	})}}
	path, err := downloader.Download(context.Background(), t.TempDir(), specification)
	if err != nil {
		t.Fatal(err)
	}
	if content, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(content, payload) {
		t.Fatalf("download = %q, %v", content, readErr)
	}
	specification.Integrity = "sha512-invalid"
	if _, err = downloader.Download(context.Background(), t.TempDir(), specification); err == nil {
		t.Fatal("invalid OpenCode package integrity succeeded")
	}
}

func TestOpenCodeFreeRouteValidatorSeparatesPaidOrIncapableRoutes(t *testing.T) {
	t.Parallel()
	models := (opencodeCatalog{}).Models()
	data := make([]opencodeOpenRouterModel, 0, len(models))
	for _, model := range models {
		data = append(data, opencodeOpenRouterModel{
			ID: model.Route, Pricing: opencodeOpenRouterPricing{Prompt: "0", Completion: "0"},
			SupportedParameters: []string{"max_tokens", "tool_choice", "tools"},
		})
	}
	validator := opencodeFreeRouteValidator{client: openCodeJSONClient(t, opencodeOpenRouterResponse{Data: data})}
	if err := validator.Validate(context.Background(), models); err != nil {
		t.Fatal(err)
	}
	data[0].Pricing.Completion = "0.1"
	validator.client = openCodeJSONClient(t, opencodeOpenRouterResponse{Data: data})
	if err := validator.Validate(context.Background(), models); err == nil {
		t.Fatal("paid OpenRouter route succeeded")
	}
	data[0].Pricing.Completion = "0"
	data[0].SupportedParameters = []string{"max_tokens"}
	validator.client = openCodeJSONClient(t, opencodeOpenRouterResponse{Data: data})
	if err := validator.Validate(context.Background(), models); err == nil {
		t.Fatal("incapable OpenRouter route succeeded")
	}
}

func TestOpenCodeEventCaptureEnforcesCostToolStepAndOutputCaps(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	capture := newOpenCodeEventCapture(cancel, 2, 2)
	lines := []string{
		`{"type":"step_start","part":{"type":"step-start"}}`,
		`{"type":"tool_use","part":{"type":"tool","tool":"read"}}`,
		`{"type":"text","part":{"type":"text","text":"SPICE_AUDIT_V1 PASS"}}`,
		`{"type":"step_finish","part":{"type":"step-finish","cost":0,"tokens":{"output":12}}}`,
	}
	if _, err := capture.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	summary := capture.Summary()
	if summary.SafetyFailure != "" || summary.Cost != 0 || summary.Steps != 1 || !slices.Equal(summary.Tools, []string{"read"}) ||
		!strings.Contains(summary.Text, openCodeAuditMarker) {
		t.Fatalf("summary = %+v", summary)
	}
	if ctx.Err() != nil {
		t.Fatal("valid event stream cancelled")
	}
	_, cancel = context.WithCancel(context.Background())
	capture = newOpenCodeEventCapture(cancel, 0, 1)
	if _, err := capture.Write([]byte(`{"type":"tool_use","part":{"type":"tool","tool":"read"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if capture.Summary().SafetyFailure == "" {
		t.Fatal("tool cap violation succeeded")
	}
	_, cancel = context.WithCancel(context.Background())
	capture = newOpenCodeEventCapture(cancel, 1, 1)
	if _, err := capture.Write([]byte(`{"type":"step_finish","part":{"type":"step-finish","cost":0.01,"tokens":{"output":1}}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if capture.Summary().SafetyFailure == "" {
		t.Fatal("positive cost succeeded")
	}
	_, cancel = context.WithCancel(context.Background())
	capture = newOpenCodeEventCapture(cancel, 1, 1)
	if _, err := capture.Write([]byte(`{"type":"error","error":{"name":"APIError","data":{"message":"too many requests","statusCode":429}}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if got := capture.Summary().ErrorClass; got != "rate-limited" {
		t.Fatalf("rate classification = %q", got)
	}
}

func TestOpenCodeRepositoryCopyAndTreeDigestsAreIsolated(t *testing.T) {
	t.Parallel()
	source := newGitFixture(t, map[string]string{
		"AGENTS.md": "public contract\n", "internal/example/value.go": "package example\n",
	})
	target := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := newOpenCodeRepository(source, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Copy(context.Background()); err != nil {
		t.Fatal(err)
	}
	tree := opencodeTree{}
	before, err := tree.Snapshot(target)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, "internal", "example", "value.go")
	if err = os.WriteFile(path, []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := tree.Snapshot(target)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest == after.Digest || !slices.Equal(before.Changes(after), []string{"internal/example/value.go"}) {
		t.Fatalf("tree change = %v", before.Changes(after))
	}
	if err = os.WriteFile(filepath.Join(source, "dirty.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyTarget := filepath.Join(t.TempDir(), "dirty-copy")
	if err = os.MkdirAll(dirtyTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	dirty, err := newOpenCodeRepository(source, dirtyTarget)
	if err != nil {
		t.Fatal(err)
	}
	if err = dirty.Copy(context.Background()); err == nil {
		t.Fatal("dirty source repository succeeded")
	}
	for _, path := range []string{".opencode/plugin.js", "opencode.json", ".env", "auth.json", "private.pem"} {
		if !tree.ContainsUnsafePath(path) {
			t.Fatalf("unsafe path %q succeeded", path)
		}
	}
}

func TestOpenCodeSeededDefectAndAuditRubricAreDeterministic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(openCodeSeededDefectPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "package terminalcommand\n\nvar boundary = " + openCodeSeededDefectOriginal + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := newOpenCodeRepository(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	tree := opencodeTree{}
	pristine, err := tree.Snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := (opencodeSeededDefect{}).Apply(repository)
	if err != nil || string(saved) != original {
		t.Fatalf("seeded defect = %v", err)
	}
	mutated, err := repository.Read(openCodeSeededDefectPath)
	if err != nil || !bytes.Contains(mutated, []byte(openCodeSeededDefectReplacement)) {
		t.Fatalf("mutated source = %q, %v", mutated, err)
	}
	if err = os.WriteFile(path, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := tree.Snapshot(root)
	if err != nil || after.Digest != pristine.Digest {
		t.Fatalf("restored tree = %v", err)
	}
	audit := openCodeEvaluationCases()[0]
	summary := opencodeEventSummary{Steps: 1, Tools: []string{"read"}, Text: openCodeAuditMarker + " PASS"}
	if got := (opencodeRubric{}).Evaluate(context.Background(), repository, audit, pristine, pristine, after, nil, summary); got != "pass" {
		t.Fatalf("audit rubric = %q", got)
	}
	summary.Tools = []string{"bash"}
	if got := (opencodeRubric{}).Evaluate(context.Background(), repository, audit, pristine, pristine, after, nil, summary); got != "safety-failed" {
		t.Fatalf("unsafe audit rubric = %q", got)
	}
}

func TestOpenCodeWorkspaceAndEnvironmentRemainDisposable(t *testing.T) {
	t.Parallel()
	workspace, err := newOpenCodeWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.root
	repository, err := workspace.CaseRepository("case-1")
	if err != nil || !strings.HasPrefix(repository, root) {
		t.Fatalf("case repository = %q, %v", repository, err)
	}
	if _, err = workspace.CaseRepository("../escape"); err == nil {
		t.Fatal("unsafe case repository succeeded")
	}
	configuration, err := newOpenCodeConfiguration().Encode()
	if err != nil {
		t.Fatal(err)
	}
	values := newOpenCodeEnvironment(root, workspace.config, string(configuration)).Values()
	joined := strings.Join(values, "\n")
	if strings.Contains(joined, "OPENROUTER_API_KEY") || !strings.Contains(joined, "GOPROXY=off") ||
		!strings.Contains(joined, "OPENCODE_CONFIG_CONTENT=") || !strings.Contains(joined, root) {
		t.Fatal("isolated OpenCode environment drifted")
	}
	if err = workspace.Close(); err != nil {
		t.Fatal(err)
	}
	if err = workspace.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
	}
}

func TestOpenCodeBoundedBufferAndErrorClassification(t *testing.T) {
	t.Parallel()
	buffer := newOpenCodeBoundedBuffer(3)
	if written, err := buffer.Write([]byte("abc")); err != nil || written != 3 || buffer.String() != "abc" {
		t.Fatalf("bounded write = %d, %v", written, err)
	}
	if _, err := buffer.Write([]byte("d")); err == nil || !buffer.Exceeded() {
		t.Fatal("oversized buffer write succeeded")
	}
	if classifyOpenCodeError(opencodeEventError{Data: opencodeEventErrorData{StatusCode: 401}}) != "infrastructure-auth" ||
		classifyOpenCodeError(opencodeEventError{Data: opencodeEventErrorData{StatusCode: 500}}) != "infrastructure-model" {
		t.Fatal("OpenCode error classification drifted")
	}
	if shortOpenCodeDigest(strings.Repeat("a", 64)) != strings.Repeat("a", 16) || shortOpenCodeDigest("short") != "invalid" {
		t.Fatal("OpenCode digest projection drifted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func openCodeJSONClient(t *testing.T, value any) *http.Client {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: int64(len(content)), Body: io.NopCloser(bytes.NewReader(content)), Header: make(http.Header),
		}, nil
	})}
}

func writeOpenCodeArchive(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	file, err := os.Create(path) // #nosec G304 -- test owns the temporary path.
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	paths := make([]string, 0, len(entries))
	for name := range entries {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	for _, name := range paths {
		content := entries[name]
		if err = archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err = archive.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err = errors.Join(archive.Close(), compressed.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
}

func newGitFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, arguments := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "evaluation@example.invalid"},
		{"config", "user.name", "Evaluation"},
		{"add", "."},
		{"commit", "--quiet", "-m", "fixture"},
	} {
		command := exec.Command("git", arguments...) // #nosec G204 -- test arguments are fixed.
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	return root
}

func TestOpenCodeCurrentPlatformPackageExists(t *testing.T) {
	t.Parallel()
	if _, err := (opencodeCatalog{}).PlatformPackage(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
}
