package pluginhost

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/tool"
	"google.golang.org/protobuf/proto"
)

// remoteSession is the immutable, initialized execution boundary shared by
// every tool in one plugin process. The semaphore is the only mutable state and
// enforces the exact concurrent-call limit negotiated during initialization.
type remoteSession struct {
	client      pluginv1.PluginServiceClient
	sessionID   []byte
	limits      *pluginv1.Limits
	callTimeout time.Duration
	admission   chan struct{}

	mu       sync.Mutex
	draining bool
	active   uint32
	zero     chan struct{}
}

func newRemoteSession(
	client pluginv1.PluginServiceClient,
	sessionID []byte,
	limits *pluginv1.Limits,
	callTimeout time.Duration,
) (*remoteSession, error) {
	if client == nil {
		return nil, errors.New("runtime plugin client is required")
	}
	if len(sessionID) != pluginv1.SessionIDBytes {
		return nil, errors.New("runtime plugin session identity is invalid")
	}
	if err := pluginv1.ValidateLimits(limits); err != nil {
		return nil, errors.New("runtime plugin negotiated limits are invalid")
	}
	if callTimeout <= 0 || callTimeout > MaximumOperationTimeout {
		return nil, errors.New("runtime plugin call timeout is outside supported bounds")
	}
	clonedLimits, ok := proto.Clone(limits).(*pluginv1.Limits)
	if !ok {
		return nil, errors.New("runtime plugin negotiated limits could not be copied")
	}
	zero := make(chan struct{})
	close(zero)
	return &remoteSession{
		client:      client,
		sessionID:   slices.Clone(sessionID),
		limits:      clonedLimits,
		callTimeout: callTimeout,
		admission:   make(chan struct{}, int(clonedLimits.GetMaxConcurrentCalls())),
		zero:        zero,
	}, nil
}

// remoteTool is one immutable manifest definition backed by its initialized
// process session. It deliberately contains no retry loop.
type remoteTool struct {
	definition tool.Definition
	session    *remoteSession
}

func newRemoteTool(definition tool.Definition, session *remoteSession) (*remoteTool, error) {
	if err := definition.Validate(); err != nil {
		return nil, errors.New("runtime plugin tool definition is invalid")
	}
	if session == nil || session.client == nil || session.limits == nil || session.admission == nil {
		return nil, errors.New("runtime plugin session is unavailable")
	}
	return &remoteTool{definition: definition.Clone(), session: session}, nil
}

func (implementation *remoteTool) Definition() tool.Definition {
	if implementation == nil {
		return tool.Definition{}
	}
	return implementation.definition.Clone()
}

func (implementation *remoteTool) Execute(
	ctx context.Context,
	call tool.Call,
	reporter tool.Reporter,
) (tool.Result, error) {
	if implementation == nil || implementation.session == nil {
		return tool.Result{}, errors.New("runtime plugin tool is unavailable")
	}
	if ctx == nil {
		return tool.Result{}, errors.New("runtime plugin execution context is required")
	}
	if err := call.Validate(); err != nil {
		return tool.Result{}, errors.New("runtime plugin call is invalid")
	}
	if call.Name() != implementation.definition.Name() {
		return tool.Result{}, errors.New("runtime plugin call does not match the tool definition")
	}

	operation, cancel := context.WithTimeout(ctx, implementation.session.callTimeout)
	defer cancel()
	if err := implementation.session.acquire(operation); err != nil {
		return tool.Result{}, implementation.failure(call.ID(), true, remoteAdmissionFailure, err)
	}
	defer implementation.session.release()

	request := &pluginv1.ExecuteRequest{
		SessionId:     slices.Clone(implementation.session.sessionID),
		CallId:        string(call.ID()),
		ToolName:      call.Name(),
		ArgumentsJson: call.Arguments(),
	}
	validator, err := pluginv1.NewStreamValidator(
		request,
		implementation.session.sessionID,
		implementation.session.limits,
	)
	if err != nil {
		return tool.Result{}, implementation.failure(call.ID(), true, remoteRequestFailure, nil)
	}
	if err = operation.Err(); err != nil {
		return tool.Result{}, implementation.failure(call.ID(), true, remoteAdmissionFailure, err)
	}

	stream, err := implementation.session.client.Execute(operation, request)
	if err != nil {
		// Once Execute is invoked, a server-streaming status cannot prove that
		// plugin code did not begin work. Even InvalidArgument and
		// ResourceExhausted may be returned by a handler after a mutation.
		return tool.Result{}, implementation.failure(
			call.ID(),
			false,
			remoteTransportFailure,
			operationCause(operation, err),
		)
	}
	if stream == nil {
		return tool.Result{}, implementation.failure(call.ID(), false, remoteProtocolFailure, nil)
	}
	return implementation.receive(operation, call.ID(), reporter, stream, validator)
}

