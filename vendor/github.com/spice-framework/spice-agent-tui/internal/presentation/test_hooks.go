package presentation

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// TestApplyMessage applies one Bubble Tea message to the model. It is the
// deterministic same-module seam used by tuittest and must not be treated as a
// public product API.
func TestApplyMessage(model Model, message tea.Msg) (Model, tea.Cmd, error) {
	updated, command := model.Update(message)
	next, ok := updated.(Model)
	if !ok {
		return Model{}, nil, fmt.Errorf("presentation model type became %T", updated)
	}
	return next, command, nil
}

// TestSessionUpdateMessage wraps a UI-neutral session update as the private
// presentation message type. Callers never construct that type themselves.
func TestSessionUpdateMessage(update agenttui.SessionUpdate) (tea.Msg, error) {
	if err := update.Validate(); err != nil {
		return nil, fmt.Errorf("session update: %w", err)
	}
	return sessionUpdateMsg{update: update}, nil
}

// TestWithEffectsContext binds async receive/perform command factories to the
// provided lifecycle. Used by tuittest drivers.
func TestWithEffectsContext(model Model, ctx context.Context, cancel context.CancelFunc) Model {
	return model.withEffectsContext(ctx, cancel)
}

// TestDrainCommand executes one tea.Cmd chain with a hard step bound. Session
// receive/perform commands that block forever return an error rather than hang
// agent-driven tests.
func TestDrainCommand(ctx context.Context, model Model, command tea.Cmd, maximumSteps int) (Model, error) {
	if maximumSteps <= 0 {
		maximumSteps = 32
	}
	for step := 0; command != nil && step < maximumSteps; step++ {
		if err := ctx.Err(); err != nil {
			return model, err
		}
		message, err := runCommandOnce(ctx, command)
		if err != nil {
			return model, err
		}
		if message == nil {
			return model, nil
		}
		next, nextCommand, applyErr := TestApplyMessage(model, message)
		if applyErr != nil {
			return model, applyErr
		}
		model = next
		command = nextCommand
	}
	if command != nil {
		return model, fmt.Errorf("command chain exceeded %d steps", maximumSteps)
	}
	return model, nil
}

func runCommandOnce(ctx context.Context, command tea.Cmd) (tea.Msg, error) {
	if command == nil {
		return nil, errors.New("presentation command must not be nil")
	}
	type result struct {
		message tea.Msg
	}
	done := make(chan result, 1)
	go func() {
		done <- result{message: command()}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-done:
		return value.message, nil
	}
}

// TestViewContent returns the current rendered view content without exposing
// Bubble Tea view types.
func TestViewContent(model Model) (content string, altScreen bool, cursorX, cursorY int, cursorVisible bool) {
	view := model.View()
	if view.Cursor != nil {
		return view.Content, view.AltScreen, view.Cursor.X, view.Cursor.Y, true
	}
	return view.Content, view.AltScreen, 0, 0, false
}
