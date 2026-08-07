package agenttui

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Session is the UI-neutral connection used by a terminal shell. Implementors
// own transport, reconnect, replay, and daemon discovery policy. Receive must
// return updates in strictly increasing positive revision order for the
// lifetime of one Session. The shell never retries Receive or Perform.
// Implementations must permit one blocking Receive, one ordinary Perform, and
// one cancel-run Perform to execute concurrently; the shell serializes calls
// within each of those lanes.
type Session interface {
	Receive(context.Context) (SessionUpdate, error)
	Perform(context.Context, Intent) (CommandResult, error)
}

// SessionSnapshot is one immutable, bounded server-owned presentation
// snapshot. Revision participates in the same sequence as incremental session
// updates, so a reconnect can replace all visible state without a second clock.
type SessionSnapshot struct {
	revision      uint64
	workspace     WorkspaceState
	status        StatusState
	activity      []Text
	promptHistory []Text
}

// NewSessionSnapshot constructs a complete immutable session snapshot.
func NewSessionSnapshot(
	revision uint64,
	workspace WorkspaceState,
	status StatusState,
	activity []Text,
	promptHistory []Text,
) (SessionSnapshot, error) {
	snapshot := SessionSnapshot{
		revision:      revision,
		workspace:     workspace,
		status:        status,
		activity:      slices.Clone(activity),
		promptHistory: slices.Clone(promptHistory),
	}
	return snapshot, snapshot.Validate()
}

// Revision returns the positive global session revision.
func (snapshot SessionSnapshot) Revision() uint64 { return snapshot.revision }

// Workspace returns the immutable workspace state.
func (snapshot SessionSnapshot) Workspace() WorkspaceState { return snapshot.workspace }

// Status returns the immutable status state.
func (snapshot SessionSnapshot) Status() StatusState { return snapshot.status }

// Activity returns a defensive copy of the bounded activity window.
func (snapshot SessionSnapshot) Activity() []Text { return slices.Clone(snapshot.activity) }

// PromptHistory returns a defensive copy of the bounded prompt history.
func (snapshot SessionSnapshot) PromptHistory() []Text { return slices.Clone(snapshot.promptHistory) }

// Validate reports whether the snapshot is complete, terminal-safe, and
// bounded. It does not compare revisions; monotonicity belongs to the Session
// stream and the receiving shell.
func (snapshot SessionSnapshot) Validate() error {
	if snapshot.revision == 0 {
		return errors.New("session snapshot revision must be positive")
	}
	empty, err := NewEditor("")
	if err != nil {
		return fmt.Errorf("construct session snapshot validation editor: %w", err)
	}
	if _, err := NewViewData(snapshot.workspace, snapshot.status, empty, snapshot.activity); err != nil {
		return fmt.Errorf("session snapshot: %w", err)
	}
	return validatePromptHistory(snapshot.promptHistory)
}

// SessionUpdateKind identifies the only supported update payload.
type SessionUpdateKind string

const (
	// SessionUpdateSnapshot replaces the complete server-owned presentation
	// state after initialization or reconnect.
	SessionUpdateSnapshot SessionUpdateKind = "snapshot"
	// SessionUpdateActivity appends one activity item.
	SessionUpdateActivity SessionUpdateKind = "activity"
	// SessionUpdatePromptHistory replaces the bounded prompt history.
	SessionUpdatePromptHistory SessionUpdateKind = "prompt-history"
)

// SessionUpdate is an immutable tagged union. Exactly one accessor corresponds
// to Kind; callers cannot construct unvalidated variants through public fields.
type SessionUpdate struct {
	kind     SessionUpdateKind
	revision uint64
	snapshot SessionSnapshot
	activity Text
	history  []Text
}

// NewSnapshotUpdate constructs a complete-state update.
func NewSnapshotUpdate(snapshot SessionSnapshot) (SessionUpdate, error) {
	update := SessionUpdate{
		kind:     SessionUpdateSnapshot,
		revision: snapshot.Revision(),
		snapshot: cloneSessionSnapshot(snapshot),
	}
	return update, update.Validate()
}

// NewActivityUpdate constructs one incremental activity update.
func NewActivityUpdate(revision uint64, activity Text) (SessionUpdate, error) {
	update := SessionUpdate{kind: SessionUpdateActivity, revision: revision, activity: activity}
	return update, update.Validate()
}

// NewPromptHistoryUpdate constructs one complete prompt-history update.
func NewPromptHistoryUpdate(revision uint64, history []Text) (SessionUpdate, error) {
	update := SessionUpdate{
		kind: SessionUpdatePromptHistory, revision: revision,
		history: slices.Clone(history),
	}
	return update, update.Validate()
}

// Kind returns the tagged payload kind.
func (update SessionUpdate) Kind() SessionUpdateKind { return update.kind }

// Revision returns the positive global session revision.
func (update SessionUpdate) Revision() uint64 { return update.revision }

// Snapshot returns a defensive copy of the snapshot payload when present.
func (update SessionUpdate) Snapshot() (SessionSnapshot, bool) {
	if update.kind != SessionUpdateSnapshot {
		return SessionSnapshot{}, false
	}
	return cloneSessionSnapshot(update.snapshot), true
}

// Activity returns the activity payload when present.
func (update SessionUpdate) Activity() (Text, bool) {
	return update.activity, update.kind == SessionUpdateActivity
}

// PromptHistory returns a defensive copy of the prompt-history payload when
// present.
func (update SessionUpdate) PromptHistory() ([]Text, bool) {
	if update.kind != SessionUpdatePromptHistory {
		return nil, false
	}
	return slices.Clone(update.history), true
}

// Validate reports whether the tagged update is initialized and bounded.
func (update SessionUpdate) Validate() error {
	if update.revision == 0 {
		return errors.New("session update revision must be positive")
	}
	switch update.kind {
	case SessionUpdateSnapshot:
		if update.snapshot.Revision() != update.revision {
			return errors.New("session snapshot and update revisions must match")
		}
		return update.snapshot.Validate()
	case SessionUpdateActivity:
		if err := update.activity.Validate(); err != nil {
			return fmt.Errorf("session activity: %w", err)
		}
		return nil
	case SessionUpdatePromptHistory:
		return validatePromptHistory(update.history)
	default:
		return fmt.Errorf("unsupported session update kind %q", update.kind)
	}
}

func cloneSessionSnapshot(snapshot SessionSnapshot) SessionSnapshot {
	snapshot.activity = slices.Clone(snapshot.activity)
	snapshot.promptHistory = slices.Clone(snapshot.promptHistory)
	return snapshot
}

func validatePromptHistory(history []Text) error {
	if len(history) > MaximumPromptHistoryItems {
		return fmt.Errorf("prompt history exceeds %d items", MaximumPromptHistoryItems)
	}
	for index, entry := range history {
		if _, err := NewEditor(entry.String()); err != nil {
			return fmt.Errorf("prompt history item %d: %w", index, err)
		}
	}
	return nil
}
