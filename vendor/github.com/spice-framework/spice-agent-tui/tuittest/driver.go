package tuittest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent-tui/internal/presentation"
	"github.com/spice-framework/spice-agent-tui/terminal"
)

// Options configures a deterministic interactive Driver.
type Options struct {
	Width      int
	Height     int
	Accessible bool
	Theme      agenttui.Theme
	Renderer   agenttui.Renderer
	Bindings   []agenttui.KeyBinding
	// Initial is optional. When nil, DefaultConnectingView("Spice Agent") is used.
	Initial *agenttui.ViewData
	Session agenttui.Session
	// DrainTimeout bounds one perform command. Zero selects 250ms. A timeout is
	// returned to the caller; it is never mistaken for a successful operation.
	DrainTimeout time.Duration
	// MaxDrainSteps bounds how many command-produced messages are applied.
	MaxDrainSteps int
}

// Driver owns one presentation model and captures pixel-perfect screens.
type Driver struct {
	model        presentation.Model
	drainTimeout time.Duration
	maxSteps     int
	actionKeys   map[agenttui.Action]string
	snapshots    []Screen
	lastQuit     bool
	cancel       context.CancelFunc
	closed       bool
}

// ErrDriverClosed reports an attempted mutation after Close or quit.
var ErrDriverClosed = errors.New("tui test driver is closed")

// NewDriver constructs a deterministic interactive TUI driver.
func NewDriver(options Options) (*Driver, error) {
	var err error
	options, err = normalizedOptions(options)
	if err != nil {
		return nil, err
	}
	initial, err := initialView(options.Initial)
	if err != nil {
		return nil, err
	}
	var effects presentation.Effects
	if options.Session != nil {
		effects, err = presentation.NewSessionEffects(options.Session)
		if err != nil {
			return nil, err
		}
	}

	themeSnapshot, err := agenttui.NewTheme(options.Theme.Name(), options.Theme.Mode(), options.Theme.Palette())
	if err != nil {
		return nil, fmt.Errorf("theme: %w", err)
	}
	model, err := presentation.NewModel(
		options.Renderer,
		initial.Workspace(),
		initial.Status(),
		initial.Prompt(),
		initial.Activity(),
		themeSnapshot,
		options.Bindings,
		effects,
	)
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	model = model.WithAccessibleMode(options.Accessible)

	// Bind async command factories before resize so submit and cancellation
	// lanes are available for agent-driven interaction.
	ctx, cancel := context.WithCancel(context.Background())
	model = presentation.TestWithEffectsContext(model, ctx, cancel)

	driver := &Driver{
		model:        model,
		drainTimeout: options.DrainTimeout,
		maxSteps:     options.MaxDrainSteps,
		actionKeys:   firstStrokes(options.Bindings),
		cancel:       cancel,
	}
	if err := driver.Resize(options.Width, options.Height); err != nil {
		driver.Close()
		return nil, err
	}
	return driver, nil
}

func normalizedOptions(options Options) (Options, error) {
	if options.Width <= 0 {
		options.Width = 48
	}
	if options.Height <= 0 {
		options.Height = 12
	}
	if options.Theme == nil {
		options.Theme = agenttui.DarkTheme()
	}
	if options.Renderer == nil {
		options.Renderer = terminal.NewFixedRenderer()
	}
	if len(options.Bindings) == 0 {
		bindings, err := agenttui.StandardKeyBindings()
		if err != nil {
			return Options{}, err
		}
		options.Bindings = bindings
	}
	if options.DrainTimeout <= 0 {
		options.DrainTimeout = 250 * time.Millisecond
	}
	if options.MaxDrainSteps <= 0 {
		options.MaxDrainSteps = 32
	}
	return options, nil
}

func initialView(configured *agenttui.ViewData) (agenttui.ViewData, error) {
	var initial agenttui.ViewData
	if configured == nil {
		built, err := DefaultConnectingView("Spice Agent")
		if err != nil {
			return agenttui.ViewData{}, err
		}
		initial = built
	} else {
		if err := configured.Validate(); err != nil {
			return agenttui.ViewData{}, fmt.Errorf("initial view: %w", err)
		}
		initial = *configured
	}
	return initial, nil
}

