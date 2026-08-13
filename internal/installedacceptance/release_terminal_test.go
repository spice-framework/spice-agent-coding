//go:build spice_release_artifacts

package installedacceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kodecable/crosspty"
	"github.com/spice-framework/spice-agent-tui/tuittest"
	"github.com/spice-framework/spice-agent/daemon/endpoint"

	"github.com/spice-framework/spice-agent-coding/internal/releaseinstallation"
)

func assertReleasedNativeTerminal(
	t *testing.T,
	daemonBinary, terminalBinary string,
	store *endpoint.Store,
	environment map[string]string,
	set *releaseinstallation.Set,
) {
	t.Helper()
	nativeEnvironment := releasedNativeTerminalEnvironment(t, environment)
	if _, found := nativeEnvironment["SPICE_AGENT_TERMINAL_ACCESSIBLE"]; found {
		t.Fatal("native release terminal must not enable the accessible pipe mode")
	}

	t.Run("native explicit attach survives daemon replacement", func(t *testing.T) {
		provider := newReleasedNativeProvider(t)
		explicitEnvironment := cloneEnvironment(nativeEnvironment)
		explicitEnvironment["OPENAI_BASE_URL"] = provider.server.URL + "/v1"
		assertEndpointAbsent(t, store, nil)
		daemonOne := startProcessWithEnvironment(
			t, daemonBinary, []string{"serve"}, nativeTerminalEnvironment(explicitEnvironment),
		)
		t.Cleanup(func() { daemonOne.stop(t, false) })
		metadataOne := waitForEndpoint(t, store, daemonOne, nil, "")
		assertReleasedServer(t, metadataOne, set)

		terminal := startReleasedNativeTerminal(
			t, terminalBinary, []string{"attach", "--endpoint", metadataOne.Address()}, nativeEnvironment,
			crosspty.KillModeKillGroupOnSubProcessExit,
		)
		initial := waitForReleasedNativeScreen(t, terminal, "native-initial", func(screen tuittest.Screen) bool {
			return screen.AlternateScreen() && screen.Contains("Spice Agent")
		})
		if initial.Width() != nativeTerminalWidth || initial.Height() != nativeTerminalHeight {
			t.Fatalf("released native initial size = %dx%d", initial.Width(), initial.Height())
		}
		if _, _, visible := initial.Cursor(); !visible {
			t.Fatal("released native terminal did not expose its prompt cursor")
		}
		if err := terminal.write([]byte("native π界 input\r")); err != nil {
			t.Fatalf("type released native Unicode input: %v", err)
		}
		provider.wait(t)
		unicodeScreen := waitForReleasedNativeScreen(t, terminal, "native-response", func(screen tuittest.Screen) bool {
			return screen.Contains("native response π界") && screen.Contains("run.completed")
		})
		if strings.Count(unicodeScreen.Plain(), "native response π界") != 1 {
			t.Fatalf("released native response was duplicated:\n%s", unicodeScreen.Plain())
		}
		provider.assert(t)
		if err := terminal.resize(nativeTerminalResizedWidth, nativeTerminalResizedHeight); err != nil {
			t.Fatalf("resize released native terminal: %v", err)
		}
		resized := waitForReleasedNativeScreen(t, terminal, "native-resized", func(screen tuittest.Screen) bool {
			return screen.Width() == nativeTerminalResizedWidth &&
				screen.Height() == nativeTerminalResizedHeight && screen.Contains("native response π界")
		})
		if !strings.Contains(resized.Styled(), "<ESC>") ||
			!strings.Contains(string(terminal.transcript()), "\x1b[") {
			t.Fatal("released native terminal emitted no interpreted ANSI control sequence")
		}

		daemonOne.stop(t, false)
		daemonTwo := startProcessWithEnvironment(
			t, daemonBinary, []string{"serve"}, nativeTerminalEnvironment(explicitEnvironment),
		)
		t.Cleanup(func() { daemonTwo.stop(t, false) })
		metadataTwo := waitForEndpoint(t, store, daemonTwo, &metadataOne, "")
		assertReleasedServer(t, metadataTwo, set)
		if metadataTwo.Process().ID() == metadataOne.Process().ID() &&
			string(metadataTwo.Process().InstanceID()) == string(metadataOne.Process().InstanceID()) {
			t.Fatal("replacement released daemon reused the prior process identity")
		}
		reconnected := waitForReleasedNativeScreen(t, terminal, "native-reconnected", func(screen tuittest.Screen) bool {
			return screen.Contains("daemon connection restored with a fresh session")
		})
		if !reconnected.Contains("native response π界") ||
			strings.Count(reconnected.Plain(), "daemon connection restored with a fresh session") != 1 {
			t.Fatalf("released native terminal lost history or duplicated reconnect state:\n%s", reconnected.Plain())
		}
		provider.assert(t)
		if err := terminal.write([]byte("\x1b[A")); err != nil {
			t.Fatalf("request released native prompt history: %v", err)
		}
		history := waitForReleasedNativeScreen(t, terminal, "native-history", func(screen tuittest.Screen) bool {
			cursorX, _, visible := screen.Cursor()
			return visible && cursorX == 2+tuittest.CellWidth("native π界 input")
		})
		if !history.Contains("native π界 input") {
			t.Fatalf("released native terminal did not retain Unicode prompt history:\n%s", history.Plain())
		}

		quitReleasedNativeTerminal(t, terminal)
		provider.assert(t)
		daemonTwo.assertRunning(t)
		daemonTwo.stop(t, true)
		assertEndpointAbsent(t, store, daemonTwo)
	})

	t.Run("native managed mode cleans owned sibling", func(t *testing.T) {
		assertEndpointAbsent(t, store, nil)
		terminal := startReleasedNativeTerminal(
			t, terminalBinary, nil, nativeEnvironment, crosspty.KillModeKillSubProcess,
		)
		if terminal.killMode != crosspty.KillModeKillSubProcess {
			t.Fatalf("managed native terminal outer kill mode = %d", terminal.killMode)
		}
		waitForReleasedNativeScreen(t, terminal, "native-managed", func(screen tuittest.Screen) bool {
			return screen.AlternateScreen() && screen.Contains("Spice Agent")
		})
		metadata := waitForNativeManagedEndpoint(t, store, terminal)
		assertReleasedServer(t, metadata, set)
		if int(metadata.Process().ID()) == terminal.pty.Pid() {
			t.Fatalf("managed released daemon reused native terminal PID %d", terminal.pty.Pid())
		}
		witness, err := openManagedProcessWitness(metadata.Process().ID())
		if err != nil {
			t.Fatalf("observe managed released daemon: %v", err)
		}
		disarmFailureCleanup := registerManagedFailureCleanup(t, witness)

		quitReleasedNativeTerminal(t, terminal)
		waitForNativeManagedProcessExit(t, witness, metadata, terminal)
		waitForNativeEndpointAbsence(t, store, terminal)
		disarmFailureCleanup()
	})

	t.Run("native provider rejects request replay", func(t *testing.T) {
		provider := newReleasedNativeProvider(t)
		post := func() *http.Response {
			request, err := http.NewRequestWithContext(
				t.Context(), http.MethodPost, provider.server.URL+"/v1/responses",
				strings.NewReader(`{"input":"native π界 input"}`),
			)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer release-archive-check-only")
			response, err := provider.server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			return response
		}
		first := post()
		if first.StatusCode != http.StatusOK {
			t.Fatalf("first native provider response status = %d", first.StatusCode)
		}
		if _, err := io.Copy(io.Discard, first.Body); err != nil {
			t.Fatal(err)
		}
		if err := first.Body.Close(); err != nil {
			t.Fatal(err)
		}
		second := post()
		if second.StatusCode != http.StatusBadRequest {
			t.Fatalf("replayed native provider response status = %d", second.StatusCode)
		}
		if _, err := io.Copy(io.Discard, second.Body); err != nil {
			t.Fatal(err)
		}
		if err := second.Body.Close(); err != nil {
			t.Fatal(err)
		}
		provider.mu.Lock()
		defer provider.mu.Unlock()
		if provider.requests != 2 || provider.responses != 1 || provider.violation != "duplicate provider request" {
			t.Fatalf(
				"replayed native provider = requests %d responses %d violation %q",
				provider.requests, provider.responses, provider.violation,
			)
		}
	})

	t.Run("native failure cleanup precedes terminal cleanup", func(t *testing.T) {
		var events []string
		t.Run("cleanup scope", func(t *testing.T) {
			t.Cleanup(func() { events = append(events, "terminal") })
			registerManagedFailureCleanup(t, &managedFailureCleanupRecorder{events: &events})
		})
		want := []string{"witness terminate", "witness close", "terminal"}
		if !slices.Equal(events, want) {
			t.Fatalf("managed failure cleanup order = %v, want %v", events, want)
		}
	})
}

