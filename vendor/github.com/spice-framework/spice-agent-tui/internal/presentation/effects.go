package presentation

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// OperationToken identifies one presentation-issued asynchronous operation.
// It is local process state, not a daemon or protocol operation identifier.
type OperationToken uint64

// Effects is the adapter seam for asynchronous presentation work. Implementors
// may later delegate to an adopted high-level client, but the model only calls
// these methods from Bubble Tea commands.
type Effects interface {
	Receive(context.Context, OperationToken) (tea.Msg, error)
	Perform(context.Context, OperationToken, agenttui.Intent) (agenttui.CommandResult, error)
}

var errSessionPanicked = errors.New("session operation panicked")

// sessionEffects is the only adapter from the public UI-neutral Session to
// Bubble Tea commands. Each command invokes the session exactly once.
type sessionEffects struct {
	session agenttui.Session
}

// NewSessionEffects validates and adapts a public Session. The returned seam is
// internal presentation machinery and never appears in terminal public APIs.
func NewSessionEffects(session agenttui.Session) (Effects, error) {
	if session == nil {
		return nil, errors.New("session must not be nil")
	}
	return sessionEffects{session: session}, nil
}

func (effects sessionEffects) Receive(
	ctx context.Context,
	_ OperationToken,
) (message tea.Msg, returnErr error) {
	if ctx == nil {
		return nil, errors.New("session receive context must not be nil")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	defer func() {
		if recover() == nil {
			return
		}
		if err := context.Cause(ctx); err != nil {
			returnErr = err
		} else {
			returnErr = errSessionPanicked
		}
		message = nil
	}()
	update, err := effects.session.Receive(ctx)
	if err != nil {
		return nil, err
	}
	if err := update.Validate(); err != nil {
		return nil, fmt.Errorf("session update: %w", err)
	}
	return sessionUpdateMsg{update: update}, nil
}

func (effects sessionEffects) Perform(
	ctx context.Context,
	_ OperationToken,
	intent agenttui.Intent,
) (result agenttui.CommandResult, returnErr error) {
	if ctx == nil {
		return agenttui.CommandResult{}, errors.New("session perform context must not be nil")
	}
	if err := context.Cause(ctx); err != nil {
		return agenttui.CommandResult{}, err
	}
	if err := intent.Validate(); err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("session intent: %w", err)
	}
	defer func() {
		if recover() == nil {
			return
		}
		result = agenttui.CommandResult{}
		if err := context.Cause(ctx); err != nil {
			returnErr = err
		} else {
			returnErr = errSessionPanicked
		}
	}()
	result, err := effects.session.Perform(ctx, intent)
	if err != nil {
		return agenttui.CommandResult{}, err
	}
	if err := result.Validate(); err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("session result: %w", err)
	}
	if _, nested := result.Intent(); nested {
		return agenttui.CommandResult{}, errors.New("session result must not contain a nested intent")
	}
	return result, nil
}

type receiveCompletedMsg struct {
	token   OperationToken
	message tea.Msg
	err     error
}

type effectCompletedMsg struct {
	token  OperationToken
	kind   agenttui.IntentKind
	result agenttui.CommandResult
	err    error
}

type sessionUpdateMsg struct {
	update agenttui.SessionUpdate
}

func receiveCommand(ctx context.Context, effects Effects, token OperationToken) tea.Cmd {
	return func() tea.Msg {
		message, err := effects.Receive(ctx, token)
		return receiveCompletedMsg{token: token, message: message, err: err}
	}
}

func effectCommand(ctx context.Context, effects Effects, token OperationToken, intent agenttui.Intent) tea.Cmd {
	return func() tea.Msg {
		result, err := effects.Perform(ctx, token, intent)
		return effectCompletedMsg{token: token, kind: intent.Kind(), result: result, err: err}
	}
}

func effectErrorStatus(err error) agenttui.StatusState {
	message, textErr := agenttui.NewText("operation failed; inspect application diagnostics")
	if textErr != nil || err == nil {
		return agenttui.StatusState{}
	}
	status, statusErr := agenttui.NewStatus(agenttui.StatusError, message, nil)
	if statusErr != nil {
		return agenttui.StatusState{}
	}
	return status
}