func (implementation *remoteTool) receive(
	ctx context.Context,
	callID tool.CallID,
	reporter tool.Reporter,
	stream pluginv1.PluginService_ExecuteClient,
	validator *pluginv1.StreamValidator,
) (tool.Result, error) {
	var terminal remoteTerminal
	for {
		response, err := stream.Recv()
		if err != nil {
			if terminal.present {
				if errors.Is(err, io.EOF) {
					if finishErr := validator.Finish(); finishErr != nil {
						return tool.Result{}, implementation.failure(callID, false, remoteProtocolFailure, nil)
					}
				}
				return terminal.outcome()
			}
			if errors.Is(err, io.EOF) {
				return tool.Result{}, implementation.failure(callID, false, remoteMissingTerminalFailure, nil)
			}
			return tool.Result{}, implementation.failure(
				callID,
				false,
				remoteTransportFailure,
				operationCause(ctx, err),
			)
		}
		frame, acceptErr := validator.Accept(response)
		if acceptErr != nil {
			return tool.Result{}, implementation.failure(callID, false, remoteProtocolFailure, nil)
		}
		switch frame.Kind() {
		case pluginv1.FrameProgress:
			progress, _ := frame.Progress()
			if reporter != nil {
				if reportErr := reporter.Report(ctx, progress); reportErr != nil {
					return tool.Result{}, implementation.failure(callID, false, remoteReporterFailure, nil)
				}
			}
		case pluginv1.FrameResult:
			terminal.result, _ = frame.Result()
			terminal.present = true
		case pluginv1.FrameFailure:
			terminal.failure, _ = frame.Failure()
			if !implementation.compatibleTerminalFailure(terminal.failure) {
				return tool.Result{}, implementation.failure(callID, false, remoteProtocolFailure, nil)
			}
			terminal.present = true
		default:
			return tool.Result{}, implementation.failure(callID, false, remoteProtocolFailure, nil)
		}
	}
}

func (implementation *remoteTool) compatibleTerminalFailure(failure *tool.ExecutionError) bool {
	if failure == nil {
		return false
	}
	if failure.State() == tool.ExecutionUncertain && implementation.definition.Effect() != tool.EffectMutating {
		return false
	}
	return failure.RetryDisposition() != tool.RetryAllowed ||
		implementation.definition.ReplaySafety() != tool.ReplayUnsafe
}

func (session *remoteSession) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case session.admission <- struct{}{}:
		session.mu.Lock()
		if session.draining {
			session.mu.Unlock()
			<-session.admission
			return errors.New("runtime plugin session is draining")
		}
		if session.active == 0 {
			session.zero = make(chan struct{})
		}
		session.active++
		session.mu.Unlock()
		if err := ctx.Err(); err != nil {
			session.release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *remoteSession) release() {
	session.mu.Lock()
	if session.active == 0 {
		session.mu.Unlock()
		panic("remote plugin session release without admission")
	}
	session.active--
	if session.active == 0 {
		close(session.zero)
	}
	session.mu.Unlock()
	<-session.admission
}

func (session *remoteSession) beginDrain(ctx context.Context) error {
	session.mu.Lock()
	session.draining = true
	zero := session.zero
	session.mu.Unlock()
	select {
	case <-zero:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type remoteTerminal struct {
	result  tool.Result
	failure *tool.ExecutionError
	present bool
}

func (terminal remoteTerminal) outcome() (tool.Result, error) {
	if terminal.failure != nil {
		return tool.Result{}, terminal.failure
	}
	return terminal.result.Clone(), nil
}

type remoteFailureMessage string

const (
	remoteAdmissionFailure       remoteFailureMessage = "runtime plugin execution was canceled before remote admission"
	remoteRequestFailure         remoteFailureMessage = "runtime plugin execution request is invalid"
	remoteTransportFailure       remoteFailureMessage = "runtime plugin execution transport failed"
	remoteProtocolFailure        remoteFailureMessage = "runtime plugin execution stream is invalid"
	remoteMissingTerminalFailure remoteFailureMessage = "runtime plugin execution ended without a terminal outcome"
	remoteReporterFailure        remoteFailureMessage = "runtime plugin progress reporter rejected progress"
)

type remoteFailureCause struct {
	message remoteFailureMessage
	cause   error
}

func (failure *remoteFailureCause) Error() string { return string(failure.message) }
func (failure *remoteFailureCause) Unwrap() error { return failure.cause }

func (implementation *remoteTool) failure(
	callID tool.CallID,
	definitive bool,
	message remoteFailureMessage,
	cause error,
) *tool.ExecutionError {
	state := tool.ExecutionDefinitive
	if !definitive && implementation.definition.Effect() == tool.EffectMutating {
		state = tool.ExecutionUncertain
	}
	failure, err := tool.NewExecutionError(
		callID,
		state,
		tool.RetryNever,
		&remoteFailureCause{message: message, cause: cause},
	)
	if err != nil {
		panic("validated remote execution failure became invalid")
	}
	return failure
}

func operationCause(ctx context.Context, transport error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return transport
}