type managedFailureCleanupWitness interface {
	terminateForFailureCleanup() error
	Close() error
}

// registerManagedFailureCleanup must run after startReleasedNativeTerminal.
// testing.Cleanup is LIFO, so the exact daemon witness is terminated before
// the outer direct-process PTY fallback and cannot be masked by that fallback.
func registerManagedFailureCleanup(t *testing.T, witness managedFailureCleanupWitness) func() {
	t.Helper()
	cleanupRequired := true
	t.Cleanup(func() {
		if cleanupRequired {
			if cleanupErr := witness.terminateForFailureCleanup(); cleanupErr != nil {
				t.Errorf("terminate leaked managed released daemon: %v", cleanupErr)
			}
		}
		if closeErr := witness.Close(); closeErr != nil {
			t.Errorf("close managed released daemon witness: %v", closeErr)
		}
	})
	return func() { cleanupRequired = false }
}

type managedFailureCleanupRecorder struct{ events *[]string }

func (recorder *managedFailureCleanupRecorder) terminateForFailureCleanup() error {
	*recorder.events = append(*recorder.events, "witness terminate")
	return nil
}

func (recorder *managedFailureCleanupRecorder) Close() error {
	*recorder.events = append(*recorder.events, "witness close")
	return nil
}

type releasedNativeProvider struct {
	server *httptest.Server
	seen   chan struct{}
	once   sync.Once
	mu     sync.Mutex

	requests  int
	responses int
	violation string
}

