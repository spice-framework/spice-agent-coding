package terminal

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

type accessibleTestSession struct {
	mutex      sync.Mutex
	updates    chan agenttui.SessionUpdate
	intents    []agenttui.Intent
	performErr error
}

func (session *accessibleTestSession) Receive(ctx context.Context) (agenttui.SessionUpdate, error) {
	select {
	case <-ctx.Done():
		return agenttui.SessionUpdate{}, context.Cause(ctx)
	case update, available := <-session.updates:
		if !available {
			return agenttui.SessionUpdate{}, errors.New("test session closed")
		}
		return update, nil
	}
}

func (session *accessibleTestSession) Perform(
	ctx context.Context,
	intent agenttui.Intent,
) (agenttui.CommandResult, error) {
	if err := context.Cause(ctx); err != nil {
		return agenttui.CommandResult{}, err
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.intents = append(session.intents, intent)
	return agenttui.CommandResult{}, session.performErr
}

func TestAccessibleShellConstructionAndRunBoundaries(t *testing.T) {
	t.Parallel()
	initial := accessibleInitialView(t)
	streams, err := agenttui.NewTerminalIO(strings.NewReader("\x11"), new(bytes.Buffer))
	if err != nil {
		t.Fatal(err)
	}
	session := &accessibleTestSession{updates: make(chan agenttui.SessionUpdate, 1)}
	snapshot := accessibleSnapshotUpdate(t, 1, initial, nil)
	session.updates <- snapshot

	if shell, constructErr := NewTerminalShell(
		nil, nil, nil, nil, initial, streams, agenttui.NewTerminalConfig(true),
	); constructErr == nil || shell != nil {
		t.Fatalf("nil-session shell = %T, %v", shell, constructErr)
	}
	if shell, constructErr := NewTerminalShell(
		session, nil, nil, nil, agenttui.ViewData{}, streams, agenttui.NewTerminalConfig(true),
	); constructErr == nil || shell != nil {
		t.Fatalf("invalid-view shell = %T, %v", shell, constructErr)
	}
	if shell, constructErr := NewTerminalShell(
		session, nil, nil, nil, initial, agenttui.TerminalIO{}, agenttui.NewTerminalConfig(true),
	); constructErr == nil || shell != nil {
		t.Fatalf("invalid-stream shell = %T, %v", shell, constructErr)
	}
	shell, err := NewTerminalShell(
		session, nil, nil, nil, initial, streams, agenttui.NewTerminalConfig(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = shell.Run(nil); err == nil { //nolint:staticcheck // Boundary deliberately rejects nil.
		t.Fatal("Run(nil) error = nil")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = shell.Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run(cancelled) error = %v", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err = shell.Run(ctx); err != nil {
		t.Fatalf("Run(accessible) error = %v", err)
	}
}

func TestAccessibleModelEditingSubmissionAndUpdates(t *testing.T) {
	t.Parallel()
	initial := accessibleInitialView(t)
	session := &accessibleTestSession{updates: make(chan agenttui.SessionUpdate, 2)}
	model := &accessibleModel{
		context: func() context.Context { return t.Context() },
		session: session, workspace: initial.Workspace(), status: initial.Status(),
		activity: initial.Activity(),
	}

	if command := model.Init(); command == nil {
		t.Fatal("Init() command = nil")
	}
	history := []agenttui.Text{accessibleText(t, "first"), accessibleText(t, "second")}
	snapshot := accessibleSnapshotUpdate(t, 1, initial, history)
	nextModel, command := model.Update(sessionUpdateMessage{update: snapshot})
	if nextModel != model || command == nil || !model.ready || model.historyAt != len(history) {
		t.Fatal("snapshot update did not initialize accessible model")
	}
	if view := model.View().Content; !strings.Contains(view, "[READY]") || !strings.Contains(view, "Spice Agent") {
		t.Fatalf("ready view = %q", view)
	}

	model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if model.prompt != "second" {
		t.Fatalf("previous prompt = %q", model.prompt)
	}
	model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if model.prompt != "second" {
		t.Fatalf("next prompt = %q", model.prompt)
	}
	model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model.updateKey(tea.KeyPressMsg(tea.Key{Code: 'h', Text: "hé"}))
	model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if model.prompt != "h" {
		t.Fatalf("unicode backspace prompt = %q", model.prompt)
	}

	_, command = model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || !model.performing || model.prompt != "" {
		t.Fatal("submit did not create one asynchronous command")
	}
	result, ok := command().(commandResultMessage)
	if !ok || result.err != nil {
		t.Fatalf("submit command result = %#v", result)
	}
	model.Update(result)
	session.mutex.Lock()
	intents := append([]agenttui.Intent(nil), session.intents...)
	session.mutex.Unlock()
	if len(intents) != 1 || intents[0].Kind() != agenttui.IntentSubmit ||
		intents[0].Values()[0].String() != "h" {
		t.Fatalf("submitted intents = %#v", intents)
	}

	completion, err := agenttui.NewActivityUpdate(2, accessibleText(t, "run.completed"))
	if err != nil {
		t.Fatal(err)
	}
	if err = model.apply(completion); err != nil || model.terminals != 1 {
		t.Fatalf("apply completion = terminals %d, %v", model.terminals, err)
	}
	historyUpdate, err := agenttui.NewPromptHistoryUpdate(3, []agenttui.Text{accessibleText(t, "third")})
	if err != nil {
		t.Fatal(err)
	}
	if err = model.apply(historyUpdate); err != nil || len(model.history) != 1 {
		t.Fatalf("apply history = %#v, %v", model.history, err)
	}
	if err = model.apply(historyUpdate); err == nil {
		t.Fatal("non-increasing revision was accepted")
	}

	model.ready = false
	model.status = accessibleStatus(t, agenttui.StatusReconnecting, "waiting")
	model.activity = []agenttui.Text{accessibleText(t, "line one\nline two")}
	if view := model.View().Content; !strings.Contains(view, "[RECONNECTING] waiting") ||
		!strings.Contains(view, "- line two") || !strings.Contains(view, "Completed runs: 1") {
		t.Fatalf("reconnecting view = %q", view)
	}
	model.prompt = ""
	model.backspace()
	model.previousPrompt()
	model.nextPrompt()
	model.performing = true
	if _, command = model.submit(); command != nil {
		t.Fatal("submit while performing returned a command")
	}
	model.performing = false
	model.prompt = "   "
	if _, command = model.submit(); command != nil {
		t.Fatal("blank submit returned a command")
	}
	model.prompt = "\x00"
	model.submit()
	model.appendActivity("\x00")
	model.prompt = strings.Repeat("x", agenttui.MaximumTextBytes)
	model.updateKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if len(model.prompt) != agenttui.MaximumTextBytes {
		t.Fatal("maximum prompt accepted additional text")
	}
	if _, quit := model.updateKey(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})); quit == nil {
		t.Fatal("ctrl+c did not request quit")
	}
}

func TestAccessibleModelReceiveAndFailurePaths(t *testing.T) {
	t.Parallel()
	initial := accessibleInitialView(t)
	session := &accessibleTestSession{updates: make(chan agenttui.SessionUpdate, 1), performErr: errors.New("perform failed")}
	model := &accessibleModel{
		context: func() context.Context { return t.Context() }, session: session,
		workspace: initial.Workspace(), status: initial.Status(),
	}
	session.updates <- accessibleSnapshotUpdate(t, 1, initial, nil)
	message, ok := model.receive().(sessionUpdateMessage)
	if !ok || message.err != nil {
		t.Fatalf("receive message = %#v", message)
	}
	model.Update(message)
	model.prompt = "work"
	_, command := model.submit()
	if command == nil {
		t.Fatal("submit command = nil")
	}
	commandMessage := command()
	result, ok := commandMessage.(commandResultMessage)
	if !ok {
		t.Fatalf("submit result type is unexpected: %T", commandMessage)
	}
	model.Update(result)
	if len(model.activity) == 0 || model.activity[len(model.activity)-1].String() !=
		"operation failed; inspect application diagnostics" {
		t.Fatalf("failure activity = %#v", model.activity)
	}
	if next, follow := model.Update(struct{}{}); next != model || follow != nil {
		t.Fatal("unknown message changed model")
	}
	close(session.updates)
	if _, quit := model.Update(model.receive()); quit == nil {
		t.Fatal("receive failure did not request quit")
	}
	if _, quit := model.Update(sessionUpdateMessage{update: agenttui.SessionUpdate{}}); quit == nil {
		t.Fatal("invalid update did not request quit")
	}
}

func TestAccessibleModelCancellationBindings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		key        tea.Key
		wantCancel bool
		wantQuit   bool
	}{
		{name: "Escape cancels", key: tea.Key{Code: tea.KeyEscape}, wantCancel: true},
		{name: "Ctrl+X cancels", key: tea.Key{Code: 'x', Mod: tea.ModCtrl}, wantCancel: true},
		{name: "Ctrl+C only quits", key: tea.Key{Code: 'c', Mod: tea.ModCtrl}, wantQuit: true},
		{name: "Ctrl+Q only quits", key: tea.Key{Code: 'q', Mod: tea.ModCtrl}, wantQuit: true},
		{name: "ordinary input does neither", key: tea.Key{Code: 'x', Text: "x"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			initial := accessibleInitialView(t)
			session := &accessibleTestSession{updates: make(chan agenttui.SessionUpdate)}
			model := &accessibleModel{
				context: func() context.Context { return t.Context() }, session: session,
				workspace: initial.Workspace(), status: initial.Status(),
			}
			_, command := model.updateKey(tea.KeyPressMsg(test.key))
			if test.wantCancel {
				if command == nil || !model.cancelling {
					t.Fatal("cancel binding did not create a cancellation command")
				}
				result, ok := command().(commandResultMessage)
				if !ok || result.lane != commandLaneCancel || result.err != nil {
					t.Fatalf("cancel command result = %#v", result)
				}
				model.Update(result)
			} else if test.wantQuit != (command != nil) {
				t.Fatalf("quit command presence = %v, want %v", command != nil, test.wantQuit)
			}
			session.mutex.Lock()
			defer session.mutex.Unlock()
			if test.wantCancel {
				if len(session.intents) != 1 || session.intents[0].Kind() != agenttui.IntentCancelActiveRun {
					t.Fatalf("cancel intents = %#v", session.intents)
				}
			} else if len(session.intents) != 0 {
				t.Fatalf("non-cancel key emitted intents = %#v", session.intents)
			}
		})
	}
}

