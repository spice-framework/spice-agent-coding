package presentation

import (
	"context"
	"errors"
	"fmt"
	"math"

	tea "charm.land/bubbletea/v2"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// Model is the deterministic Bubble Tea v2 presentation state. It owns no
// client, daemon, transport, annotation, or generated-application behavior.
type Model struct {
	renderer         agenttui.Renderer
	workspace        agenttui.WorkspaceState
	status           agenttui.StatusState
	editor           agenttui.EditorState
	activity         []agenttui.Text
	bindings         []agenttui.Binding
	effects          Effects
	effectsCancel    context.CancelFunc
	receiveEffect    func(OperationToken) tea.Cmd
	performEffect    func(OperationToken, agenttui.Intent) tea.Cmd
	theme            agenttui.ThemeState
	size             agenttui.Size
	revision         uint64
	accessible       bool
	promptHistory    []agenttui.Text
	historyIndex     int
	historyDraft     agenttui.EditorState
	receiveToken     OperationToken
	receiveArmed     bool
	operationToken   OperationToken
	operationActive  bool
	cancelToken      OperationToken
	cancelActive     bool
	lastResult       agenttui.CommandResult
	hasLastResult    bool
	pendingPrompt    agenttui.Text
	hasPendingPrompt bool
}

// NewModel constructs a bounded presentation model from immutable snapshots.
func NewModel(
	renderer agenttui.Renderer,
	workspace agenttui.WorkspaceState,
	status agenttui.StatusState,
	editor agenttui.EditorState,
	activity []agenttui.Text,
	theme agenttui.ThemeState,
	bindings []agenttui.KeyBinding,
	effects Effects,
) (Model, error) {
	if renderer == nil {
		return Model{}, errors.New("renderer must not be nil")
	}
	if err := theme.Validate(); err != nil {
		return Model{}, fmt.Errorf("theme must be initialized: %w", err)
	}
	if _, err := agenttui.NewViewData(workspace, status, editor, activity); err != nil {
		return Model{}, err
	}
	normalizedBindings, err := normalizeBindings(bindings)
	if err != nil {
		return Model{}, err
	}
	model := Model{
		renderer: renderer, workspace: workspace, status: status, editor: editor,
		activity: append([]agenttui.Text(nil), activity...), bindings: normalizedBindings,
		effects: effects,
		theme:   theme, size: agenttui.BoundedSize(80, 24), historyIndex: -1,
		historyDraft: editor,
	}
	if effects != nil {
		model.receiveToken = 1
		model.receiveArmed = true
	}
	return model, nil
}

// WithAccessibleMode returns a model that renders unstyled text without the
// alternate screen. Status labels remain explicit and keyboard behavior is
// unchanged.
func (model Model) WithAccessibleMode(enabled bool) Model {
	model.accessible = enabled
	return model
}

// withEffectsContext binds all asynchronous commands to one shell-owned
// lifecycle. The context is never accepted through public semantic values.
func (model Model) withEffectsContext(ctx context.Context, cancel context.CancelFunc) Model {
	model.effectsCancel = cancel
	if model.effects != nil {
		effects := model.effects
		model.receiveEffect = func(token OperationToken) tea.Cmd {
			return receiveCommand(ctx, effects, token)
		}
		model.performEffect = func(token OperationToken, intent agenttui.Intent) tea.Cmd {
			return effectCommand(ctx, effects, token, intent)
		}
	}
	return model
}

// Init implements tea.Model. When effects are injected it returns one command
// that owns the blocking receive; Init itself performs no I/O.
func (model Model) Init() tea.Cmd {
	if model.receiveEffect == nil || !model.receiveArmed {
		return nil
	}
	return model.receiveEffect(model.receiveToken)
}

// Update implements tea.Model using only resize and presentation key events.
func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		model.size = agenttui.BoundedSize(message.Width, message.Height)
		return model, nil
	case sessionUpdateMsg:
		return model.applySessionUpdate(message.update), nil
	case receiveCompletedMsg:
		return model.completeReceive(message)
	case effectCompletedMsg:
		return model.completeEffect(message)
	case tea.KeyPressMsg:
		return model.updateKey(message)
	default:
		return model, nil
	}
}

func (model Model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key, err := agenttui.NewKey(message.Keystroke(), message.Key().Text)
	if err != nil {
		return model, nil
	}
	for _, binding := range model.bindings {
		if !binding.Matches(key) {
			continue
		}
		return model.applyAction(binding.Action())
	}
	if key.Text() != "" {
		if editor, insertErr := model.editor.Insert(key.Text()); insertErr == nil {
			model.editor = editor
			model = model.detachHistory()
		}
	}
	return model, nil
}

