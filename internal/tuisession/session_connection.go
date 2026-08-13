package tuisession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent/client"
)

type sessionConnection struct {
	owner            *Session
	initializeOnce   sync.Once
	initializeDone   chan struct{}
	clientMutex      sync.RWMutex
	clientSession    client.Session
	clientGeneration uint64
	initializeErr    error
	reconnectMutex   sync.Mutex
	identifierMutex  sync.Mutex
}

func (session *sessionConnection) ensureInitialized(ctx context.Context) error {
	select {
	case <-session.owner.closed:
		return client.ErrClosed
	default:
	}
	session.owner.initializeOnce.Do(func() { go session.owner.initialize() })
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-session.owner.closed:
		return client.ErrClosed
	case <-session.owner.initializeDone:
		session.owner.clientMutex.RLock()
		err := session.owner.initializeErr
		session.owner.clientMutex.RUnlock()
		if err != nil {
			return fmt.Errorf("initialize TUI session: %w", err)
		}
		return nil
	}
}

func (session *sessionConnection) initialize() {
	defer close(session.owner.initializeDone)
	clientSession, err := session.owner.connector.Initialize(session.owner.context(), session.owner.config.InitializeRequest)
	if err != nil {
		session.owner.setInitializeError(err)
		return
	}
	if clientSession == nil {
		session.owner.setInitializeError(errors.New("connector returned a nil client session"))
		return
	}
	if err = session.owner.validateConnection(clientSession.Connection()); err != nil {
		session.owner.setInitializeError(err)
		return
	}
	select {
	case <-session.owner.closed:
		closeErr := clientSession.Close()
		session.owner.clientMutex.Lock()
		session.owner.initializeErr = errors.Join(client.ErrClosed, closeErr)
		session.owner.clientMutex.Unlock()
		return
	default:
	}
	session.owner.clientMutex.Lock()
	session.owner.clientSession = clientSession
	session.owner.clientGeneration = 1
	session.owner.clientMutex.Unlock()
	if err = session.owner.publishSnapshot(); err != nil {
		session.owner.clientMutex.Lock()
		session.owner.initializeErr = err
		session.owner.clientMutex.Unlock()
		closeErr := clientSession.Close()
		if closeErr != nil {
			session.owner.recordFailure(fmt.Errorf("close client after initial snapshot failure: %w", closeErr))
		}
		return
	}
	session.owner.startWorker(session.owner.observeInteractions)
}

func (session *sessionConnection) setInitializeError(err error) {
	session.owner.clientMutex.Lock()
	session.owner.initializeErr = err
	session.owner.clientMutex.Unlock()
}

func (session *sessionConnection) validateConnection(connection client.Connection) error {
	if err := connection.Validate(); err != nil {
		return fmt.Errorf("negotiated connection: %w", err)
	}
	if session.owner.config.ReplayLimit > connection.Limits().ReplayEvents() {
		return fmt.Errorf(
			"event replay limit %d exceeds negotiated limit %d",
			session.owner.config.ReplayLimit,
			connection.Limits().ReplayEvents(),
		)
	}
	if _, found := connection.Catalog().Find(session.owner.config.Definition); !found {
		return fmt.Errorf(
			"definition %s@%s is not advertised by the negotiated server",
			session.owner.config.Definition.ID(),
			session.owner.config.Definition.Revision(),
		)
	}
	return nil
}

func (session *sessionConnection) currentClient() client.Session {
	current, _ := session.owner.currentClientGeneration()
	return current
}

func (session *sessionConnection) currentClientGeneration() (client.Session, uint64) {
	session.owner.clientMutex.RLock()
	defer session.owner.clientMutex.RUnlock()
	return session.owner.clientSession, session.owner.clientGeneration
}

func (session *sessionConnection) context() context.Context {
	return closeContext{done: session.owner.closed}
}

func (session *sessionConnection) newOperationID() (client.OperationID, error) {
	value, err := session.owner.nextIdentifier()
	if err != nil {
		return client.OperationID{}, fmt.Errorf("create operation ID: %w", err)
	}
	return client.NewOperationID(value)
}

func (session *sessionConnection) nextIdentifier() (string, error) {
	session.owner.identifierMutex.Lock()
	defer session.owner.identifierMutex.Unlock()
	value, err := session.owner.identifiers.Next()
	if err != nil {
		return "", err
	}
	if _, err = client.NewOperationID(value); err != nil {
		return "", fmt.Errorf("identifier source returned an invalid value: %w", err)
	}
	return value, nil
}

func (session *sessionConnection) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	operationContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.owner.context(), cancel)
	return operationContext, func() {
		stop()
		cancel()
	}
}