func TestAccessibleModelCancellationUsesIndependentLane(t *testing.T) {
	t.Parallel()
	initial := accessibleInitialView(t)
	session := newAccessibleConcurrentSession()
	model := &accessibleModel{
		context: func() context.Context { return t.Context() }, session: session,
		workspace: initial.Workspace(), status: initial.Status(), prompt: "blocked work",
	}

	_, ordinary := model.submit()
	if ordinary == nil || !model.performing {
		t.Fatal("submit did not occupy the ordinary lane")
	}
	ordinaryResult := make(chan commandResultMessage, 1)
	go func() {
		message, ok := ordinary().(commandResultMessage)
		if !ok {
			message = commandResultMessage{lane: commandLaneOrdinary, err: errors.New("unexpected ordinary command result")}
		}
		ordinaryResult <- message
	}()
	select {
	case <-session.ordinaryStarted:
	case <-time.After(time.Second):
		t.Fatal("ordinary lane did not start")
	}

	_, cancel := model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cancel == nil || !model.cancelling || !model.performing {
		t.Fatal("Escape did not occupy the independent cancel lane")
	}
	cancelResult, ok := cancel().(commandResultMessage)
	if !ok || cancelResult.lane != commandLaneCancel || cancelResult.err != nil {
		t.Fatalf("Escape cancellation result = %#v", cancelResult)
	}
	model.Update(cancelResult)
	if model.cancelling || !model.performing {
		t.Fatal("cancel completion changed the blocked ordinary lane")
	}
	if _, secondCancel := model.updateKey(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl})); secondCancel == nil {
		// The first cancel has completed, so Ctrl+X is a second valid request.
		t.Fatal("Ctrl+X did not map to active-run cancellation")
	} else {
		result, resultOK := secondCancel().(commandResultMessage)
		if !resultOK || result.lane != commandLaneCancel || result.err != nil {
			t.Fatalf("Ctrl+X cancellation result = %#v", result)
		}
		model.Update(result)
	}
	model.cancelling = true
	if _, duplicate := model.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})); duplicate != nil {
		t.Fatal("a busy cancel lane accepted a duplicate request")
	}
	model.cancelling = false

	if _, quit := model.updateKey(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})); quit == nil {
		t.Fatal("Ctrl+C did not remain a quit-only binding")
	}
	if got := session.intentKinds(); !slices.Equal(got, []agenttui.IntentKind{
		agenttui.IntentSubmit, agenttui.IntentCancelActiveRun, agenttui.IntentCancelActiveRun,
	}) {
		t.Fatalf("intent order before ordinary release = %v", got)
	}
	close(session.releaseOrdinary)
	select {
	case result := <-ordinaryResult:
		if result.lane != commandLaneOrdinary || result.err != nil {
			t.Fatalf("ordinary result = %#v", result)
		}
		model.Update(result)
	case <-time.After(time.Second):
		t.Fatal("ordinary lane did not finish")
	}
	if model.performing || model.cancelling {
		t.Fatal("accessible command lanes remained busy")
	}
}

