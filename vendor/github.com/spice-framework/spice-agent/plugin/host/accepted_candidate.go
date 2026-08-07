package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/tool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

type lifecyclePhase string

const (
	lifecyclePhaseAccept      lifecyclePhase = "accept"
	lifecyclePhaseHealth      lifecyclePhase = "health"
	lifecyclePhaseDrain       lifecyclePhase = "drain"
	lifecyclePhaseShutdown    lifecyclePhase = "shutdown"
	lifecyclePhaseContainment lifecyclePhase = "containment"
)

// acceptedCandidate is the sole lifecycle owner of an authenticated
// candidate. Drain and Shutdown are each attempted at most once; explicit
// later close calls may retry only containment when Wait has not yet proved
// that process-owned resources are safe to release.
type acceptedCandidate struct {
	operation sync.Mutex
	healthMu  sync.Mutex

	candidate   *candidate
	session     *remoteSession
	toolSet     map[string]tool.Tool
	processDone <-chan struct{}
	stdout      *readinessSink
	connection  *grpc.ClientConn

	admissionDrained  bool
	drainAttempted    bool
	drainResult       error
	shutdownAttempted bool
	shutdownResult    error
	contained         bool
	closeResult       error

	unhealthy     chan struct{}
	unhealthyOnce sync.Once
	healthResult  error
	stopObserving context.CancelFunc
}

func newAcceptedCandidate(value *candidate) (*acceptedCandidate, error) {
	if value == nil {
		return nil, lifecycleFailure(lifecyclePhaseAccept, errors.New("candidate is required"))
	}

	value.mu.Lock()
	if value.closed || value.client == nil || value.connection == nil || value.process == nil ||
		value.endpoint == nil || value.lease == nil || value.stdout == nil {
		value.mu.Unlock()
		return nil, lifecycleFailure(lifecyclePhaseAccept, errors.New("candidate ownership is incomplete"))
	}
	executable := value.executable.Clone()
	client := value.client
	connection := value.connection
	ownedProcess := value.process
	stdout := value.stdout
	sessionID := slices.Clone(value.session)
	limits := cloneProto(value.limits)
	value.mu.Unlock()

	if err := executable.Validate(); err != nil {
		return nil, lifecycleFailure(lifecyclePhaseAccept, errors.New("candidate executable is invalid"))
	}
	if stdout.err() != nil {
		return nil, lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate process is unavailable"))
	}
	select {
	case <-ownedProcess.Done():
		return nil, lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate process is unavailable"))
	default:
	}
	if state := connection.GetState(); state == connectivity.Shutdown || state == connectivity.TransientFailure {
		return nil, lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate connection is unavailable"))
	}
	session, err := newRemoteSession(client, sessionID, limits, executable.CallTimeout())
	if err != nil {
		return nil, lifecycleFailure(lifecyclePhaseAccept, errors.New("candidate session is invalid"))
	}
	implementations := make(map[string]tool.Tool)
	for _, definition := range value.catalogSnapshot().Definitions() {
		implementation, definitionErr := newRemoteTool(definition, session)
		if definitionErr != nil {
			return nil, lifecycleFailure(lifecyclePhaseAccept, errors.New("candidate tool catalog is invalid"))
		}
		implementations[definition.Name()] = implementation
	}
	if len(implementations) == 0 {
		return nil, lifecycleFailure(lifecyclePhaseAccept, errors.New("candidate tool catalog is empty"))
	}

	accepted := &acceptedCandidate{
		candidate:   value,
		session:     session,
		toolSet:     implementations,
		processDone: ownedProcess.Done(),
		stdout:      stdout,
		connection:  connection,
		unhealthy:   make(chan struct{}),
	}
	observation, cancel := context.WithCancel(context.Background())
	accepted.stopObserving = cancel
	go accepted.observe(observation, ownedProcess.Done(), stdout.failureSignal(), connection)
	return accepted, nil
}

func (accepted *acceptedCandidate) toolSession() *remoteSession {
	if accepted == nil {
		return nil
	}
	return accepted.session
}

func (accepted *acceptedCandidate) tools() map[string]tool.Tool {
	if accepted == nil {
		return nil
	}
	result := make(map[string]tool.Tool, len(accepted.toolSet))
	maps.Copy(result, accepted.toolSet)
	return result
}

func (accepted *acceptedCandidate) done() <-chan struct{} { return accepted.healthSignal() }

func (accepted *acceptedCandidate) healthSignal() <-chan struct{} {
	if accepted == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return accepted.unhealthy
}

func (accepted *acceptedCandidate) healthFailure() error {
	if accepted == nil {
		return lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate is unavailable"))
	}
	accepted.healthMu.Lock()
	defer accepted.healthMu.Unlock()
	return accepted.healthResult
}