func newReleasedNativeProvider(t *testing.T) *releasedNativeProvider {
	t.Helper()
	provider := &releasedNativeProvider{seen: make(chan struct{})}
	provider.server = httptest.NewServer(http.HandlerFunc(provider.serveHTTP))
	t.Cleanup(provider.server.Close)
	return provider
}

func (provider *releasedNativeProvider) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
	provider.mu.Lock()
	provider.requests++
	if provider.requests != 1 {
		provider.violation = "duplicate provider request"
	} else if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
		provider.violation = "unexpected request target"
	} else if err != nil || !json.Valid(body) {
		provider.violation = "invalid request JSON"
	} else if request.Header.Get("Authorization") != "Bearer release-archive-check-only" {
		provider.violation = "missing fixed provider authorization"
	} else if strings.Contains(string(body), "release-archive-check-only") {
		provider.violation = "provider credential leaked into request body"
	} else if !strings.Contains(string(body), "native π界 input") {
		provider.violation = "Unicode terminal prompt was not submitted"
	}
	violation := provider.violation
	provider.mu.Unlock()
	provider.once.Do(func() { close(provider.seen) })
	if violation != "" {
		http.Error(writer, violation, http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	_, writeErr := io.WriteString(
		writer,
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"native-item\",\"output_index\":0,\"content_index\":0,\"delta\":\"native response π界\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"native-response\",\"model\":\"release-archive-model\",\"status\":\"completed\",\"service_tier\":\"default\",\"usage\":{\"input_tokens\":2,\"output_tokens\":2,\"total_tokens\":4},\"output\":[{\"type\":\"message\",\"id\":\"native-item\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"native response π界\",\"annotations\":[]}]}]}}\n\n"+
			"data: [DONE]\n\n",
	)
	if writeErr != nil {
		provider.mu.Lock()
		provider.violation = "write loopback provider response: " + writeErr.Error()
		provider.mu.Unlock()
		return
	}
	provider.mu.Lock()
	provider.responses++
	if provider.responses != 1 {
		provider.violation = "duplicate provider response"
	}
	provider.mu.Unlock()
}

func (provider *releasedNativeProvider) wait(t *testing.T) {
	t.Helper()
	select {
	case <-provider.seen:
	case <-time.After(observationTimeout):
		t.Fatal("released native provider did not receive the terminal prompt")
	}
}

func (provider *releasedNativeProvider) assert(t *testing.T) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.requests != 1 || provider.responses != 1 || provider.violation != "" {
		t.Fatalf(
			"released native provider = requests %d responses %d violation %q",
			provider.requests, provider.responses, provider.violation,
		)
	}
}

func startReleasedNativeTerminal(
	t *testing.T,
	binary string,
	arguments []string,
	environment map[string]string,
	killMode crosspty.KillMode,
) *nativeTerminal {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), acceptanceTimeout)
	t.Cleanup(cancel)
	terminal, err := startNativeTerminal(ctx, nativeTerminalConfig{
		binary: binary, arguments: arguments, directory: filepath.Dir(binary), environment: environment,
		width: nativeTerminalWidth, height: nativeTerminalHeight,
		maximumTranscript: nativeTerminalMaximumTranscript,
		killMode:          killMode,
	})
	if err != nil {
		t.Fatalf("start released native terminal: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cleanupCancel()
		if _, closeErr := terminal.close(cleanupContext); closeErr != nil {
			t.Errorf("clean up released native terminal: %v", closeErr)
		}
	})
	return terminal
}