func (model Model) applyAction(action agenttui.Action) (tea.Model, tea.Cmd) {
	switch action {
	case agenttui.ActionQuit:
		return model, quitCommand(model.effectsCancel)
	case agenttui.ActionSubmit:
		return model.issuePromptIntent(agenttui.IntentSubmit)
	case agenttui.ActionCancelActiveRun:
		return model.issueIntent(agenttui.IntentCancelActiveRun, nil)
	case agenttui.ActionRespond:
		return model.issuePromptIntent(agenttui.IntentRespond)
	case agenttui.ActionCursorLeft:
		model.editor = model.editor.Move(agenttui.MoveLeft)
	case agenttui.ActionCursorRight:
		model.editor = model.editor.Move(agenttui.MoveRight)
	case agenttui.ActionCursorStart:
		model.editor = model.editor.Move(agenttui.MoveStart)
	case agenttui.ActionCursorEnd:
		model.editor = model.editor.Move(agenttui.MoveEnd)
	case agenttui.ActionHistoryPrevious:
		model = model.moveHistory(-1)
	case agenttui.ActionHistoryNext:
		model = model.moveHistory(1)
	case agenttui.ActionBackspace:
		model.editor = model.editor.Backspace()
		model = model.detachHistory()
	}
	return model, nil
}

func quitCommand(cancel context.CancelFunc) tea.Cmd {
	if cancel == nil {
		return tea.Quit
	}
	return func() tea.Msg {
		cancel()
		return tea.Quit()
	}
}

// View implements tea.Model and always requests the alternate screen.
func (model Model) View() tea.View {
	data, err := agenttui.NewViewData(model.workspace, model.status, model.editor, model.activity)
	if err != nil {
		return failureView(model.accessible)
	}
	if model.accessible {
		view := tea.NewView(renderAccessible(data))
		view.AltScreen = false
		return view
	}
	frame, err := model.renderer.Render(data, model.size, model.theme)
	if err != nil {
		return failureView(model.accessible)
	}
	view := tea.NewView(frame.Content())
	view.AltScreen = true
	if x, y, visible := frame.Cursor(); visible {
		view.Cursor = tea.NewCursor(x, y)
	}
	return view
}

// Size returns the current bounded terminal size for deterministic tests.
func (model Model) Size() agenttui.Size { return model.size }

// Editor returns the current immutable prompt snapshot.
func (model Model) Editor() agenttui.EditorState { return model.editor }

// Revision returns the latest accepted streaming presentation revision.
func (model Model) Revision() uint64 { return model.revision }

// Activity returns a defensive copy of the current bounded activity window.
func (model Model) Activity() []agenttui.Text { return append([]agenttui.Text(nil), model.activity...) }

// Status returns the current explicit presentation status.
func (model Model) Status() agenttui.StatusState { return model.status }

// LastResult returns the most recent accepted asynchronous effect result.
func (model Model) LastResult() (agenttui.CommandResult, bool) {
	return model.lastResult, model.hasLastResult
}

func failureView(accessible bool) tea.View {
	view := tea.NewView("Spice Agent TUI\n[ERROR] render unavailable")
	view.AltScreen = !accessible
	return view
}

func (model Model) applySessionUpdate(update agenttui.SessionUpdate) Model {
	if update.Validate() != nil || update.Revision() <= model.revision {
		return model
	}
	switch update.Kind() {
	case agenttui.SessionUpdateSnapshot:
		snapshot, present := update.Snapshot()
		if !present {
			return model
		}
		model.workspace = snapshot.Workspace()
		model.status = snapshot.Status()
		model.activity = snapshot.Activity()
		model.promptHistory = snapshot.PromptHistory()
		model.historyIndex = -1
		model.historyDraft = model.editor
	case agenttui.SessionUpdateActivity:
		item, present := update.Activity()
		if !present {
			return model
		}
		activity := append(append([]agenttui.Text(nil), model.activity...), item)
		for len(activity) > 0 {
			if _, err := agenttui.NewViewData(model.workspace, model.status, model.editor, activity); err == nil {
				break
			}
			activity = activity[1:]
		}
		model.activity = activity
	case agenttui.SessionUpdatePromptHistory:
		history, present := update.PromptHistory()
		if !present {
			return model
		}
		model.promptHistory = history
		model.historyIndex = -1
		model.historyDraft = model.editor
	default:
		return model
	}
	model.revision = update.Revision()
	return model
}

func (model Model) moveHistory(direction int) Model {
	if len(model.promptHistory) == 0 {
		return model
	}
	if direction < 0 {
		if model.historyIndex < 0 {
			model.historyDraft = model.editor
			model.historyIndex = len(model.promptHistory) - 1
		} else {
			model.historyIndex = max(model.historyIndex-1, 0)
		}
	} else if model.historyIndex >= 0 {
		model.historyIndex++
		if model.historyIndex >= len(model.promptHistory) {
			model.historyIndex = -1
			model.editor = model.historyDraft
			return model
		}
	}
	if model.historyIndex >= 0 {
		editor, err := agenttui.NewEditor(model.promptHistory[model.historyIndex].String())
		if err == nil {
			model.editor = editor
		}
	}
	return model
}

func (model Model) detachHistory() Model {
	model.historyIndex = -1
	model.historyDraft = model.editor
	return model
}

func (model Model) issuePromptIntent(kind agenttui.IntentKind) (tea.Model, tea.Cmd) {
	value := model.editor.Value()
	if value.String() == "" {
		return model, nil
	}
	model, command := model.issueIntent(kind, []agenttui.Text{value})
	if command == nil {
		return model, nil
	}
	model.pendingPrompt = value
	model.hasPendingPrompt = true
	return model, command
}