func (accepted *acceptedCandidate) close(ctx context.Context) error {
	if accepted == nil {
		return nil
	}
	if accepted.candidate == nil || accepted.session == nil || ctx == nil {
		return lifecycleFailure(lifecyclePhaseContainment, errors.New("lifecycle context is required"))
	}

	accepted.operation.Lock()
	defer accepted.operation.Unlock()
	if accepted.contained {
		return accepted.closeResult
	}

	drainErr := accepted.drainLocked(ctx)
	if !accepted.admissionDrained {
		return lifecycleCloseFailure(drainErr)
	}
	var shutdownErr error
	if drainErr == nil {
		shutdownErr = accepted.shutdownLocked(ctx)
		if shutdownErr == nil {
			shutdownErr = accepted.awaitProcessExitLocked(ctx)
		}
	}
	cleanupErr := accepted.candidate.cleanup(ctx)

	accepted.candidate.mu.Lock()
	contained := accepted.candidate.closed
	accepted.candidate.mu.Unlock()
	result := lifecycleCloseFailure(errors.Join(drainErr, shutdownErr, cleanupErr))
	if !contained {
		return result
	}
	accepted.contained = true
	accepted.closeResult = result
	if accepted.stopObserving != nil {
		accepted.stopObserving()
	}
	return accepted.closeResult
}

func (accepted *acceptedCandidate) awaitProcessExitLocked(ctx context.Context) error {
	operation, cancel := boundedContext(ctx, accepted.candidate.executable.ShutdownTimeout())
	defer cancel()
	select {
	case <-accepted.processDone:
		return nil
	case <-operation.Done():
		return lifecycleFailure(lifecyclePhaseShutdown, operation.Err())
	}
}

func (accepted *acceptedCandidate) drainLocked(ctx context.Context) error {
	if accepted.contained {
		return accepted.closeResult
	}
	if accepted.drainAttempted {
		return accepted.drainResult
	}
	operation, cancel := boundedContext(ctx, accepted.candidate.executable.DrainTimeout())
	defer cancel()
	if !accepted.admissionDrained {
		if err := operation.Err(); err != nil {
			return lifecycleFailure(lifecyclePhaseDrain, err)
		}
		if err := accepted.session.beginDrain(operation); err != nil {
			return lifecycleFailure(lifecyclePhaseDrain, err)
		}
		if err := operation.Err(); err != nil {
			return lifecycleFailure(lifecyclePhaseDrain, err)
		}
		accepted.admissionDrained = true
	}
	accepted.drainAttempted = true
	if err := accepted.currentHealthFailure(); err != nil {
		accepted.drainResult = err
		return err
	}
	request := &pluginv1.DrainRequest{SessionId: slices.Clone(accepted.session.sessionID)}
	if err := pluginv1.ValidateDrainRequest(request, accepted.session.sessionID, accepted.session.limits); err != nil {
		accepted.drainResult = lifecycleFailure(lifecyclePhaseDrain, errors.New("drain request is invalid"))
		return accepted.drainResult
	}
	response, err := accepted.session.client.Drain(operation, request)
	if err != nil {
		accepted.drainResult = lifecycleRPCFailure(lifecyclePhaseDrain, operation)
		return accepted.drainResult
	}
	if err = pluginv1.ValidateDrainResponse(response, accepted.session.limits); err != nil ||
		response.GetStatus().GetCode() != commonv1.ErrorCode_ERROR_CODE_OK {
		accepted.drainResult = lifecycleFailure(lifecyclePhaseDrain, errors.New("drain response is invalid"))
		return accepted.drainResult
	}
	if err = accepted.currentHealthFailure(); err != nil {
		accepted.drainResult = err
	}
	return accepted.drainResult
}

func (accepted *acceptedCandidate) shutdownLocked(ctx context.Context) error {
	if accepted.contained {
		return accepted.closeResult
	}
	if accepted.shutdownAttempted {
		return accepted.shutdownResult
	}
	if !accepted.drainAttempted || accepted.drainResult != nil {
		return lifecycleFailure(lifecyclePhaseShutdown, errors.New("candidate is not drained"))
	}
	accepted.shutdownAttempted = true

	if err := accepted.currentHealthFailure(); err != nil {
		accepted.shutdownResult = err
		return err
	}
	operation, cancel := boundedContext(ctx, accepted.candidate.executable.ShutdownTimeout())
	defer cancel()
	if err := operation.Err(); err != nil {
		accepted.shutdownResult = lifecycleFailure(lifecyclePhaseShutdown, err)
		return accepted.shutdownResult
	}
	request := &pluginv1.ShutdownRequest{SessionId: slices.Clone(accepted.session.sessionID)}
	if err := pluginv1.ValidateShutdownRequest(request, accepted.session.sessionID, accepted.session.limits); err != nil {
		accepted.shutdownResult = lifecycleFailure(lifecyclePhaseShutdown, errors.New("shutdown request is invalid"))
		return accepted.shutdownResult
	}
	response, err := accepted.session.client.Shutdown(operation, request)
	if err != nil {
		accepted.shutdownResult = lifecycleRPCFailure(lifecyclePhaseShutdown, operation)
		return accepted.shutdownResult
	}
	if err = pluginv1.ValidateShutdownResponse(response, accepted.session.limits); err != nil ||
		response.GetStatus().GetCode() != commonv1.ErrorCode_ERROR_CODE_OK {
		accepted.shutdownResult = lifecycleFailure(lifecyclePhaseShutdown, errors.New("shutdown response is invalid"))
	}
	return accepted.shutdownResult
}