func waitForReleasedNativeScreen(
	t *testing.T,
	terminal *nativeTerminal,
	name string,
	predicate func(tuittest.Screen) bool,
) tuittest.Screen {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), observationTimeout)
	defer cancel()
	screen, err := terminal.waitFor(ctx, name, predicate)
	if err != nil {
		latest, captureErr := terminal.display.Screen(name + "-failure")
		t.Fatalf(
			"wait for released native terminal %s: %v\nlatest capture error: %v\nlatest screen:\n%s\ntranscript tail:\n%s",
			name, err, captureErr, latest.AgentReport(), terminal.transcriptDiagnostic(),
		)
	}
	return screen
}

func quitReleasedNativeTerminal(t *testing.T, terminal *nativeTerminal) {
	t.Helper()
	if err := terminal.write([]byte{0x11}); err != nil {
		t.Fatalf("request released native terminal quit: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	result, err := terminal.wait(ctx)
	if err != nil {
		t.Fatalf("wait for released native terminal quit: %v\ntranscript tail:\n%s", err, terminal.transcriptDiagnostic())
	}
	if result.exitCode != 0 || result.captureErr != nil || result.closeErr != nil {
		t.Fatalf("released native terminal quit result = %+v\ntranscript tail:\n%s", result, terminal.transcriptDiagnostic())
	}
	exited, screenErr := terminal.display.Screen("native-exited")
	if screenErr != nil {
		t.Fatalf("capture exited released native terminal: %v", screenErr)
	}
	transcript := string(terminal.transcript())
	if exited.AlternateScreen() || !strings.Contains(transcript, "\x1b[?1049h") ||
		!strings.Contains(transcript, "\x1b[?1049l") {
		t.Fatalf("released native terminal did not enter and leave alternate screen\ntranscript tail:\n%s", terminal.transcriptDiagnostic())
	}
	if strings.Contains(transcript, acceptanceAPIKey) || strings.Contains(transcript, "release-archive-check-only") {
		t.Fatal("released native terminal transcript exposed its fixed credential canary")
	}
}

func releasedNativeTerminalEnvironment(t *testing.T, base map[string]string) map[string]string {
	t.Helper()
	result := cloneEnvironment(base)
	delete(result, "SPICE_AGENT_TERMINAL_ACCESSIBLE")
	delete(result, ephemeralRunnerEnvironment)
	result["TERM"] = "xterm-256color"
	result["COLORTERM"] = "truecolor"
	for _, key := range releasedNativeTerminalOSVariables(runtime.GOOS) {
		if value, found := os.LookupEnv(key); found && value != "" {
			result[key] = value
		}
	}
	return result
}

func releasedNativeTerminalOSVariables(goos string) []string {
	switch goos {
	case "windows":
		return []string{
			"APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "TEMP", "TMP", "USERPROFILE", "WINDIR",
		}
	case "linux", "darwin":
		return []string{"HOME"}
	default:
		return nil
	}
}

func waitForNativeManagedEndpoint(
	t *testing.T,
	store *endpoint.Store,
	owner *nativeTerminal,
) endpoint.Metadata {
	t.Helper()
	var result endpoint.Metadata
	var lastErr error
	waitFor(t, func() bool {
		result, lastErr = store.Discover(t.Context())
		return lastErr == nil
	}, func() string {
		return fmt.Sprintf(
			"native managed endpoint was not published: %v\ntranscript tail:\n%s",
			lastErr, owner.transcriptDiagnostic(),
		)
	})
	return result
}

func waitForNativeManagedProcessExit(
	t *testing.T,
	witness *managedProcessWitness,
	metadata endpoint.Metadata,
	owner *nativeTerminal,
) {
	t.Helper()
	var lastErr error
	waitFor(t, func() bool {
		var exited bool
		exited, lastErr = witness.Exited()
		return lastErr == nil && exited
	}, func() string {
		return fmt.Sprintf(
			"native managed daemon %d/%x remained alive: %v\ntranscript tail:\n%s",
			metadata.Process().ID(), metadata.Process().InstanceID(), lastErr, owner.transcriptDiagnostic(),
		)
	})
}

func waitForNativeEndpointAbsence(
	t *testing.T,
	store *endpoint.Store,
	owner *nativeTerminal,
) {
	t.Helper()
	var lastErr error
	waitFor(t, func() bool {
		_, lastErr = store.Discover(t.Context())
		return errors.Is(lastErr, endpoint.ErrNotFound)
	}, func() string {
		return fmt.Sprintf(
			"native managed endpoint remained after terminal exit: %v\ntranscript tail:\n%s",
			lastErr, owner.transcriptDiagnostic(),
		)
	})
}
