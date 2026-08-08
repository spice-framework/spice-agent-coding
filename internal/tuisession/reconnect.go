package tuisession

import (
	"errors"
	"fmt"
	"io"

	"github.com/spice-framework/spice-agent/client"
)

func (session *Session) retryObservation(
	kind string,
	after, generation uint64,
	err error,
	announced *bool,
) bool {
	if !session.canRetryObservation(err) {
		return false
	}
	// A clean EOF is a bounded page boundary, not evidence that ownership or
	// transport was lost. Reopen from the exact cursor without fencing the
	// healthy session.
	if errors.Is(err, io.EOF) {
		return session.waitToReconnect()
	}
	if kind == "event" && !*announced {
		if publishErr := session.publishActivity(fmt.Sprintf(
			"event stream reconnecting after sequence %d (replay limit %d)", after, session.config.ReplayLimit,
		)); publishErr != nil {
			session.recordFailure(fmt.Errorf("publish event reconnecting state: %w", publishErr))
			return false
		}
		*announced = true
	}
	if !session.waitToReconnect() {
		return false
	}
	if restoreErr := session.restoreConnection(generation); restoreErr != nil {
		session.recordFailure(fmt.Errorf("restore daemon connection: %w", restoreErr))
		return false
	}
	return true
}

func (session *Session) restoreConnection(expectedGeneration uint64) error {
	session.reconnectMutex.Lock()
	defer session.reconnectMutex.Unlock()

	current, generation := session.currentClientGeneration()
	if generation != expectedGeneration {
		return nil
	}
	if current == nil {
		return errors.New("current client session is unavailable")
	}
	connection := current.Connection()
	claim, err := client.NewReconnectClaim(connection.ClientID(), connection.OwnershipEpoch())
	if err != nil {
		return fmt.Errorf("construct reconnect claim: %w", err)
	}
	reconnectRequest, err := session.newReconnectRequest(claim)
	if err != nil {
		return err
	}
	replacement, fresh, err := session.acquireRestoredClient(reconnectRequest)
	if err != nil {
		return err
	}
	if replacement == nil {
		return errors.New("connector returned a nil restored client session")
	}
	if err = session.validateConnection(replacement.Connection()); err != nil {
		return errors.Join(err, replacement.Close())
	}

	session.clientMutex.Lock()
	if session.clientGeneration != expectedGeneration || session.clientSession != current {
		session.clientMutex.Unlock()
		return replacement.Close()
	}
	session.clientSession = replacement
	session.clientGeneration++
	session.clientMutex.Unlock()
	if fresh {
		session.resetDaemonInteractionState()
	}
	closeErr := current.Close()
	message := "daemon connection restored"
	if fresh {
		message = "daemon connection restored with a fresh session"
	}
	return errors.Join(session.publishActivity(message), closeErr)
}

func (session *Session) acquireRestoredClient(
	reconnectRequest client.InitializeRequest,
) (client.Session, bool, error) {
	replacement, err := session.initializeForRestore(reconnectRequest, false)
	if err == nil || !session.reconnectSessionUnavailable(err) {
		return replacement, false, err
	}
	if session.activeRunExists() {
		return nil, false, errors.New("daemon session was lost during an active run; durable process-loss recovery is not available")
	}
	freshRequest, err := session.newFreshRequest()
	if err != nil {
		return nil, false, err
	}
	replacement, err = session.initializeForRestore(freshRequest, true)
	return replacement, err == nil, err
}

func (session *Session) newReconnectRequest(claim client.ReconnectClaim) (client.InitializeRequest, error) {
	base := session.config.InitializeRequest
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

func (session *Session) newFreshRequest() (client.InitializeRequest, error) {
	base := session.config.InitializeRequest
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

func (session *Session) initializeForRestore(
	request client.InitializeRequest,
	retryUnavailable bool,
) (client.Session, error) {
	for {
		replacement, err := session.connector.Initialize(session.context(), request)
		if err == nil {
			return replacement, nil
		}
		if session.closedForRestore() {
			return nil, client.ErrClosed
		}
		if !session.retryInitialization(err, retryUnavailable) {
			return nil, err
		}
		if !session.waitToReconnect() {
			return nil, client.ErrClosed
		}
	}
}

func (session *Session) retryInitialization(err error, retryUnavailable bool) bool {
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

func (session *Session) reconnectSessionUnavailable(err error) bool {
	status, ok := errors.AsType[*client.StatusError](err)
	return ok && status != nil && status.Code() == client.ErrorUnavailable && status.Retryable()
}

func (session *Session) activeRunExists() bool {
	session.stateMutex.Lock()
	defer session.stateMutex.Unlock()
	return session.hasActiveRun
}

func (session *Session) resetDaemonInteractionState() {
	session.stateMutex.Lock()
	session.pending = make(map[interactionKey]client.PendingInteraction)
	session.interactionRevision = 0
	session.stateMutex.Unlock()
}

func (session *Session) closedForRestore() bool {
	select {
	case <-session.closed:
		return true
	default:
		return false
	}
}
