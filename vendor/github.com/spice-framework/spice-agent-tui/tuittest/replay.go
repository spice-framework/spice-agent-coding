package tuittest

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

// ReplayStep is the complete screen evidence captured after one trace event.
type ReplayStep struct {
	index  int
	kind   TraceEventKind
	screen Screen
	digest string
}

// Index returns the zero-based event index.
func (step ReplayStep) Index() int { return step.index }

// Kind returns the event tag applied before this capture.
func (step ReplayStep) Kind() TraceEventKind { return step.kind }

// Screen returns the immutable full-state screen capture.
func (step ReplayStep) Screen() Screen { return step.screen }

// Digest returns the full Screen.Digest captured after the event.
func (step ReplayStep) Digest() string { return step.digest }

// ReplayResult is returned only after two independent replays match exactly.
type ReplayResult struct {
	traceDigest string
	steps       []ReplayStep
	intents     []agenttui.Intent
}

// TraceDigest returns the digest of the canonical input trace.
func (result ReplayResult) TraceDigest() string { return result.traceDigest }

// Steps returns a defensive copy of all per-event screen evidence.
func (result ReplayResult) Steps() []ReplayStep { return append([]ReplayStep(nil), result.steps...) }

// Intents returns the performed UI-neutral intents in order.
func (result ReplayResult) Intents() []agenttui.Intent {
	return append([]agenttui.Intent(nil), result.intents...)
}

// Screen returns a named snapshot checkpoint.
func (result ReplayResult) Screen(name string) (Screen, bool) {
	for _, step := range result.steps {
		if step.kind == TraceEventSnapshot && step.screen.Name() == name {
			return step.screen, true
		}
	}
	return Screen{}, false
}

// Replay executes the canonical trace twice through fresh real presentation
// models. It returns evidence only if every full screen and final intent is
// byte-identical across the two runs.
func (trace Trace) Replay() (ReplayResult, error) {
	if err := trace.validate(); err != nil {
		return ReplayResult{}, err
	}
	first, err := trace.replayOnce()
	if err != nil {
		return ReplayResult{}, fmt.Errorf("replay run 1: %w", err)
	}
	second, err := trace.replayOnce()
	if err != nil {
		return ReplayResult{}, fmt.Errorf("replay run 2: %w", err)
	}
	if err := compareReplay(first, second); err != nil {
		return ReplayResult{}, fmt.Errorf("nondeterministic trace: %w", err)
	}
	return first, nil
}

func (trace Trace) replayOnce() (ReplayResult, error) {
	session := NewScriptSession()
	theme := agenttui.DarkTheme()
	if trace.options.ThemeMode == agenttui.ThemeLight {
		theme = agenttui.LightTheme()
	}
	driver, err := NewDriver(Options{
		Width: trace.options.Width, Height: trace.options.Height,
		Accessible: trace.options.Accessible, Theme: theme, Session: session,
	})
	if err != nil {
		return ReplayResult{}, err
	}
	defer driver.Close()

	result := ReplayResult{traceDigest: trace.Digest(), steps: make([]ReplayStep, 0, len(trace.events))}
	reference := replayReference{
		width: trace.options.Width, height: trace.options.Height,
		accessible: trace.options.Accessible,
	}
	for index, event := range trace.events {
		name := fmt.Sprintf("step-%03d-%s", index, event.kind)
		screen, captured, err := applyReplayEvent(driver, session, event, &reference)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("event %d (%s): %w", index, event.kind, err)
		}
		if event.kind == TraceEventSnapshot {
			name = event.name
		}
		if !captured {
			screen, err = driver.Screen(name)
			if err != nil {
				return ReplayResult{}, fmt.Errorf("capture event %d (%s): %w", index, event.kind, err)
			}
		}
		if err := reference.validate(screen, driver); err != nil {
			return ReplayResult{}, fmt.Errorf("reference invariant after event %d (%s): %w", index, event.kind, err)
		}
		result.steps = append(result.steps, ReplayStep{
			index: index, kind: event.kind, screen: screen, digest: screen.Digest(),
		})
	}
	result.intents = session.Intents()
	return result, nil
}

func applyReplayEvent(
	driver *Driver,
	session *ScriptSession,
	event TraceEvent,
	reference *replayReference,
) (Screen, bool, error) {
	switch event.kind {
	case TraceEventType:
		return Screen{}, false, driver.Type(event.text)
	case TraceEventKey:
		return Screen{}, false, applyReplayKey(driver, event)
	case TraceEventAction:
		return Screen{}, false, driver.Action(event.action)
	case TraceEventUpdate:
		return Screen{}, false, applyReplayUpdate(driver, event.update, reference)
	case TraceEventResize:
		return Screen{}, false, applyReplayResize(driver, event.width, event.height, reference)
	case TraceEventSnapshot:
		screen, err := driver.Snapshot(event.name)
		return screen, true, err
	case TraceEventPerformResult:
		return Screen{}, false, session.SetPerformResult(event.result)
	default:
		return Screen{}, false, fmt.Errorf("unsupported event kind %q", event.kind)
	}
}

