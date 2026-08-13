package installedacceptance

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kodecable/crosspty"
	"github.com/spice-framework/spice-agent-tui/tuittest"
)

const (
	nativeTerminalWidth               = 80
	nativeTerminalHeight              = 24
	nativeTerminalResizedWidth        = 100
	nativeTerminalResizedHeight       = 30
	nativeTerminalMaximumTranscript   = 256 << 10
	nativeTerminalCloseTimeout        = 8 * time.Second
	nativeTerminalKillDelay           = 2 * time.Second
	nativeTerminalHelperEnvironment   = "SPICE_NATIVE_TERMINAL_HELPER"
	nativeTerminalHelperInteractive   = "interactive"
	nativeTerminalHelperBlocking      = "blocking"
	nativeTerminalHelperOverflow      = "overflow"
	nativeTerminalDiagnosticByteLimit = 4 << 10
)

type nativeTerminalConfig struct {
	binary            string
	arguments         []string
	directory         string
	environment       map[string]string
	width             int
	height            int
	maximumTranscript int
	killMode          crosspty.KillMode
}

type nativeTerminalResult struct {
	exitCode   int
	captureErr error
	closeErr   error
}

type nativeTerminal struct {
	pty      crosspty.Pty
	display  *tuittest.VirtualTerminal
	output   *nativeTerminalOutput
	done     chan struct{}
	killMode crosspty.KillMode

	cancelStop func() bool
	result     nativeTerminalResult
	resultMu   sync.Mutex
	writeMu    sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

func startNativeTerminal(ctx context.Context, config nativeTerminalConfig) (*nativeTerminal, error) {
	if ctx == nil {
		return nil, errors.New("native terminal context must not be nil")
	}
	if err := validateNativeTerminalConfig(config); err != nil {
		return nil, err
	}
	display, err := tuittest.NewVirtualTerminal(tuittest.VirtualTerminalOptions{
		Width:              config.width,
		Height:             config.height,
		MaxTranscriptBytes: config.maximumTranscript,
	})
	if err != nil {
		return nil, fmt.Errorf("construct bounded virtual terminal: %w", err)
	}
	pty, err := crosspty.Start(crosspty.CommandConfig{
		Argv:        append([]string{config.binary}, config.arguments...),
		Dir:         config.directory,
		Env:         nativeTerminalEnvironment(config.environment),
		EnvFallback: map[string]string{},
		EnvInject:   map[string]string{},
		Size: crosspty.TermSize{
			Rows: uint16(config.height),
			Cols: uint16(config.width),
		},
		CloseConfig: crosspty.CloseConfig{
			CloseTimeout: nativeTerminalCloseTimeout,
			KillDelay:    nativeTerminalKillDelay,
			KillMode:     config.killMode,
		},
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start native PTY process: %w", err), display.Close())
	}
	terminal := &nativeTerminal{
		pty: pty, display: display, done: make(chan struct{}), killMode: config.killMode,
	}
	terminal.output = &nativeTerminalOutput{
		display: display,
		maximum: config.maximumTranscript,
	}
	terminal.watchCancellation(ctx)
	go terminal.capture()
	return terminal, nil
}

func (terminal *nativeTerminal) watchCancellation(ctx context.Context) {
	terminal.cancelStop = context.AfterFunc(ctx, func() {
		if terminal.closePTY() != nil {
			return
		}
	})
}

func (terminal *nativeTerminal) closePTY() error {
	terminal.closeOnce.Do(func() { terminal.closeErr = terminal.pty.Close() })
	return terminal.closeErr
}

func validateNativeTerminalConfig(config nativeTerminalConfig) error {
	if config.binary == "" || !filepath.IsAbs(config.binary) || filepath.Clean(config.binary) != config.binary {
		return errors.New("native terminal binary must be canonical and absolute")
	}
	if config.directory == "" || !filepath.IsAbs(config.directory) || filepath.Clean(config.directory) != config.directory {
		return errors.New("native terminal directory must be canonical and absolute")
	}
	if config.width < 1 || config.width > 512 || config.height < 1 || config.height > 256 {
		return errors.New("native terminal dimensions are outside the virtual-terminal bounds")
	}
	if config.maximumTranscript < 1 || config.maximumTranscript > 16<<20 {
		return errors.New("native terminal transcript limit is outside the virtual-terminal bounds")
	}
	for key, value := range config.environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return errors.New("native terminal environment contains an invalid entry")
		}
	}
	return nil
}

