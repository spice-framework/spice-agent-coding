package devacceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	spiceToolPath   = "github.com/spice-framework/toolchain/cmd/spice"
	fixtureRevision = `return "one"`
	latestRevision  = `return "latest"`
)

type probeResponse struct {
	PID      int    `json:"pid"`
	Revision string `json:"revision"`
	Instance string `json:"instance"`
}

type probeEndpoint struct {
	PID      int    `json:"pid"`
	Address  string `json:"address"`
	Instance string `json:"instance"`
}

func TestSpiceDevPreservesLastKnownGoodAndDebouncesReplacement(t *testing.T) {
	if runtime.Version() != "go1.26.5" {
		t.Fatalf("runtime = %s, want go1.26.5", runtime.Version())
	}
	repository := repositoryRoot(t)
	scratch := t.TempDir()
	workspace := filepath.Join(scratch, "workspace")
	copyDevelopmentWorkspace(t, repository, workspace)
	spiceExecutable := buildSpiceExecutable(t, repository, scratch)
	addressFile := filepath.Join(scratch, "probe-address")

	applicationPath := filepath.Join(workspace, "cmd", "devprobe", "application.go")
	probePath := filepath.Join(workspace, "internal", "devprobe", "probe.go")
	originalApplication := readFile(t, applicationPath)
	originalProbe := readFile(t, probePath)
	originalSources := sourceHashes(t, workspace)

	output := newEventBuffer()
	command := exec.Command(
		spiceExecutable,
		"dev",
		"--target=devprobe",
		"--poll=20ms",
		"--quiet=250ms",
		"--max-delay=2s",
		"--stop-timeout=5s",
		"./cmd/devprobe",
	)
	command.Dir = workspace
	command.Env = developmentEnvironment(addressFile)
	command.Stdout = output
	command.Stderr = output
	configureDevelopmentCommand(command)
	if err := command.Start(); err != nil {
		t.Fatalf("start spice dev: %v", err)
	}
	process := newDevelopmentProcess(command, output, addressFile)
	t.Cleanup(func() { process.cleanup(t) })

	output.waitForText(t, "spice dev: application started (revision 1)")
	firstEndpoint := waitForPublishedEndpoint(t, addressFile, "")
	first := waitForProbe(t, firstEndpoint.Address, "one")
	assertEndpointOwner(t, firstEndpoint, first)
	generatedBefore := generatedHashes(t, workspace)

	brokenApplication := bytes.Replace(
		originalApplication,
		[]byte("// @Application"),
		[]byte("// @Application\n// @Unknown"),
		1,
	)
	if bytes.Equal(brokenApplication, originalApplication) {
		t.Fatal("fixture application marker was not found")
	}
	atomicReplace(t, scratch, applicationPath, brokenApplication)
	output.waitForText(t, "spice dev: revision 2 failed:")
	output.waitForText(
		t,
		"spice dev: application remains on last-known-good revision 1",
	)
	stillRunning := readProbe(t, firstEndpoint.Address)
	if stillRunning != first {
		t.Fatalf(
			"last-known-good probe = %#v, want unchanged %#v\n%s",
			stillRunning,
			first,
			output.String(),
		)
	}

	atomicReplace(t, scratch, applicationPath, originalApplication)
	output.waitForText(t, "spice dev: graceful restart requested for revision 3")
	output.waitForText(t, "spice dev: application started (revision 3)")
	thirdEndpoint := waitForPublishedEndpoint(t, addressFile, firstEndpoint.Instance)
	third := waitForProbe(t, thirdEndpoint.Address, "one")
	assertEndpointOwner(t, thirdEndpoint, third)
	if third.Instance == first.Instance {
		t.Fatalf("revision 3 retained process identity %q; expected replacement", third.Instance)
	}

	firstBurst := bytes.Replace(
		originalProbe,
		[]byte(fixtureRevision),
		[]byte(`return "intermediate"`),
		1,
	)
	finalBurst := bytes.Replace(
		originalProbe,
		[]byte(fixtureRevision),
		[]byte(latestRevision),
		1,
	)
	if bytes.Equal(firstBurst, originalProbe) || bytes.Equal(finalBurst, originalProbe) {
		t.Fatal("fixture revision body was not found")
	}
	changeCount := output.Count("spice dev: change detected: internal/devprobe/probe.go")
	analysisCount := output.Count("spice dev: analysis started")
	startCount := output.Count("spice dev: application started")
	atomicReplace(t, scratch, probePath, firstBurst)
	output.waitForCount(
		t,
		"spice dev: change detected: internal/devprobe/probe.go",
		changeCount+1,
	)
	atomicReplace(t, scratch, probePath, finalBurst)
	output.waitForCount(
		t,
		"spice dev: change detected: internal/devprobe/probe.go",
		changeCount+2,
	)
	output.waitForText(t, "spice dev: analysis started (revision 4)")
	output.waitForText(t, "spice dev: application started (revision 4)")
	fourthEndpoint := waitForPublishedEndpoint(t, addressFile, thirdEndpoint.Instance)
	fourth := waitForProbe(t, fourthEndpoint.Address, "latest")
	assertEndpointOwner(t, fourthEndpoint, fourth)
	if fourth.Instance == third.Instance {
		t.Fatalf("debounced revision retained process identity %q; expected replacement", fourth.Instance)
	}
	assertBurstPrecedesAnalysis(t, output.String(), changeCount, 4)
	output.requireStableCounts(t, 3*time.Second, map[string]int{
		"spice dev: analysis started":    analysisCount + 1,
		"spice dev: application started": startCount + 1,
	})

	atomicReplace(t, scratch, probePath, originalProbe)
	output.waitForCount(t, "spice dev: analysis started", analysisCount+2)
	output.waitForCount(t, "spice dev: application started", startCount+2)
	output.waitForText(t, "spice dev: application started (revision 5)")
	fifthEndpoint := waitForPublishedEndpoint(t, addressFile, fourthEndpoint.Instance)
	fifth := waitForProbe(t, fifthEndpoint.Address, "one")
	assertEndpointOwner(t, fifthEndpoint, fifth)
	if fifth.Instance == fourth.Instance {
		t.Fatalf("restored revision retained process identity %q; expected replacement", fifth.Instance)
	}

	process.stop(t)
	waitForProbeUnavailable(t, fifthEndpoint.Address)
	if got := sourceHashes(t, workspace); !equalHashes(got, originalSources) {
		t.Fatalf("source hashes changed:\n%s", hashDifference(originalSources, got))
	}
	if got := generatedHashes(t, workspace); !equalHashes(got, generatedBefore) {
		t.Fatalf("generated hashes changed:\n%s", hashDifference(generatedBefore, got))
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve acceptance source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func copyDevelopmentWorkspace(t *testing.T, repository, workspace string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatalf("create development workspace: %v", err)
	}
	copyFile(t, filepath.Join(repository, "go.mod"), filepath.Join(workspace, "go.mod"))
	copyFile(t, filepath.Join(repository, "go.sum"), filepath.Join(workspace, "go.sum"))
	copyTree(t, filepath.Join(repository, "vendor"), filepath.Join(workspace, "vendor"))
	copyTree(t, filepath.Join(repository, "testdata", "devloop"), workspace)
}

func copyTree(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyFileError(path, target)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", source, err)
	}
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	if err := copyFileError(source, destination); err != nil {
		t.Fatalf("copy %s: %v", source, err)
	}
}

