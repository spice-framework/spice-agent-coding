package tuisession

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
)

type sessionCommands struct {
	owner         *Session
	ordinaryMutex sync.Mutex
	cancelMutex   sync.Mutex
}

func (session *sessionCommands) performSubmit(
	ctx context.Context,
	prompt agenttui.Text,
) (agenttui.CommandResult, error) {
	if _, err := agenttui.NewEditor(prompt.String()); err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	session.owner.stateMutex.Lock()
	active := session.owner.hasActiveRun
	session.owner.stateMutex.Unlock()
	if active {
		return agenttui.CommandResult{}, errors.New("submit prompt: a run is already active")
	}
	operation, err := session.owner.newOperationID()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	messageID, err := session.owner.nextIdentifier()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: create message ID: %w", err)
	}
	input, err := client.NewInput(messageID, prompt.String())
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	request, err := client.NewStartRequest(operation, session.owner.config.Definition, input)
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	clientSession := session.owner.currentClient()
	operationContext, cancel := session.owner.operationContext(ctx)
	result, err := clientSession.Start(operationContext, request)
	cancel()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	session.owner.stateMutex.Lock()
	session.owner.activeRun = result.Run()
	session.owner.hasActiveRun = true
	session.owner.eventCursor = 0
	session.owner.promptHistory = (historyBuffer{}).append(session.owner.promptHistory, prompt)
	history := slices.Clone(session.owner.promptHistory)
	session.owner.stateMutex.Unlock()
	if err = session.owner.publishHistory(history); err != nil && !errors.Is(err, client.ErrClosed) {
		return agenttui.CommandResult{}, fmt.Errorf("publish submitted prompt: %w", err)
	}
	session.owner.startWorker(func() { session.owner.observeEvents(result.Run()) })
	return (commandResultFactory{}).new("run " + result.Run().ID() + " started")
}

func (session *sessionCommands) performCancel(ctx context.Context) (agenttui.CommandResult, error) {
	session.owner.stateMutex.Lock()
	run, active := session.owner.activeRun, session.owner.hasActiveRun
	session.owner.stateMutex.Unlock()
	if !active {
		return agenttui.CommandResult{}, errors.New("cancel run: no run is active")
	}
	operation, err := session.owner.newOperationID()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("cancel run: %w", err)
	}
	request, err := client.NewCancelRequest(run, operation, "cancelled from TUI")
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("cancel run: %w", err)
	}
	operationContext, cancel := session.owner.operationContext(ctx)
	result, err := session.owner.currentClient().Cancel(operationContext, request)
	cancel()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("cancel run: %w", err)
	}
	if result.AlreadyTerminal() {
		session.owner.clearActiveRun(run)
		return (commandResultFactory{}).new("run " + run.ID() + " was already terminal")
	}
	return (commandResultFactory{}).new("cancellation requested for run " + run.ID())
}

func (session *sessionCommands) performRespond(
	ctx context.Context,
	value agenttui.Text,
) (agenttui.CommandResult, error) {
	pending, found := session.owner.currentInteraction()
	if !found {
		return agenttui.CommandResult{}, errors.New("respond to interaction: no interaction is pending")
	}
	structured, err := client.NewStructuredText(value.String())
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	response, err := client.NewInteractionResponse(pending.ID(), structured)
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	operation, err := session.owner.newOperationID()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	request, err := client.NewRespondRequest(pending.Run(), operation, response)
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	operationContext, cancel := session.owner.operationContext(ctx)
	result, err := session.owner.currentClient().Respond(operationContext, request)
	cancel()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	if result.DuplicateOperation() {
		return (commandResultFactory{}).new("interaction response was already accepted")
	}
	return (commandResultFactory{}).new("interaction response accepted")
}