type accessibleConcurrentSession struct {
	mu              sync.Mutex
	intents         []agenttui.IntentKind
	ordinaryStarted chan struct{}
	releaseOrdinary chan struct{}
	ordinaryOnce    sync.Once
}

func newAccessibleConcurrentSession() *accessibleConcurrentSession {
	return &accessibleConcurrentSession{
		ordinaryStarted: make(chan struct{}), releaseOrdinary: make(chan struct{}),
	}
}

func (*accessibleConcurrentSession) Receive(context.Context) (agenttui.SessionUpdate, error) {
	return agenttui.SessionUpdate{}, errors.New("concurrent test session does not receive")
}

func (session *accessibleConcurrentSession) Perform(
	ctx context.Context,
	intent agenttui.Intent,
) (agenttui.CommandResult, error) {
	session.mu.Lock()
	session.intents = append(session.intents, intent.Kind())
	session.mu.Unlock()
	if intent.Kind() == agenttui.IntentSubmit {
		session.ordinaryOnce.Do(func() { close(session.ordinaryStarted) })
		select {
		case <-session.releaseOrdinary:
		case <-ctx.Done():
			return agenttui.CommandResult{}, context.Cause(ctx)
		}
	}
	return agenttui.CommandResult{}, nil
}

func (session *accessibleConcurrentSession) intentKinds() []agenttui.IntentKind {
	session.mu.Lock()
	defer session.mu.Unlock()
	return slices.Clone(session.intents)
}