func copyFileError(source, destination string) (resultErr error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(destination), 0o750); mkdirErr != nil {
		return mkdirErr
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

func buildSpiceExecutable(t *testing.T, repository, scratch string) string {
	t.Helper()
	executable := filepath.Join(scratch, "spice"+executableSuffix())
	command := exec.Command(
		exactGoExecutable(), "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
		"-ldflags=-buildid=", "-o", executable, spiceToolPath,
	)
	command.Dir = repository
	command.Env = developmentEnvironment("")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build vendored Spice executable: %v\n%s", err, output)
	}
	return executable
}

func executableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func exactGoExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Exact executing toolchain.
}

func developmentEnvironment(addressFile string) []string {
	overrides := map[string]string{
		"GONOSUMDB":   "*",
		"GOPROXY":     "off",
		"GOSUMDB":     "off",
		"GOTOOLCHAIN": "local",
		"GOWORK":      "off",
		"GOFLAGS":     "-mod=vendor",
	}
	if addressFile != "" {
		overrides["DEV_PROBE_ADDRESS_FILE"] = addressFile
	}
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		if !found || environmentContains(overrides, name) {
			continue
		}
		result = append(result, item)
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, name+"="+overrides[name])
	}
	return result
}

func environmentContains(values map[string]string, name string) bool {
	for candidate := range values {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func waitForPublishedEndpoint(t *testing.T, path, previousInstance string) probeEndpoint {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		content, err := os.ReadFile(path)
		var endpoint probeEndpoint
		decodeErr := json.Unmarshal(content, &endpoint)
		if err == nil && decodeErr == nil && endpoint.PID > 0 && endpoint.Instance != "" &&
			endpoint.Instance != previousInstance {
			if host, _, splitErr := net.SplitHostPort(endpoint.Address); splitErr == nil && host == "127.0.0.1" {
				return endpoint
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf(
				"probe endpoint was not published after instance %q: content %q, read error %v, decode error %v",
				previousInstance,
				content,
				err,
				decodeErr,
			)
		}
	}
}

func assertEndpointOwner(t *testing.T, endpoint probeEndpoint, response probeResponse) {
	t.Helper()
	if endpoint.PID != response.PID || endpoint.Instance != response.Instance {
		t.Fatalf("published endpoint %#v served from process %#v", endpoint, response)
	}
}

func waitForProbe(t *testing.T, address, revision string) probeResponse {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := probe(address)
		if err == nil && response.PID > 0 && response.Revision == revision {
			return response
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("probe did not reach revision %q: last response %#v, error %v", revision, response, err)
		}
	}
}

func readProbe(t *testing.T, address string) probeResponse {
	t.Helper()
	response, err := probe(address)
	if err != nil {
		t.Fatalf("read last-known-good probe: %v", err)
	}
	return response
}

func probe(address string) (result probeResponse, resultErr error) {
	client := &http.Client{
		Timeout: 500 * time.Millisecond,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	response, err := client.Get("http://" + address + "/health")
	if err != nil {
		return probeResponse{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		return probeResponse{}, fmt.Errorf("health status %s", response.Status)
	}
	var decoded probeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&decoded); err != nil {
		return probeResponse{}, err
	}
	return decoded, nil
}

func waitForProbeUnavailable(t *testing.T, address string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := probe(address); err != nil {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("probe %s remained reachable after supervisor cancellation", address)
		}
	}
}

type eventBuffer struct {
	mu      sync.Mutex
	content bytes.Buffer
	updated chan struct{}
}

func newEventBuffer() *eventBuffer {
	return &eventBuffer{updated: make(chan struct{}, 1)}
}

func (buffer *eventBuffer) Write(content []byte) (int, error) {
	buffer.mu.Lock()
	written, err := buffer.content.Write(content)
	buffer.mu.Unlock()
	select {
	case buffer.updated <- struct{}{}:
	default:
	}
	return written, err
}

func (buffer *eventBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.content.String()
}

func (buffer *eventBuffer) Count(text string) int {
	return strings.Count(buffer.String(), text)
}

func (buffer *eventBuffer) waitForText(t *testing.T, expected string) {
	t.Helper()
	buffer.waitForCount(t, expected, 1)
}

func (buffer *eventBuffer) waitForCount(t *testing.T, expected string, count int) {
	t.Helper()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		if buffer.Count(expected) >= count {
			return
		}
		select {
		case <-buffer.updated:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d occurrences of %q\n%s", count, expected, buffer.String())
		}
	}
}

