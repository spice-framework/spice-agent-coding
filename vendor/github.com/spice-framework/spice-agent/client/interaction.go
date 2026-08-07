package client

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// PendingInteraction is one immutable UI-neutral prompt with a portable
// structured schema.
type PendingInteraction struct {
	run    RunRef
	id     string
	kind   string
	prompt string
	schema StructuredValue
}

func NewPendingInteraction(run RunRef, id, kind, prompt string, schema StructuredValue) (PendingInteraction, error) {
	if err := run.Validate(); err != nil {
		return PendingInteraction{}, err
	}
	if err := token("interaction ID", id, 128); err != nil {
		return PendingInteraction{}, err
	}
	if err := token("interaction kind", kind, 128); err != nil {
		return PendingInteraction{}, err
	}
	if err := boundedText("interaction prompt", prompt, 512<<10, false); err != nil {
		return PendingInteraction{}, err
	}
	if prompt != strings.TrimSpace(prompt) {
		return PendingInteraction{}, errors.New("interaction prompt must not have surrounding whitespace")
	}
	if err := schema.Validate(); err != nil {
		return PendingInteraction{}, fmt.Errorf("interaction schema: %w", err)
	}
	return PendingInteraction{run: run, id: id, kind: kind, prompt: prompt, schema: schema}, nil
}

func (pending PendingInteraction) Run() RunRef             { return pending.run }
func (pending PendingInteraction) ID() string              { return pending.id }
func (pending PendingInteraction) Kind() string            { return pending.kind }
func (pending PendingInteraction) Prompt() string          { return pending.prompt }
func (pending PendingInteraction) Schema() StructuredValue { return pending.schema }

func (pending PendingInteraction) Validate() error {
	_, err := NewPendingInteraction(pending.run, pending.id, pending.kind, pending.prompt, pending.schema)
	return err
}

// InteractionResponse is one bounded structured response to a pending prompt.
type InteractionResponse struct {
	id    string
	value StructuredValue
}

func NewInteractionResponse(id string, value StructuredValue) (InteractionResponse, error) {
	if err := token("interaction response ID", id, 128); err != nil {
		return InteractionResponse{}, err
	}
	if err := value.Validate(); err != nil {
		return InteractionResponse{}, fmt.Errorf("interaction response: %w", err)
	}
	return InteractionResponse{id: id, value: value}, nil
}

func (response InteractionResponse) ID() string             { return response.id }
func (response InteractionResponse) Value() StructuredValue { return response.value }

func (response InteractionResponse) Validate() error {
	_, err := NewInteractionResponse(response.id, response.value)
	return err
}

// RespondRequest is one idempotent response mutation correlated to a run.
type RespondRequest struct {
	run       RunRef
	operation OperationID
	response  InteractionResponse
}

func NewRespondRequest(run RunRef, operation OperationID, response InteractionResponse) (RespondRequest, error) {
	if err := run.Validate(); err != nil {
		return RespondRequest{}, err
	}
	if err := operation.Validate(); err != nil {
		return RespondRequest{}, err
	}
	if err := response.Validate(); err != nil {
		return RespondRequest{}, err
	}
	return RespondRequest{run: run, operation: operation, response: response}, nil
}

func (request RespondRequest) Run() RunRef                   { return request.run }
func (request RespondRequest) Operation() OperationID        { return request.operation }
func (request RespondRequest) Response() InteractionResponse { return request.response }

// RespondResult reports whether an idempotent response operation was accepted.
type RespondResult struct {
	accepted  bool
	duplicate bool
}

func NewRespondResult(accepted, duplicate bool) (RespondResult, error) {
	if !accepted && !duplicate {
		return RespondResult{}, errors.New("interaction response was neither accepted nor duplicated")
	}
	return RespondResult{accepted: accepted, duplicate: duplicate}, nil
}

func (result RespondResult) Accepted() bool           { return result.accepted }
func (result RespondResult) DuplicateOperation() bool { return result.duplicate }