func accessibleInitialView(t *testing.T) agenttui.ViewData {
	t.Helper()
	workspace, err := agenttui.NewWorkspace(accessibleText(t, "Spice Agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	status := accessibleStatus(t, agenttui.StatusReconnecting, "connecting")
	editor, err := agenttui.NewEditor("")
	if err != nil {
		t.Fatal(err)
	}
	view, err := agenttui.NewViewData(workspace, status, editor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func accessibleSnapshotUpdate(
	t *testing.T,
	revision uint64,
	view agenttui.ViewData,
	history []agenttui.Text,
) agenttui.SessionUpdate {
	t.Helper()
	snapshot, err := agenttui.NewSessionSnapshot(
		revision, view.Workspace(), view.Status(), view.Activity(), history,
	)
	if err != nil {
		t.Fatal(err)
	}
	update, err := agenttui.NewSnapshotUpdate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func accessibleStatus(t *testing.T, level agenttui.StatusLevel, value string) agenttui.StatusState {
	t.Helper()
	status, err := agenttui.NewStatus(level, accessibleText(t, value), nil)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func accessibleText(t *testing.T, value string) agenttui.Text {
	t.Helper()
	text, err := agenttui.NewText(value)
	if err != nil {
		t.Fatal(err)
	}
	return text
}