func (model Model) issueIntent(kind agenttui.IntentKind, values []agenttui.Text) (Model, tea.Cmd) {
	if kind == agenttui.IntentCancelActiveRun {
		return model.issueCancelIntent()
	}
	if model.performEffect == nil || model.operationActive || model.operationToken == math.MaxUint64 {
		return model, nil
	}
	intent, err := agenttui.NewIntent(kind, values)
	if err != nil {
		return model, nil
	}
	model.operationToken++
	model.operationActive = true
	return model, model.performEffect(model.operationToken, intent)
}

func (model Model) issueCancelIntent() (Model, tea.Cmd) {
	if model.performEffect == nil || model.cancelActive || model.cancelToken == math.MaxUint64 {
		return model, nil
	}
	intent, err := agenttui.NewIntent(agenttui.IntentCancelActiveRun, nil)
	if err != nil {
		return model, nil
	}
	model.cancelToken++
	model.cancelActive = true
	return model, model.performEffect(model.cancelToken, intent)
}

func (model Model) completeEffect(message effectCompletedMsg) (tea.Model, tea.Cmd) {
	if message.kind == agenttui.IntentCancelActiveRun {
		return model.completeCancelEffect(message), nil
	}
	if !model.operationActive || message.token != model.operationToken {
		return model, nil
	}
	model.operationActive = false
	if message.err != nil {
		model.pendingPrompt = agenttui.Text{}
		model.hasPendingPrompt = false
		if status := effectErrorStatus(message.err); status.Validate() == nil {
			model.status = status
		}
		return model, nil
	}
	if message.result.Validate() != nil {
		model.pendingPrompt = agenttui.Text{}
		model.hasPendingPrompt = false
		return model, nil
	}
	if model.hasPendingPrompt {
		model.promptHistory = append(model.promptHistory, model.pendingPrompt)
		if len(model.promptHistory) > agenttui.MaximumPromptHistoryItems {
			model.promptHistory = append([]agenttui.Text(nil), model.promptHistory[len(model.promptHistory)-agenttui.MaximumPromptHistoryItems:]...)
		}
		if model.editor.Value() == model.pendingPrompt {
			model.editor = model.editor.Clear()
			model = model.detachHistory()
		}
	}
	model.pendingPrompt = agenttui.Text{}
	model.hasPendingPrompt = false
	model.lastResult = message.result
	model.hasLastResult = true
	return model, nil
}

func (model Model) completeCancelEffect(message effectCompletedMsg) Model {
	if !model.cancelActive || message.token != model.cancelToken {
		return model
	}
	model.cancelActive = false
	if message.err != nil {
		if status := effectErrorStatus(message.err); status.Validate() == nil {
			model.status = status
		}
		return model
	}
	if message.result.Validate() != nil {
		return model
	}
	model.lastResult = message.result
	model.hasLastResult = true
	return model
}

func (model Model) completeReceive(message receiveCompletedMsg) (tea.Model, tea.Cmd) {
	if !model.receiveArmed || message.token != model.receiveToken {
		return model, nil
	}
	model.receiveArmed = false
	if message.err != nil {
		if status := effectErrorStatus(message.err); status.Validate() == nil {
			model.status = status
		}
		return model, nil
	}
	switch received := message.message.(type) {
	case sessionUpdateMsg:
		if received.update.Validate() != nil || received.update.Revision() <= model.revision {
			model.status = effectErrorStatus(errors.New("session update sequence is invalid"))
			return model, nil
		}
		model = model.applySessionUpdate(received.update)
	default:
		return model, nil
	}
	if model.receiveToken == math.MaxUint64 {
		return model, nil
	}
	model.receiveToken++
	model.receiveArmed = true
	return model, model.receiveEffect(model.receiveToken)
}

func normalizeBindings(bindings []agenttui.KeyBinding) ([]agenttui.Binding, error) {
	if len(bindings) == 0 {
		return nil, errors.New("at least one key binding must be injected")
	}
	actions := make(map[agenttui.Action]int, len(bindings))
	keys := make(map[string]int)
	result := make([]agenttui.Binding, 0, len(bindings))
	for index, source := range bindings {
		if source == nil {
			return nil, fmt.Errorf("key binding %d must not be nil", index)
		}
		binding, err := agenttui.NewBinding(source.Action(), source.Keys(), source.Help())
		if err != nil {
			return nil, fmt.Errorf("key binding %d: %w", index, err)
		}
		if previous, exists := actions[binding.Action()]; exists {
			return nil, fmt.Errorf("key action %q collides at indexes %d and %d", binding.Action(), previous, index)
		}
		actions[binding.Action()] = index
		for _, key := range binding.Keys() {
			if previous, exists := keys[key.Stroke()]; exists {
				return nil, fmt.Errorf("keystroke %q collides at indexes %d and %d", key.Stroke(), previous, index)
			}
			keys[key.Stroke()] = index
		}
		result = append(result, binding)
	}
	return result, nil
}

var _ tea.Model = Model{}