func (session *sessionConnection) canRetryObservation(err error) bool {
	if err == nil {
		return false
	}
	select {
	case <-session.owner.closed:
		return false
	default:
	}
	gap, isGap := errors.AsType[*client.CursorGapError](err)
	if isGap && gap != nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, client.ErrClosed) {
		return true
	}
	if status, ok := errors.AsType[*client.StatusError](err); ok && status != nil &&
		status.Code() == client.ErrorUnauthenticated {
		return true
	}
	var failure client.StatusFailure
	if errors.As(err, &failure) && failure.Retryable() {
		return true
	}
	return false
}

func (session *sessionConnection) waitToReconnect() bool {
	return session.owner.waitToReconnectContext(session.owner.context())
}

func (session *sessionConnection) waitToReconnectContext(ctx context.Context) bool {
	if session.owner.config.ReconnectDelay == 0 {
		select {
		case <-ctx.Done():
			return false
		case <-session.owner.closed:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(session.owner.config.ReconnectDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-session.owner.closed:
		return false
	case <-timer.C:
		return true
	}
}

func (session *sessionConnection) retryObservation(
	kind string,
	after, generation uint64,
	err error,
	announced *bool,
) bool {
	if !session.owner.canRetryObservation(err) {
		return false
	}
	// A clean EOF is a bounded page boundary, not evidence that ownership or
	// transport was lost. Reopen from the exact cursor without fencing the
	// healthy session.owner.
	if errors.Is(err, io.EOF) {
		return session.owner.waitToReconnect()
	}
	if kind == "event" && !*announced {
		if publishErr := session.owner.publishActivity(fmt.Sprintf(
			"event stream reconnecting after sequence %d (replay limit %d)", after, session.owner.config.ReplayLimit,
		)); publishErr != nil {
			session.owner.recordFailure(fmt.Errorf("publish event reconnecting state: %w", publishErr))
			return false
		}
		*announced = true
	}
	if !session.owner.waitToReconnect() {
		return false
	}
	if restoreErr := session.owner.restoreConnection(generation); restoreErr != nil {
		session.owner.recordFailure(fmt.Errorf("restore daemon connection: %w", restoreErr))
		return false
	}
	return true
}

func (session *sessionConnection) restoreConnection(expectedGeneration uint64) error {
	session.owner.reconnectMutex.Lock()
	defer session.owner.reconnectMutex.Unlock()
	_, err := session.owner.restoreConnectionLocked(session.owner.context(), expectedGeneration)
	return err
}

type restoredConnection struct {
	session    client.Session
	generation uint64
	fresh      bool
}

func (session *sessionConnection) restoreConnectionLocked(
	ctx context.Context,
	expectedGeneration uint64,
) (restoredConnection, error) {
	current, generation := session.owner.currentClientGeneration()
	if generation != expectedGeneration {
		return restoredConnection{session: current, generation: generation}, nil
	}
	if current == nil {
		return restoredConnection{}, errors.New("current client session is unavailable")
	}
	connection := current.Connection()
	claim, err := client.NewReconnectClaim(connection.ClientID(), connection.OwnershipEpoch())
	if err != nil {
		return restoredConnection{}, fmt.Errorf("construct reconnect claim: %w", err)
	}
	reconnectRequest, err := session.owner.newReconnectRequest(claim)
	if err != nil {
		return restoredConnection{}, err
	}
	replacement, fresh, err := session.owner.acquireRestoredClient(ctx, reconnectRequest)
	if err != nil {
		return restoredConnection{}, err
	}
	if replacement == nil {
		return restoredConnection{}, errors.New("connector returned a nil restored client session")
	}
	if err = session.owner.validateConnection(replacement.Connection()); err != nil {
		return restoredConnection{}, errors.Join(err, replacement.Close())
	}

	session.owner.clientMutex.Lock()
	if session.owner.clientGeneration != expectedGeneration || session.owner.clientSession != current {
		active := session.owner.clientSession
		activeGeneration := session.owner.clientGeneration
		session.owner.clientMutex.Unlock()
		return restoredConnection{
			session: active, generation: activeGeneration,
		}, replacement.Close()
	}
	session.owner.clientSession = replacement
	session.owner.clientGeneration++
	replacementGeneration := session.owner.clientGeneration
	session.owner.clientMutex.Unlock()
	if fresh {
		session.owner.resetDaemonInteractionState()
	}
	closeErr := current.Close()
	message := "daemon connection restored"
	if fresh {
		message = "daemon connection restored with a fresh session"
	}
	err = errors.Join(session.owner.publishActivity(message), closeErr)
	return restoredConnection{
		session: replacement, generation: replacementGeneration, fresh: fresh,
	}, err
}

func (session *sessionConnection) acquireRestoredClient(
	ctx context.Context,
	reconnectRequest client.InitializeRequest,
) (client.Session, bool, error) {
	replacement, err := session.owner.initializeForRestore(ctx, reconnectRequest, false)
	if err == nil || !session.owner.reconnectSessionUnavailable(err) {
		return replacement, false, err
	}
	if session.owner.activeRunExists() {
		return nil, false, errors.New("daemon session was lost during an active run; durable process-loss recovery is not available")
	}
	freshRequest, err := session.owner.newFreshRequest()
	if err != nil {
		return nil, false, err
	}
	replacement, err = session.owner.initializeForRestore(ctx, freshRequest, true)
	return replacement, err == nil, err
}

func (session *sessionConnection) newReconnectRequest(claim client.ReconnectClaim) (client.InitializeRequest, error) {
	base := session.owner.config.InitializeRequest
	attempt, err := client.NewInitializationAttemptID()
	if err != nil {
		return client.InitializeRequest{}, fmt.Errorf("create reconnect attempt: %w", err)
	}
	request, err := client.NewReconnectRequestWithAttempt(
		base.Protocol(), base.Client(), base.SupportedCapabilities(), base.RequiredCapabilities(),
		base.RequestedLimits(), claim, attempt,
	)
	if err != nil {
		return client.InitializeRequest{}, fmt.Errorf("construct reconnect request: %w", err)
	}
	return request, nil
}

func (session *sessionConnection) newFreshRequest() (client.InitializeRequest, error) {
	base := session.owner.config.InitializeRequest
	attempt, err := client.NewInitializationAttemptID()
	if err != nil {
		return client.InitializeRequest{}, fmt.Errorf("create replacement initialization attempt: %w", err)
	}
	request, err := client.NewInitializeRequestWithAttempt(
		base.Protocol(), base.Client(), base.SupportedCapabilities(), base.RequiredCapabilities(),
		base.RequestedLimits(), attempt,
	)
	if err != nil {
		return client.InitializeRequest{}, fmt.Errorf("construct replacement initialization request: %w", err)
	}
	return request, nil
}

func (session *sessionConnection) initializeForRestore(
	ctx context.Context,
	request client.InitializeRequest,
	retryUnavailable bool,
) (client.Session, error) {
	for {
		replacement, err := session.owner.connector.Initialize(ctx, request)
		if err == nil {
			return replacement, nil
		}
		if session.owner.closedForRestore() {
			return nil, client.ErrClosed
		}
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		if !session.owner.retryInitialization(err, retryUnavailable) {
			return nil, err
		}
		if !session.owner.waitToReconnectContext(ctx) {
			if cause := context.Cause(ctx); cause != nil {
				return nil, cause
			}
			return nil, client.ErrClosed
		}
	}
}

func (session *sessionConnection) retryInitialization(err error, retryUnavailable bool) bool {
	replayError, replay := errors.AsType[*client.InitializationReplayError](err)
	if replay && replayError != nil {
		return true
	}
	if status, ok := errors.AsType[*client.StatusError](err); ok && status != nil {
		if status.Code() == client.ErrorUnavailable {
			return retryUnavailable && status.Retryable()
		}
		return status.Code() == client.ErrorUnauthenticated && status.Retryable()
	}
	if failure, ok := errors.AsType[client.StatusFailure](err); ok && failure != nil {
		return failure.Retryable()
	}
	// Protected endpoint discovery may temporarily report absence between an
	// old process exiting and its replacement publishing fresh metadata.
	return true
}

func (session *sessionConnection) reconnectSessionUnavailable(err error) bool {
	status, ok := errors.AsType[*client.StatusError](err)
	return ok && status != nil && status.Code() == client.ErrorUnavailable && status.Retryable()
}

func (session *sessionConnection) rejectedStartCanRetry(ctx context.Context, err error) bool {
	if context.Cause(ctx) != nil || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, uncertain := errors.AsType[*client.UncertainOperationError](err); uncertain {
		return false
	}
	status, ok := errors.AsType[*client.StatusError](err)
	if !ok || status == nil || status.Code() != client.ErrorUnavailable || !status.Retryable() {
		return false
	}
	_, correlated := status.Operation()
	return !correlated
}

func (session *sessionConnection) activeRunExists() bool {
	session.owner.stateMutex.Lock()
	defer session.owner.stateMutex.Unlock()
	return session.owner.hasActiveRun
}

func (session *sessionConnection) resetDaemonInteractionState() {
	session.owner.stateMutex.Lock()
	session.owner.pending = make(map[interactionKey]client.PendingInteraction)
	session.owner.interactionRevision = 0
	session.owner.stateMutex.Unlock()
}

func (session *sessionConnection) closedForRestore() bool {
	select {
	case <-session.owner.closed:
		return true
	default:
		return false
	}
}