func (buffer *eventBuffer) requireStableCounts(
	t *testing.T,
	duration time.Duration,
	expected map[string]int,
) {
	t.Helper()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		for text, count := range expected {
			if got := buffer.Count(text); got != count {
				t.Fatalf("count of %q = %d, want stable %d\n%s", text, got, count, buffer.String())
			}
		}
		select {
		case <-buffer.updated:
		case <-deadline.C:
			return
		}
	}
}

func assertBurstPrecedesAnalysis(t *testing.T, output string, previousChanges, revision int) {
	t.Helper()
	change := "spice dev: change detected: internal/devprobe/probe.go"
	analysis := fmt.Sprintf("spice dev: analysis started (revision %d)", revision)
	analysisIndex := strings.Index(output, analysis)
	if analysisIndex < 0 {
		t.Fatalf("missing %q", analysis)
	}
	beforeAnalysis := output[:analysisIndex]
	if got := strings.Count(beforeAnalysis, change); got != previousChanges+2 {
		t.Fatalf(
			"change events before revision %d = %d, want %d\n%s",
			revision,
			got,
			previousChanges+2,
			output,
		)
	}
}

type developmentProcess struct {
	command  *exec.Cmd
	output   *eventBuffer
	endpoint string
	done     chan error
	stopOnce sync.Once
}

func newDevelopmentProcess(
	command *exec.Cmd,
	output *eventBuffer,
	endpoint string,
) *developmentProcess {
	process := &developmentProcess{
		command:  command,
		output:   output,
		endpoint: endpoint,
		done:     make(chan error, 1),
	}
	go func() {
		process.done <- command.Wait()
		close(process.done)
	}()
	return process
}