func applyReplayKey(driver *Driver, event TraceEvent) error {
	if event.text == "" {
		return driver.Key(event.stroke)
	}
	return driver.Key(event.stroke, event.text)
}

func applyReplayUpdate(driver *Driver, update agenttui.SessionUpdate, reference *replayReference) error {
	if err := driver.InjectUpdate(update); err != nil {
		return err
	}
	reference.revision = update.Revision()
	return nil
}

func applyReplayResize(driver *Driver, width, height int, reference *replayReference) error {
	if err := driver.Resize(width, height); err != nil {
		return err
	}
	reference.width, reference.height = width, height
	return nil
}

type replayReference struct {
	width      int
	height     int
	accessible bool
	revision   uint64
}

func (reference replayReference) validate(screen Screen, driver *Driver) error {
	if err := reference.validateCommon(screen, driver); err != nil {
		return err
	}
	if reference.accessible {
		return reference.validateAccessible(screen)
	}
	return reference.validateNormal(screen)
}

func (reference replayReference) validateCommon(screen Screen, driver *Driver) error {
	if !utf8.ValidString(screen.Plain()) || !utf8.ValidString(screen.Styled()) {
		return errors.New("screen output is not valid UTF-8")
	}
	if screen.Revision() != reference.revision || screen.Revision() != driver.Revision() {
		return fmt.Errorf("revision = %d, want %d", screen.Revision(), reference.revision)
	}
	if screen.Prompt() != driver.Prompt() {
		return fmt.Errorf("screen prompt %q differs from model prompt %q", screen.Prompt(), driver.Prompt())
	}
	if screen.StatusLevel() != string(driver.StatusLevel()) {
		return fmt.Errorf("screen status level %q differs from model %q", screen.StatusLevel(), driver.StatusLevel())
	}
	if screen.Accessible() != reference.accessible {
		return fmt.Errorf("accessible = %t, want %t", screen.Accessible(), reference.accessible)
	}
	if len(screen.Digest()) != 64 {
		return errors.New("screen digest is not a SHA-256 hex digest")
	}
	x, y, visible := screen.Cursor()
	if visible && (x < 0 || x >= screen.Width() || y < 0 || y >= screen.Height()) {
		return fmt.Errorf("visible cursor (%d,%d) is outside %dx%d screen", x, y, screen.Width(), screen.Height())
	}
	return nil
}

func (replayReference) validateAccessible(screen Screen) error {
	return screen.ValidateAccessibility()
}

func (reference replayReference) validateNormal(screen Screen) error {
	if !screen.AlternateScreen() {
		return errors.New("normal screen did not request alternate-screen mode")
	}
	if _, _, visible := screen.Cursor(); !visible {
		return errors.New("normal screen did not expose the prompt cursor")
	}
	if screen.Width() != reference.width || screen.Height() != reference.height {
		return fmt.Errorf(
			"normal screen size = %dx%d, want %dx%d",
			screen.Width(), screen.Height(), reference.width, reference.height,
		)
	}
	if len(screen.Lines()) != reference.height {
		return fmt.Errorf("normal screen line count = %d, want %d", len(screen.Lines()), reference.height)
	}
	for index, line := range screen.Lines() {
		if width := CellWidth(line); width != reference.width {
			return fmt.Errorf("normal screen line %d width = %d, want %d", index, width, reference.width)
		}
	}
	statusLabel := "[" + strings.ToUpper(screen.StatusLevel()) + "]"
	if CellWidth(statusLabel) <= screen.Width() {
		if err := screen.ValidateStatusSemantics(); err != nil {
			return err
		}
	}
	return nil
}

func compareReplay(first, second ReplayResult) error {
	if first.traceDigest != second.traceDigest {
		return errors.New("trace digests differ")
	}
	if len(first.steps) != len(second.steps) {
		return fmt.Errorf("step counts differ: %d and %d", len(first.steps), len(second.steps))
	}
	for index := range first.steps {
		left, right := first.steps[index], second.steps[index]
		if left.index != right.index || left.kind != right.kind || left.digest != right.digest ||
			!bytes.Equal(left.screen.canonicalState(), right.screen.canonicalState()) {
			return fmt.Errorf("full screen state differs at step %d", index)
		}
	}
	if !equalIntents(first.intents, second.intents) {
		return errors.New("performed intents differ")
	}
	return nil
}

func equalIntents(first, second []agenttui.Intent) bool {
	if len(first) != len(second) {
		return false
	}
	for index, left := range first {
		right := second[index]
		if left.Kind() != right.Kind() {
			return false
		}
		leftValues, rightValues := left.Values(), right.Values()
		if len(leftValues) != len(rightValues) {
			return false
		}
		for valueIndex := range leftValues {
			if leftValues[valueIndex].String() != rightValues[valueIndex].String() {
				return false
			}
		}
	}
	return true
}