// Close cancels in-flight Session work. It is safe to call more than once.
// Capturing the final screen remains valid after Close.
func (driver *Driver) Close() {
	if driver == nil || driver.closed {
		return
	}
	driver.closed = true
	driver.cancel()
}

// Resize updates the terminal canvas.
func (driver *Driver) Resize(width, height int) error {
	if err := driver.mutable(); err != nil {
		return err
	}
	next, command, err := presentation.TestApplyMessage(driver.model, tea.WindowSizeMsg{
		Width:  width,
		Height: height,
	})
	if err != nil {
		return err
	}
	driver.model = next
	return driver.drain(command)
}

// Type inserts text one Unicode grapheme cluster at a time through the editor.
func (driver *Driver) Type(text string) error {
	if err := driver.mutable(); err != nil {
		return err
	}
	if text == "" {
		return nil
	}
	if strings.ContainsAny(text, "\n\t\x1b") {
		return errors.New("typed text must not contain control characters")
	}
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		if err := driver.Key("text", graphemes.Str()); err != nil {
			return err
		}
	}
	return nil
}

// Key sends one keystroke. Optional text is used for printable insertion.
// Common strokes: enter, esc, backspace, left, right, up, down, home, end,
// ctrl+c, ctrl+q, ctrl+x, alt+enter, text.
func (driver *Driver) Key(stroke string, text ...string) error {
	if err := driver.mutable(); err != nil {
		return err
	}
	if len(text) > 1 {
		return errors.New("key accepts at most one text value")
	}
	payload := ""
	if len(text) > 0 {
		payload = text[0]
	}
	if stroke == "text" && payload == "" {
		return errors.New("Key(\"text\") requires printable text")
	}
	message, err := keyPress(stroke, payload)
	if err != nil {
		return err
	}
	return driver.apply(message)
}

// Action sends the first bound key for a semantic action.
func (driver *Driver) Action(action agenttui.Action) error {
	if err := driver.mutable(); err != nil {
		return err
	}
	stroke, exists := driver.actionKeys[action]
	if !exists {
		return fmt.Errorf("no injected binding for action %q", action)
	}
	return driver.Key(stroke)
}

// InjectUpdate applies a SessionUpdate as if Receive delivered it.
func (driver *Driver) InjectUpdate(update agenttui.SessionUpdate) error {
	if err := driver.mutable(); err != nil {
		return err
	}
	message, err := presentation.TestSessionUpdateMessage(update)
	if err != nil {
		return err
	}
	return driver.apply(message)
}

// Snapshot captures the current pixel-perfect screen and appends history.
func (driver *Driver) Snapshot(name string) (Screen, error) {
	screen, err := driver.Screen(name)
	if err != nil {
		return Screen{}, err
	}
	driver.snapshots = append(driver.snapshots, screen)
	return screen, nil
}

// Screen captures the current frame without recording history.
func (driver *Driver) Screen(name string) (Screen, error) {
	content, altScreen, cursorX, cursorY, cursorVisible := presentation.TestViewContent(driver.model)
	size := driver.model.Size()
	frame, err := agenttui.NewFrame(content, size)
	if err != nil {
		// Accessible mode is not always a fixed-size padded frame. Build a
		// synthetic size from the rendered content for agent inspection.
		lines := strings.Split(content, "\n")
		height := len(lines)
		width := 0
		for _, line := range lines {
			if w := CellWidth(line); w > width {
				width = w
			}
		}
		if width == 0 {
			width = 1
		}
		if height == 0 {
			height = 1
		}
		size = agenttui.BoundedSize(width, height)
		frame, err = agenttui.NewFrame(content, size)
		if err != nil {
			return Screen{}, err
		}
	}
	if cursorVisible {
		frame, err = frame.WithCursor(cursorX, cursorY)
		if err != nil {
			return Screen{}, err
		}
	}
	activity := make([]string, 0, len(driver.model.Activity()))
	for _, item := range driver.model.Activity() {
		activity = append(activity, item.String())
	}
	return FromFrame(
		frame,
		WithName(name),
		WithSemantic(
			driver.model.Editor().Value().String(),
			string(driver.model.Status().Level()),
			driver.model.Status().Message().String(),
			activity,
			driver.model.Revision(),
			driver.modelAccessible(),
			altScreen,
		),
	)
}

