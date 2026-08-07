package client

import (
	"context"
	"errors"
)

// ErrClosed is returned by future operations after a local Session or stream
// handle closes. Close must also unblock active stream reads with ErrClosed.
var ErrClosed = errors.New("client handle is closed")

// Connector initializes one authenticated transport session. Endpoint
// discovery and authentication material belong to the implementation and are
// intentionally absent from InitializeRequest. The initialization context owns
// negotiation only and never becomes the Session lifetime.
type Connector interface {
	Initialize(context.Context, InitializeRequest) (Session, error)
}

// EventStream is a local handle to a replay/tail operation. Next is single-reader
// and must honor its caller-owned context. Close is local, nonblocking,
// idempotent, may race with Next, unblocks an active Next, and causes future Next
// calls to return ErrClosed.
type EventStream interface {
	Next(context.Context) (EventFrame, error)
	Close() error
}

// InteractionStream yields a complete pending snapshot first and then strictly
// revision-contiguous changes and explicit controls. Next is single-reader and
// must honor its caller-owned context. Close is local, nonblocking, idempotent,
// may race with Next, unblocks an active Next, and causes future Next calls to
// return ErrClosed.
type InteractionStream interface {
	Next(context.Context) (InteractionFrame, error)
	Close() error
}

// Session is one negotiated stable-client ownership epoch. All methods are safe
// for concurrent use, including simultaneous streams, mutations, and health
// calls. Implementations must not retry mutations automatically: an unknown
// commit outcome is returned as UncertainOperationError for explicit caller
// recovery. Close is local, nonblocking, idempotent, may race with any method,
// unblocks active streams, and makes future context-taking operations return
// ErrClosed. Connection remains an immutable local snapshot callable after
// Close.
type Session interface {
	Connection() Connection
	Start(context.Context, StartRequest) (StartResult, error)
	Events(context.Context, Cursor, EventStreamOptions) (EventStream, error)
	Interactions(context.Context, InteractionStreamOptions) (InteractionStream, error)
	Cancel(context.Context, CancelRequest) (CancelResult, error)
	Respond(context.Context, RespondRequest) (RespondResult, error)
	Suspend(context.Context, RunMutation) (SuspendResult, error)
	Resume(context.Context, RunMutation) (ResumeResult, error)
	Export(context.Context, RunRef) (Snapshot, error)
	Import(context.Context, ImportRequest) (ImportResult, error)
	Health(context.Context) (Health, error)
	Close() error
}