// InteractionUpdateKind identifies the active stream payload.
type InteractionUpdateKind string

const (
	InteractionSnapshot InteractionUpdateKind = "snapshot"
	InteractionOpened   InteractionUpdateKind = "opened"
	InteractionClosed   InteractionUpdateKind = "closed"
)

// InteractionUpdate is a closed immutable union. Every stream begins with a
// complete snapshot; subsequent opened/closed revisions are contiguous.
type InteractionUpdate struct {
	kind     InteractionUpdateKind
	revision uint64
	pending  []PendingInteraction
	item     PendingInteraction
}

func NewInteractionSnapshot(revision uint64, pending []PendingInteraction, limits Limits) (InteractionUpdate, error) {
	if err := limits.Validate(); err != nil {
		return InteractionUpdate{}, fmt.Errorf("interaction snapshot limits: %w", err)
	}
	if uint64(len(pending)) > uint64(limits.CollectionItems()) {
		return InteractionUpdate{}, fmt.Errorf("pending interaction count exceeds %d", limits.CollectionItems())
	}
	values := slices.Clone(pending)
	for index := range values {
		if err := values[index].Validate(); err != nil {
			return InteractionUpdate{}, fmt.Errorf("pending interaction %d: %w", index, err)
		}
	}
	slices.SortFunc(values, func(left, right PendingInteraction) int {
		if left.run.id == right.run.id {
			return compareString(left.id, right.id)
		}
		return compareString(left.run.id, right.run.id)
	})
	for index := 1; index < len(values); index++ {
		if values[index-1].run == values[index].run && values[index-1].id == values[index].id {
			return InteractionUpdate{}, errors.New("pending interactions must be unique by run and interaction ID")
		}
	}
	return InteractionUpdate{kind: InteractionSnapshot, revision: revision, pending: values}, nil
}

func NewInteractionChange(kind InteractionUpdateKind, revision uint64, item PendingInteraction) (InteractionUpdate, error) {
	if kind != InteractionOpened && kind != InteractionClosed {
		return InteractionUpdate{}, fmt.Errorf("interaction change kind %q is unsupported", kind)
	}
	if revision == 0 {
		return InteractionUpdate{}, errors.New("interaction change revision must be positive")
	}
	if err := item.Validate(); err != nil {
		return InteractionUpdate{}, err
	}
	return InteractionUpdate{kind: kind, revision: revision, item: item}, nil
}

func (update InteractionUpdate) Kind() InteractionUpdateKind { return update.kind }
func (update InteractionUpdate) Revision() uint64            { return update.revision }
func (update InteractionUpdate) Snapshot() ([]PendingInteraction, bool) {
	if update.kind != InteractionSnapshot {
		return nil, false
	}
	return slices.Clone(update.pending), true
}

func (update InteractionUpdate) Item() (PendingInteraction, bool) {
	return update.item, update.kind == InteractionOpened || update.kind == InteractionClosed
}

func (update InteractionUpdate) validate() error {
	switch update.kind {
	case InteractionSnapshot:
		for index := range update.pending {
			if err := update.pending[index].Validate(); err != nil {
				return fmt.Errorf("pending interaction %d: %w", index, err)
			}
		}
		return nil
	case InteractionOpened, InteractionClosed:
		if update.revision == 0 {
			return errors.New("interaction change revision must be positive")
		}
		return update.item.Validate()
	default:
		return fmt.Errorf("interaction update kind %q is unsupported", update.kind)
	}
}

// InteractionStreamOptions chooses whether to tail future changes after the
// mandatory complete snapshot and captured control. Reconnect always starts
// from a fresh snapshot, so interaction streams have no historical replay
// cursor or limit.
type InteractionStreamOptions struct {
	tail bool
}

func NewInteractionStreamOptions(tail bool) InteractionStreamOptions {
	return InteractionStreamOptions{tail: tail}
}

func (options InteractionStreamOptions) Tail() bool { return options.tail }

func (InteractionStreamOptions) Validate() error { return nil }