func (process *developmentProcess) stopProcess() error {
	var interruptErr error
	process.stopOnce.Do(func() {
		interruptErr = interruptDevelopmentCommand(process.command)
	})
	if interruptErr != nil {
		return interruptErr
	}
	select {
	case err := <-process.done:
		return err
	case <-time.After(30 * time.Second):
		killErr := killDevelopmentSupervisor(process.command)
		if childPID := process.confirmedChildPID(); childPID > 0 {
			killErr = errors.Join(killErr, killDevelopmentChild(childPID))
		}
		select {
		case <-process.done:
		case <-time.After(10 * time.Second):
			killErr = errors.Join(killErr, errors.New("timed out reaping the terminated development process tree"))
		}
		return errors.Join(
			errors.New("timed out waiting for Spice development supervisor shutdown"),
			killErr,
		)
	}
}

func (process *developmentProcess) confirmedChildPID() int {
	content, err := os.ReadFile(process.endpoint)
	if err != nil {
		return 0
	}
	var endpoint probeEndpoint
	if err = json.Unmarshal(content, &endpoint); err != nil || endpoint.PID <= 0 || endpoint.Instance == "" {
		return 0
	}
	response, err := probe(endpoint.Address)
	if err != nil || response.PID != endpoint.PID || response.Instance != endpoint.Instance {
		return 0
	}
	return endpoint.PID
}

func (process *developmentProcess) stop(t *testing.T) {
	t.Helper()
	if err := process.stopProcess(); err != nil {
		t.Fatalf("stop spice dev: %v\n%s", err, process.output.String())
	}
}

func (process *developmentProcess) cleanup(t *testing.T) {
	t.Helper()
	if process.command.ProcessState != nil {
		return
	}
	if err := process.stopProcess(); err != nil {
		t.Errorf("cleanup spice dev: %v\n%s", err, process.output.String())
	}
}

func atomicReplace(t *testing.T, scratch, destination string, content []byte) {
	t.Helper()
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("inspect %s: %v", destination, err)
	}
	temporary, err := os.CreateTemp(scratch, "spice-dev-edit-*")
	if err != nil {
		t.Fatalf("create atomic edit: %v", err)
	}
	temporaryPath := temporary.Name()
	t.Cleanup(func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			t.Errorf("remove temporary edit %s: %v", temporaryPath, removeErr)
		}
	})
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		t.Fatalf("set atomic edit permissions: %v", errors.Join(err, temporary.Close()))
	}
	if _, err := temporary.Write(content); err != nil {
		t.Fatalf("write atomic edit: %v", errors.Join(err, temporary.Close()))
	}
	if err := temporary.Sync(); err != nil {
		t.Fatalf("sync atomic edit: %v", errors.Join(err, temporary.Close()))
	}
	if err := temporary.Close(); err != nil {
		t.Fatalf("close atomic edit: %v", err)
	}
	if err := replaceFile(temporaryPath, destination); err != nil {
		t.Fatalf("replace %s atomically: %v", destination, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func sourceHashes(t *testing.T, workspace string) map[string]string {
	t.Helper()
	result := prefixedTreeHashes(t, filepath.Join(workspace, "cmd", "devprobe"), "cmd/devprobe")
	maps.Copy(result, prefixedTreeHashes(
		t,
		filepath.Join(workspace, "internal", "devprobe"),
		"internal/devprobe",
	))
	return result
}

func prefixedTreeHashes(t *testing.T, root, prefix string) map[string]string {
	t.Helper()
	result := treeHashes(t, root)
	prefixed := make(map[string]string, len(result))
	for path, digest := range result {
		prefixed[filepath.ToSlash(filepath.Join(prefix, path))] = digest
	}
	return prefixed
}

func generatedHashes(t *testing.T, workspace string) map[string]string {
	t.Helper()
	result := treeHashes(t, filepath.Join(workspace, "internal", "spicegen", "devprobe"))
	manifest := filepath.Join(workspace, ".spice", "devprobe.manifest.json")
	digest := sha256.Sum256(readFile(t, manifest))
	result[".spice/devprobe.manifest.json"] = hex.EncodeToString(digest[:])
	return result
}

func treeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hash %s: %v", root, err)
	}
	return result
}

func equalHashes(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for path, digest := range left {
		if right[path] != digest {
			return false
		}
	}
	return true
}

func hashDifference(want, got map[string]string) string {
	paths := make([]string, 0, len(want)+len(got))
	for path := range want {
		paths = append(paths, path)
	}
	for path := range got {
		if _, found := want[path]; !found {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var result strings.Builder
	for _, path := range paths {
		if want[path] == got[path] {
			continue
		}
		fmt.Fprintf(&result, "%s: want %s, got %s\n", path, want[path], got[path])
	}
	return result.String()
}