func (accepted *acceptedCandidate) currentHealthFailure() error {
	if failure := accepted.healthFailure(); failure != nil {
		return failure
	}
	if accepted.stdout == nil || accepted.connection == nil || accepted.processDone == nil {
		failure := lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate health ownership is unavailable"))
		accepted.markUnhealthy(failure)
		return failure
	}
	if err := accepted.stdout.err(); err != nil {
		failure := lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate process is unavailable"))
		accepted.markUnhealthy(failure)
		return failure
	}
	select {
	case <-accepted.processDone:
		failure := lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate process is unavailable"))
		accepted.markUnhealthy(failure)
		return failure
	default:
	}
	state := accepted.connection.GetState()
	if state == connectivity.Shutdown || state == connectivity.TransientFailure {
		failure := lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate connection is unavailable"))
		accepted.markUnhealthy(failure)
		return failure
	}
	return nil
}

func (accepted *acceptedCandidate) observe(
	ctx context.Context,
	processDone <-chan struct{},
	stdoutFailed <-chan struct{},
	connection interface {
		GetState() connectivity.State
		WaitForStateChange(context.Context, connectivity.State) bool
	},
) {
	connectionFailed := make(chan struct{}, 1)
	go func() {
		for {
			state := connection.GetState()
			if state == connectivity.Shutdown || state == connectivity.TransientFailure {
				connectionFailed <- struct{}{}
				return
			}
			if !connection.WaitForStateChange(ctx, state) {
				return
			}
		}
	}()

	var failure error
	select {
	case <-processDone:
		failure = lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate process exited"))
	case <-stdoutFailed:
		failure = lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate stdout failed"))
	case <-connectionFailed:
		failure = lifecycleFailure(lifecyclePhaseHealth, errors.New("candidate connection failed"))
	case <-ctx.Done():
		return
	}
	accepted.markUnhealthy(failure)
}

func (accepted *acceptedCandidate) markUnhealthy(failure error) {
	accepted.unhealthyOnce.Do(func() {
		accepted.healthMu.Lock()
		accepted.healthResult = failure
		accepted.healthMu.Unlock()
		close(accepted.unhealthy)
		if accepted.stopObserving != nil {
			accepted.stopObserving()
		}
	})
}

func lifecycleRPCFailure(phase lifecyclePhase, operation context.Context) error {
	if err := operation.Err(); err != nil {
		return lifecycleFailure(phase, err)
	}
	return lifecycleFailure(phase, errors.New("lifecycle transport failed"))
}

type lifecycleError struct {
	phase lifecyclePhase
	cause error
}

func lifecycleFailure(phase lifecyclePhase, cause error) error {
	if cause == nil {
		return nil
	}
	return &lifecycleError{phase: phase, cause: cause}
}

func (failure *lifecycleError) Error() string {
	if failure == nil || !validLifecyclePhase(failure.phase) {
		return "runtime plugin lifecycle failed"
	}
	return "runtime plugin lifecycle failed during " + string(failure.phase)
}

func (failure *lifecycleError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure *lifecycleError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, failure.Error())
}

func (failure *lifecycleError) MarshalJSON() ([]byte, error) { return json.Marshal(failure.Error()) }

func validLifecyclePhase(phase lifecyclePhase) bool {
	switch phase {
	case lifecyclePhaseAccept, lifecyclePhaseHealth, lifecyclePhaseDrain,
		lifecyclePhaseShutdown, lifecyclePhaseContainment:
		return true
	default:
		return false
	}
}

type lifecycleCloseError struct{ cause error }

func lifecycleCloseFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return &lifecycleCloseError{cause: cause}
}

func (*lifecycleCloseError) Error() string { return "runtime plugin graceful lifecycle failed" }

func (failure *lifecycleCloseError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure *lifecycleCloseError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, failure.Error())
}

func (failure *lifecycleCloseError) MarshalJSON() ([]byte, error) {
	return json.Marshal(failure.Error())
}

func (*acceptedCandidate) String() string   { return "pluginhost.acceptedCandidate([REDACTED])" }
func (*acceptedCandidate) GoString() string { return "pluginhost.acceptedCandidate([REDACTED])" }
func (*acceptedCandidate) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "pluginhost.acceptedCandidate([REDACTED])")
}

func (*acceptedCandidate) MarshalJSON() ([]byte, error) {
	return json.Marshal("pluginhost.acceptedCandidate([REDACTED])")
}