func nativeTerminalEnvironment(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

func (terminal *nativeTerminal) capture() {
	_, captureErr := io.Copy(terminal.output, terminal.pty)
	if flushErr := terminal.output.flush(); flushErr != nil {
		captureErr = errors.Join(captureErr, flushErr)
	}
	var closeErr error
	if captureErr != nil {
		closeErr = terminal.closePTY()
	}
	exitCode := terminal.pty.Wait()
	if closeErr == nil {
		closeErr = terminal.closePTY()
	}
	if displayErr := terminal.display.Close(); displayErr != nil {
		closeErr = errors.Join(closeErr, displayErr)
	}
	terminal.cancelStop()
	terminal.resultMu.Lock()
	terminal.result = nativeTerminalResult{
		exitCode: exitCode, captureErr: captureErr, closeErr: closeErr,
	}
	terminal.resultMu.Unlock()
	close(terminal.done)
}

func (terminal *nativeTerminal) write(value []byte) error {
	terminal.writeMu.Lock()
	defer terminal.writeMu.Unlock()
	written, err := terminal.pty.Write(value)
	if err != nil {
		return fmt.Errorf("write native terminal input: %w", err)
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func (terminal *nativeTerminal) resize(width, height int) error {
	if width < 1 || width > 512 || height < 1 || height > 256 {
		return errors.New("native terminal resize is outside the virtual-terminal bounds")
	}
	if err := terminal.display.Resize(width, height); err != nil {
		return fmt.Errorf("resize virtual terminal: %w", err)
	}
	if err := terminal.pty.Resize(crosspty.TermSize{Rows: uint16(height), Cols: uint16(width)}); err != nil {
		return fmt.Errorf("resize native PTY: %w", err)
	}
	return nil
}

func (terminal *nativeTerminal) waitFor(
	ctx context.Context,
	name string,
	predicate func(tuittest.Screen) bool,
) (tuittest.Screen, error) {
	return terminal.display.WaitFor(ctx, name, predicate)
}

func (terminal *nativeTerminal) wait(ctx context.Context) (nativeTerminalResult, error) {
	if ctx == nil {
		return nativeTerminalResult{}, errors.New("native terminal wait context must not be nil")
	}
	select {
	case <-terminal.done:
		terminal.resultMu.Lock()
		defer terminal.resultMu.Unlock()
		return terminal.result, nil
	case <-ctx.Done():
		return nativeTerminalResult{}, fmt.Errorf("wait for native terminal: %w", context.Cause(ctx))
	}
}

func (terminal *nativeTerminal) close(ctx context.Context) (nativeTerminalResult, error) {
	closeErr := terminal.closePTY()
	result, waitErr := terminal.wait(ctx)
	result.closeErr = errors.Join(result.closeErr, closeErr)
	return result, errors.Join(waitErr, result.closeErr)
}

func (terminal *nativeTerminal) transcriptDiagnostic() string {
	transcript := terminal.transcript()
	if len(transcript) > nativeTerminalDiagnosticByteLimit {
		transcript = transcript[len(transcript)-nativeTerminalDiagnosticByteLimit:]
	}
	return string(transcript)
}

func (terminal *nativeTerminal) transcript() []byte {
	return terminal.output.transcript()
}

type nativeTerminalOutput struct {
	mu sync.Mutex

	display *tuittest.VirtualTerminal
	maximum int
	raw     bytes.Buffer
	pending []byte
}

var terminalDeviceQueries = [][]byte{
	[]byte("\x1b[?2026$p"),
	[]byte("\x1b[?2027$p"),
}

func (output *nativeTerminalOutput) Write(content []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if len(content) > output.maximum-output.raw.Len() {
		return 0, errors.New("native terminal raw transcript limit exceeded")
	}
	_, _ = output.raw.Write(content)
	filtered := output.filter(content)
	if len(filtered) > 0 {
		if _, err := output.display.Write(filtered); err != nil {
			return 0, err
		}
	}
	return len(content), nil
}

func (output *nativeTerminalOutput) filter(content []byte) []byte {
	combined := append(append([]byte(nil), output.pending...), content...)
	output.pending = nil
	for _, query := range terminalDeviceQueries {
		combined = bytes.ReplaceAll(combined, query, nil)
	}
	maximumPending := 0
	for _, query := range terminalDeviceQueries {
		for length := 1; length < len(query) && length <= len(combined); length++ {
			if bytes.Equal(combined[len(combined)-length:], query[:length]) {
				maximumPending = max(maximumPending, length)
			}
		}
	}
	if maximumPending > 0 {
		output.pending = append([]byte(nil), combined[len(combined)-maximumPending:]...)
		combined = combined[:len(combined)-maximumPending]
	}
	return combined
}

func (output *nativeTerminalOutput) flush() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if len(output.pending) == 0 {
		return nil
	}
	pending := output.pending
	output.pending = nil
	_, err := output.display.Write(pending)
	return err
}

func (output *nativeTerminalOutput) transcript() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return bytes.Clone(output.raw.Bytes())
}

func TestNativeTerminalHarnessInterpretsInputResizeAndExit(t *testing.T) {
	if os.Getenv(nativeTerminalHelperEnvironment) != "" {
		runNativeTerminalHelper(t)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	contextWithTimeout, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	terminal, err := startNativeTerminal(contextWithTimeout, nativeTerminalConfig{
		binary: executable,
		arguments: []string{
			"-test.run=^TestNativeTerminalHarnessInterpretsInputResizeAndExit$",
		},
		directory: filepath.Dir(executable),
		environment: map[string]string{
			nativeTerminalHelperEnvironment: nativeTerminalHelperInteractive,
			"TERM":                          "xterm-256color",
		},
		width: nativeTerminalWidth, height: nativeTerminalHeight,
		maximumTranscript: 32 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, closeErr := terminal.close(cleanupContext); closeErr != nil {
			t.Errorf("clean up native terminal: %v", closeErr)
		}
	})
	screen, err := terminal.waitFor(contextWithTimeout, "initial", func(screen tuittest.Screen) bool {
		return screen.AlternateScreen() && screen.Contains("PTY π界")
	})
	if err != nil {
		t.Fatalf("capture initial native screen: %v\n%s", err, terminal.transcriptDiagnostic())
	}
	if screen.Width() != nativeTerminalWidth || screen.Height() != nativeTerminalHeight {
		t.Fatalf("initial screen size = %dx%d", screen.Width(), screen.Height())
	}
	if _, _, visible := screen.Cursor(); !visible {
		t.Fatal("initial native terminal cursor is hidden")
	}
	if err = terminal.resize(nativeTerminalResizedWidth, nativeTerminalResizedHeight); err != nil {
		t.Fatal(err)
	}
	if err = terminal.write([]byte("input-π界\r\n")); err != nil {
		t.Fatal(err)
	}
	screen, err = terminal.waitFor(contextWithTimeout, "resized", func(screen tuittest.Screen) bool {
		return screen.Width() == nativeTerminalResizedWidth &&
			screen.Height() == nativeTerminalResizedHeight && screen.Contains("received input-π界")
	})
	if err != nil {
		t.Fatalf("capture resized native screen: %v\n%s", err, terminal.transcriptDiagnostic())
	}
	if screen.Width() != nativeTerminalResizedWidth || screen.Height() != nativeTerminalResizedHeight {
		t.Fatalf("resized screen size = %dx%d", screen.Width(), screen.Height())
	}
	result, err := terminal.wait(contextWithTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if result.exitCode != 0 || result.captureErr != nil || result.closeErr != nil {
		t.Fatalf("native helper result = %+v\n%s", result, terminal.transcriptDiagnostic())
	}
}

func TestNativeTerminalHarnessBoundsCancellationAndFailure(t *testing.T) {
	if os.Getenv(nativeTerminalHelperEnvironment) != "" {
		runNativeTerminalHelper(t)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(executable)
	base := nativeTerminalConfig{
		binary: executable,
		arguments: []string{
			"-test.run=^TestNativeTerminalHarnessBoundsCancellationAndFailure$",
		},
		directory: directory,
		environment: map[string]string{
			nativeTerminalHelperEnvironment: nativeTerminalHelperBlocking,
		},
		width: nativeTerminalWidth, height: nativeTerminalHeight,
		maximumTranscript: 16 << 10,
	}

	t.Run("parent cancellation closes process group", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		terminal, startErr := startNativeTerminal(ctx, base)
		if startErr != nil {
			t.Fatal(startErr)
		}
		_, waitErr := terminal.waitFor(ctx, "blocking", func(screen tuittest.Screen) bool {
			return screen.Contains("blocking")
		})
		if waitErr != nil {
			t.Fatal(waitErr)
		}
		cancel(errors.New("test cancellation"))
		waitContext, waitCancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer waitCancel()
		result, waitErr := terminal.wait(waitContext)
		if waitErr != nil {
			t.Fatal(waitErr)
		}
		if result.exitCode == 0 || result.closeErr != nil {
			t.Fatalf("cancelled helper result = %+v", result)
		}
	})

	t.Run("transcript overflow fails closed", func(t *testing.T) {
		config := base
		config.environment = map[string]string{
			nativeTerminalHelperEnvironment: nativeTerminalHelperOverflow,
		}
		config.maximumTranscript = 64
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		terminal, startErr := startNativeTerminal(ctx, config)
		if startErr != nil {
			t.Fatal(startErr)
		}
		result, waitErr := terminal.wait(ctx)
		if waitErr != nil {
			t.Fatal(waitErr)
		}
		if result.captureErr == nil || !strings.Contains(result.captureErr.Error(), "transcript limit") {
			t.Fatalf("overflow capture error = %v", result.captureErr)
		}
		if result.closeErr != nil {
			t.Fatalf("overflow native close error = %v", result.closeErr)
		}
		if len(terminal.transcript()) > config.maximumTranscript {
			t.Fatal("overflow retained bytes beyond its configured limit")
		}
	})

	t.Run("direct close is bounded and idempotent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		terminal, startErr := startNativeTerminal(ctx, base)
		if startErr != nil {
			t.Fatal(startErr)
		}
		result, closeErr := terminal.close(ctx)
		if closeErr != nil || result.exitCode == 0 {
			t.Fatalf("direct close result = %+v, error = %v", result, closeErr)
		}
		if _, closeErr = terminal.close(ctx); closeErr != nil {
			t.Fatalf("second close: %v", closeErr)
		}
	})

	t.Run("start and configuration failures are explicit", func(t *testing.T) {
		var absentContext context.Context
		if _, startErr := startNativeTerminal(absentContext, base); startErr == nil ||
			!strings.Contains(startErr.Error(), "context") {
			t.Fatalf("startNativeTerminal(nil) error = %v", startErr)
		}
		tests := []struct {
			name   string
			mutate func(*nativeTerminalConfig)
			want   string
		}{
			{name: "relative binary", mutate: func(config *nativeTerminalConfig) {
				config.binary = "relative"
			}, want: "canonical and absolute"},
			{name: "bad directory", mutate: func(config *nativeTerminalConfig) {
				config.directory = "relative"
			}, want: "directory"},
			{name: "zero width", mutate: func(config *nativeTerminalConfig) {
				config.width = 0
			}, want: "dimensions"},
			{name: "zero transcript", mutate: func(config *nativeTerminalConfig) {
				config.maximumTranscript = 0
			}, want: "transcript"},
			{name: "invalid environment", mutate: func(config *nativeTerminalConfig) {
				config.environment = map[string]string{"BAD=KEY": "value"}
			}, want: "environment"},
			{name: "missing binary", mutate: func(config *nativeTerminalConfig) {
				config.binary = filepath.Join(t.TempDir(), executableName("missing"))
			}, want: "start native PTY process"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				config := base
				if test.mutate != nil {
					test.mutate(&config)
				}
				_, startErr := startNativeTerminal(t.Context(), config)
				if startErr == nil || !strings.Contains(startErr.Error(), test.want) {
					t.Fatalf("startNativeTerminal() error = %v, want %q", startErr, test.want)
				}
			})
		}
	})

	t.Run("environment ordering is deterministic", func(t *testing.T) {
		got := nativeTerminalEnvironment(map[string]string{"ZETA": "last", "ALPHA": "first", "MIDDLE": "value"})
		want := []string{"ALPHA=first", "MIDDLE=value", "ZETA=last"}
		if !slices.Equal(got, want) {
			t.Fatalf("native terminal environment = %v, want %v", got, want)
		}
	})
}

func TestNativeTerminalOutputRetainsRawBytesAndFiltersOnlyDeviceQueries(t *testing.T) {
	t.Parallel()
	display, err := tuittest.NewVirtualTerminal(tuittest.VirtualTerminalOptions{
		Width: 20, Height: 4, MaxTranscriptBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	output := &nativeTerminalOutput{display: display, maximum: 4096}
	chunks := [][]byte{
		[]byte("\x1b[?1049h\x1b[38;2;1;2;3mπ"),
		[]byte("\x1b[?20"),
		[]byte("26$p界\x1b[?2027"),
		[]byte("$p\x1b[?25h"),
		[]byte("\x1b[?2028h"),
	}
	var raw bytes.Buffer
	for _, chunk := range chunks {
		_, _ = raw.Write(chunk)
		written, writeErr := output.Write(chunk)
		if writeErr != nil || written != len(chunk) {
			t.Fatalf("write output chunk = %d, %v", written, writeErr)
		}
	}
	if err = output.flush(); err != nil {
		t.Fatal(err)
	}
	if got := output.transcript(); !bytes.Equal(got, raw.Bytes()) {
		t.Fatalf("raw native transcript = %q, want %q", got, raw.Bytes())
	}
	interpreted := display.Transcript()
	if bytes.Contains(interpreted, terminalDeviceQueries[0]) ||
		bytes.Contains(interpreted, terminalDeviceQueries[1]) ||
		!bytes.Contains(interpreted, []byte("\x1b[?2028h")) {
		t.Fatalf("interpreted terminal query filtering = %q", interpreted)
	}
	screen, err := display.Screen("filtered-queries")
	if err != nil {
		t.Fatal(err)
	}
	if !screen.AlternateScreen() || !screen.Contains("π界") {
		t.Fatalf("filtered screen lost presentation bytes:\n%s", screen.AgentReport())
	}
}

type failingNativePTY struct {
	closeErr error
	closes   atomic.Int32
}

func (*failingNativePTY) Write(content []byte) (int, error) { return len(content), nil }
func (*failingNativePTY) Read([]byte) (int, error)          { return 0, io.EOF }
func (pty *failingNativePTY) Close() error {
	pty.closes.Add(1)
	return pty.closeErr
}
func (*failingNativePTY) Wait() int                      { return -1 }
func (*failingNativePTY) Pid() int                       { return 1 }
func (*failingNativePTY) Resize(crosspty.TermSize) error { return nil }

func TestNativeTerminalCancellationRetainsAuthoritativeCloseFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("authoritative close failure")
	pty := &failingNativePTY{closeErr: want}
	terminal := &nativeTerminal{pty: pty}
	ctx, cancel := context.WithCancel(t.Context())
	terminal.watchCancellation(ctx)
	cancel()
	deadline := time.Now().Add(time.Second)
	for pty.closes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := terminal.closePTY(); !errors.Is(got, want) {
		t.Fatalf("cached native close error = %v, want %v", got, want)
	}
	if got := terminal.closePTY(); !errors.Is(got, want) || pty.closes.Load() != 1 {
		t.Fatalf("repeated native close = %v after %d calls", got, pty.closes.Load())
	}
	terminal.done = make(chan struct{})
	terminal.result = nativeTerminalResult{closeErr: want}
	close(terminal.done)
	if _, err := terminal.close(t.Context()); !errors.Is(err, want) {
		t.Fatalf("native failure caller close error = %v, want %v", err, want)
	}
}

func runNativeTerminalHelper(t *testing.T) {
	t.Helper()
	switch os.Getenv(nativeTerminalHelperEnvironment) {
	case nativeTerminalHelperInteractive:
		if _, err := fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25h\x1b[38;2;42;168;255mPTY π界\x1b[0m"); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprintf(os.Stdout, "\r\nreceived %s", strings.TrimSpace(line)); err != nil {
			t.Fatal(err)
		}
	case nativeTerminalHelperBlocking:
		if _, err := fmt.Fprint(os.Stdout, "\x1b[?1049hblocking"); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
	case nativeTerminalHelperOverflow:
		if _, err := fmt.Fprint(os.Stdout, strings.Repeat("overflow", 1024)); err != nil {
			return
		}
	default:
		t.Fatalf("unknown native terminal helper mode %q", os.Getenv(nativeTerminalHelperEnvironment))
	}
}