// History returns all Snapshot captures in order.
func (driver *Driver) History() []Screen {
	return append([]Screen(nil), driver.snapshots...)
}

// Prompt returns the current editor value.
func (driver *Driver) Prompt() string { return driver.model.Editor().Value().String() }

// StatusLevel returns the current status level.
func (driver *Driver) StatusLevel() agenttui.StatusLevel { return driver.model.Status().Level() }

// Revision returns the latest accepted session revision.
func (driver *Driver) Revision() uint64 { return driver.model.Revision() }

// QuitRequested reports whether the last key produced a quit command.
func (driver *Driver) QuitRequested() bool { return driver.lastQuit }

// LastResult returns the last accepted effect result.
func (driver *Driver) LastResult() (agenttui.CommandResult, bool) {
	return driver.model.LastResult()
}

// RunScenario executes scripted steps and snapshots after each named step.
func (driver *Driver) RunScenario(steps ...Step) error {
	for index, step := range steps {
		if err := step.apply(driver); err != nil {
			return fmt.Errorf("scenario step %d (%s): %w", index, step.label(), err)
		}
	}
	return nil
}

func (driver *Driver) apply(message tea.Msg) error {
	driver.lastQuit = false
	next, command, err := presentation.TestApplyMessage(driver.model, message)
	if err != nil {
		return err
	}
	driver.model = next
	if command == nil {
		return nil
	}
	// Quit commands cancel the effects context. Detect quit keystrokes without
	// executing the command so the driver remains usable for post-quit asserts.
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.Keystroke() {
		case "ctrl+c", "ctrl+q":
			driver.lastQuit = true
			driver.Close()
			return nil
		}
	}
	return driver.drain(command)
}

func (driver *Driver) drain(command tea.Cmd) error {
	if command == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), driver.drainTimeout)
	defer cancel()
	next, err := presentation.TestDrainCommand(ctx, driver.model, command, driver.maxSteps)
	driver.model = next
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			driver.Close()
			return fmt.Errorf("drain presentation command: %w", err)
		}
		return err
	}
	return nil
}

func (driver *Driver) modelAccessible() bool {
	// Accessible mode is private; infer from view flags via TestViewContent.
	_, altScreen, _, _, _ := presentation.TestViewContent(driver.model)
	// Normal mode always requests alt-screen; accessible mode does not.
	return !altScreen
}

// DefaultConnectingView builds the standard connecting snapshot used by
// autoconfigure, for tests that want the same initial chrome.
func DefaultConnectingView(title string) (agenttui.ViewData, error) {
	if strings.TrimSpace(title) == "" {
		title = "Spice Agent"
	}
	titleText, err := agenttui.NewText(title)
	if err != nil {
		return agenttui.ViewData{}, err
	}
	workspace, err := agenttui.NewWorkspace(titleText, nil)
	if err != nil {
		return agenttui.ViewData{}, err
	}
	message, err := agenttui.NewText("connecting to agent session")
	if err != nil {
		return agenttui.ViewData{}, err
	}
	status, err := agenttui.NewStatus(agenttui.StatusReconnecting, message, nil)
	if err != nil {
		return agenttui.ViewData{}, err
	}
	prompt, err := agenttui.NewEditor("")
	if err != nil {
		return agenttui.ViewData{}, err
	}
	return agenttui.NewViewData(workspace, status, prompt, nil)
}

func firstStrokes(bindings []agenttui.KeyBinding) map[agenttui.Action]string {
	result := make(map[agenttui.Action]string, len(bindings))
	for _, binding := range bindings {
		keys := binding.Keys()
		if len(keys) == 0 {
			continue
		}
		result[binding.Action()] = keys[0].Stroke()
	}
	return result
}

// GraphemeCount reports Unicode grapheme-cluster count for diagnostics.
func GraphemeCount(value string) int { return uniseg.GraphemeClusterCount(value) }

func (driver *Driver) mutable() error {
	if driver == nil || driver.closed {
		return ErrDriverClosed
	}
	return nil
}
