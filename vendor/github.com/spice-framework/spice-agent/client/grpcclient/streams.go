package grpcclient

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"

	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type streamLifetime struct {
	owner   *session
	done    <-chan struct{}
	cause   func() error
	cancel  context.CancelCauseFunc
	closed  atomic.Bool
	ended   atomic.Bool
	once    sync.Once
	rpcOnce sync.Once
	rpcDone chan struct{}
}

func newStreamLifetime(owner *session) (*streamLifetime, context.Context) {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &streamLifetime{
		owner: owner, done: ctx.Done(), cause: func() error { return context.Cause(ctx) }, cancel: cancel,
		rpcDone: make(chan struct{}),
	}, ctx
}

func (lifetime *streamLifetime) close() {
	if lifetime == nil {
		return
	}
	lifetime.once.Do(func() {
		lifetime.closed.Store(true)
		lifetime.cancel(client.ErrClosed)
		if lifetime.ended.Load() && lifetime.owner != nil {
			lifetime.owner.releaseStream(lifetime)
		}
	})
}

func (lifetime *streamLifetime) finish() {
	if lifetime == nil || lifetime.ended.Swap(true) {
		return
	}
	if lifetime.closed.Load() && lifetime.owner != nil {
		lifetime.owner.releaseStream(lifetime)
	}
}

func (lifetime *streamLifetime) finishRPC() {
	lifetime.rpcOnce.Do(func() {
		close(lifetime.rpcDone)
		lifetime.finish()
	})
}

func (lifetime *streamLifetime) interrupt(cause error) {
	if lifetime != nil {
		lifetime.cancel(cause)
	}
}

func (lifetime *streamLifetime) waitFor(ctx context.Context) error {
	select {
	case <-lifetime.rpcDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type streamResult[T any] struct {
	value T
	err   error
}

type eventStream struct {
	lifetime *streamLifetime
	initial  []client.EventFrame
	next     int
	frames   <-chan streamResult[client.EventFrame]
	nextMu   sync.Mutex
}

func (stream *eventStream) Next(ctx context.Context) (client.EventFrame, error) {
	stream.nextMu.Lock()
	defer stream.nextMu.Unlock()
	if ctx == nil {
		return client.EventFrame{}, invalidArgumentError("event stream context is required")
	}
	if stream.lifetime.closed.Load() {
		return client.EventFrame{}, client.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return client.EventFrame{}, context.Cause(ctx)
	}
	if stream.next < len(stream.initial) {
		result := stream.initial[stream.next]
		stream.next++
		return result, nil
	}
	select {
	case <-ctx.Done():
		return client.EventFrame{}, context.Cause(ctx)
	case <-stream.lifetime.done:
		if stream.lifetime.closed.Load() {
			return client.EventFrame{}, client.ErrClosed
		}
		return client.EventFrame{}, transportError(context.Background(), stream.lifetime.cause())
	case result, open := <-stream.frames:
		if stream.lifetime.closed.Load() {
			return client.EventFrame{}, client.ErrClosed
		}
		if !open {
			return client.EventFrame{}, io.EOF
		}
		return result.value, result.err
	}
}

func (stream *eventStream) Close() error {
	stream.lifetime.close()
	return nil
}

type interactionStream struct {
	lifetime *streamLifetime
	initial  []client.InteractionFrame
	next     int
	frames   <-chan streamResult[client.InteractionFrame]
	nextMu   sync.Mutex
}

func (stream *interactionStream) Next(ctx context.Context) (client.InteractionFrame, error) {
	stream.nextMu.Lock()
	defer stream.nextMu.Unlock()
	if ctx == nil {
		return client.InteractionFrame{}, invalidArgumentError("interaction stream context is required")
	}
	if stream.lifetime.closed.Load() {
		return client.InteractionFrame{}, client.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return client.InteractionFrame{}, context.Cause(ctx)
	}
	if stream.next < len(stream.initial) {
		result := stream.initial[stream.next]
		stream.next++
		return result, nil
	}
	select {
	case <-ctx.Done():
		return client.InteractionFrame{}, context.Cause(ctx)
	case <-stream.lifetime.done:
		if stream.lifetime.closed.Load() {
			return client.InteractionFrame{}, client.ErrClosed
		}
		return client.InteractionFrame{}, transportError(context.Background(), stream.lifetime.cause())
	case result, open := <-stream.frames:
		if stream.lifetime.closed.Load() {
			return client.InteractionFrame{}, client.ErrClosed
		}
		if !open {
			return client.InteractionFrame{}, io.EOF
		}
		return result.value, result.err
	}
}

func (stream *interactionStream) Close() error {
	stream.lifetime.close()
	return nil
}

func (current *session) Events(
	ctx context.Context,
	cursor client.Cursor,
	options client.EventStreamOptions,
) (client.EventStream, error) {
	if ctx == nil {
		return nil, invalidArgumentError("stream context is required")
	}
	if err := cursor.Validate(); err != nil {
		return nil, invalidArgumentError("event cursor is invalid")
	}
	if err := options.Validate(current.connection.Limits()); err != nil {
		return nil, invalidArgumentError("event stream options are invalid")
	}
	rpcContext, lifetime, err := current.streamContext(ctx)
	if err != nil {
		return nil, err
	}
	openingStop := context.AfterFunc(ctx, func() { lifetime.interrupt(ctx.Err()) })
	fail := func(err error) (client.EventStream, error) {
		openingStop()
		lifetime.close()
		lifetime.finishRPC()
		return nil, err
	}
	request := &enginev1.StreamEventsRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
		RunId: cursor.Run().ID(), AfterSequence: cursor.AfterSequence(),
		ReplayLimit: options.ReplayLimit(), Tail: options.Tail(),
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateStreamEventsRequest(request, limits) != nil {
		return fail(protocolError())
	}
	wireStream, err := current.service.StreamEvents(rpcContext, request, current.callOptions()...)
	if err != nil {
		return fail(transportError(ctx, err))
	}
	initial, control, err := receiveEventPage(wireStream, cursor, options, current.connection.Limits(), limits)
	if err != nil {
		return fail(err)
	}
	if !openingStop() && ctx.Err() != nil {
		return fail(ctx.Err())
	}
	frames := make(chan streamResult[client.EventFrame], 1)
	go receiveEventTail(lifetime, wireStream, cursor.Run(), control, limits, frames)
	return &eventStream{lifetime: lifetime, initial: initial, frames: frames}, nil
}

func receiveEventPage(
	wireStream grpc.ServerStreamingClient[enginev1.StreamEventsResponse],
	cursor client.Cursor,
	options client.EventStreamOptions,
	limits client.Limits,
	_ *commonv1.Limits,
) ([]client.EventFrame, *enginev1.StreamControl, error) {
	state := eventPageState{
		cursor: cursor, options: options, limits: limits,
		next: cursor.AfterSequence(),
	}
	for {
		wireFrame, err := wireStream.Recv()
		if err != nil {
			return nil, nil, transportError(wireStream.Context(), err)
		}
		if commonv1.ValidateEncodedSize(wireFrame, limits.MessageBytes()) != nil {
			return nil, nil, protocolError()
		}
		if eventValue := wireFrame.GetEvent(); eventValue != nil {
			if state.acceptEvent(eventValue) != nil {
				return nil, nil, protocolError()
			}
			continue
		}
		control := wireFrame.GetControl()
		if err = state.acceptControl(control); err != nil {
			return nil, nil, err
		}
		return state.frames, control, nil
	}
}

type eventPageState struct {
	cursor      client.Cursor
	options     client.EventStreamOptions
	limits      client.Limits
	frames      []client.EventFrame
	next        uint64
	replayBytes uint64
}

func (state *eventPageState) acceptEvent(value *enginev1.RunEvent) error {
	if uint64(len(state.frames)) >= uint64(state.options.ReplayLimit()) || state.next == math.MaxUint64 ||
		value.GetRunId() != state.cursor.Run().ID() || value.GetSequence() != state.next+1 {
		return protocolError()
	}
	size := proto.Size(value)
	if size < 0 {
		return protocolError()
	}
	encodedSize := uint64(size) // #nosec G115 -- proto.Size was checked non-negative.
	if encodedSize > state.limits.ReplayBytes() ||
		state.replayBytes > state.limits.ReplayBytes()-encodedSize {
		return protocolError()
	}
	frame, err := publicEventFrame(value)
	if err != nil {
		return protocolError()
	}
	state.replayBytes += encodedSize
	state.frames = append(state.frames, frame)
	state.next = value.GetSequence()
	return nil
}

func (state *eventPageState) acceptControl(value *enginev1.StreamControl) error {
	if value == nil || enginev1.ValidateStreamControl(value) != nil {
		return protocolError()
	}
	if commonv1.AsError(value.GetStatus()) != nil {
		if len(state.frames) != 0 {
			return protocolError()
		}
		after := state.cursor.AfterSequence()
		return statusToError(value.GetStatus(), statusContext{
			run: new(state.cursor.Run()), after: &after, readOnly: true,
		})
	}
	if value.GetLastDeliveredSequence() != state.next || value.GetTailing() && !state.options.Tail() {
		return protocolError()
	}
	if value.PageLastSequence != nil && value.GetPageLastSequence() != state.next {
		return protocolError()
	}
	publicControl, err := publicEventControl(value)
	if err != nil {
		return protocolError()
	}
	controlFrame, err := client.NewEventControlFrame(publicControl)
	if err != nil {
		return protocolError()
	}
	state.frames = append(state.frames, controlFrame)
	return nil
}

func receiveEventTail(
	lifetime *streamLifetime,
	wireStream grpc.ServerStreamingClient[enginev1.StreamEventsResponse],
	run client.RunRef,
	control *enginev1.StreamControl,
	limits *commonv1.Limits,
	frames chan<- streamResult[client.EventFrame],
) {
	defer lifetime.finishRPC()
	defer close(frames)
	next := control.GetLastDeliveredSequence()
	for {
		wireFrame, err := wireStream.Recv()
		if err != nil {
			sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{err: receiveError(lifetime, err)})
			return
		}
		if commonv1.ValidateEncodedSize(wireFrame, limits.GetMaxMessageBytes()) != nil {
			sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{err: protocolError()})
			return
		}
		if !control.GetTailing() {
			sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{err: protocolError()})
			return
		}
		if eventValue := wireFrame.GetEvent(); eventValue != nil {
			if next == math.MaxUint64 || eventValue.GetRunId() != run.ID() ||
				eventValue.GetSequence() != next+1 {
				sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{err: protocolError()})
				return
			}
			frame, convertErr := publicEventFrame(eventValue)
			if convertErr != nil {
				sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{err: protocolError()})
				return
			}
			next = eventValue.GetSequence()
			if !sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{value: frame}) {
				return
			}
			continue
		}
		tailControl := wireFrame.GetControl()
		if tailControl == nil || enginev1.ValidateStreamControl(tailControl) != nil ||
			tailControl.GetStatus().GetCode() == commonv1.ErrorCode_ERROR_CODE_OK {
			sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{err: protocolError()})
			return
		}
		sendStreamResult(lifetime, frames, streamResult[client.EventFrame]{err: statusToError(
			tailControl.GetStatus(), statusContext{run: &run, readOnly: true},
		)})
		return
	}
}

func (current *session) Interactions(
	ctx context.Context,
	options client.InteractionStreamOptions,
) (client.InteractionStream, error) {
	if ctx == nil {
		return nil, invalidArgumentError("stream context is required")
	}
	if err := options.Validate(); err != nil {
		return nil, invalidArgumentError("interaction stream options are invalid")
	}
	rpcContext, lifetime, err := current.streamContext(ctx)
	if err != nil {
		return nil, err
	}
	openingStop := context.AfterFunc(ctx, func() { lifetime.interrupt(ctx.Err()) })
	fail := func(err error) (client.InteractionStream, error) {
		openingStop()
		lifetime.close()
		lifetime.finishRPC()
		return nil, err
	}
	request := &enginev1.StreamInteractionsRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(), Tail: options.Tail(),
	}
	protocol, _ := protocolVersionToWire(current.connection.Protocol())
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateStreamInteractionsRequest(request, protocol, limits) != nil {
		return fail(protocolError())
	}
	wireStream, err := current.service.StreamInteractions(rpcContext, request, current.callOptions()...)
	if err != nil {
		return fail(transportError(ctx, err))
	}
	initial, validator, err := receiveInteractionPage(wireStream, options, current.connection.Limits(), limits)
	if err != nil {
		return fail(err)
	}
	if !openingStop() && ctx.Err() != nil {
		return fail(ctx.Err())
	}
	frames := make(chan streamResult[client.InteractionFrame], 1)
	go receiveInteractionTail(lifetime, wireStream, validator, limits, frames)
	return &interactionStream{lifetime: lifetime, initial: initial, frames: frames}, nil
}

func receiveInteractionPage(
	wireStream grpc.ServerStreamingClient[enginev1.StreamInteractionsResponse],
	options client.InteractionStreamOptions,
	limits client.Limits,
	wireLimits *commonv1.Limits,
) ([]client.InteractionFrame, *enginev1.InteractionTailValidator, error) {
	first, err := wireStream.Recv()
	if err != nil {
		return nil, nil, transportError(wireStream.Context(), err)
	}
	if commonv1.ValidateEncodedSize(first, limits.MessageBytes()) != nil {
		return nil, nil, protocolError()
	}
	if failure := first.GetControl(); failure != nil {
		if enginev1.ValidateInteractionStreamControl(failure) != nil ||
			failure.GetStatus().GetCode() == commonv1.ErrorCode_ERROR_CODE_OK {
			return nil, nil, protocolError()
		}
		return nil, nil, statusToError(failure.GetStatus(), statusContext{readOnly: true})
	}
	second, err := wireStream.Recv()
	if err != nil {
		return nil, nil, transportError(wireStream.Context(), err)
	}
	page := []*enginev1.StreamInteractionsResponse{first, second}
	if enginev1.ValidateInteractionStreamPage(page, options.Tail(), wireLimits) != nil {
		return nil, nil, protocolError()
	}
	snapshot, err := publicInteractionSnapshot(first.GetSnapshot(), limits)
	if err != nil {
		return nil, nil, protocolError()
	}
	snapshotFrame, err := client.NewInteractionFrame(snapshot)
	if err != nil {
		return nil, nil, protocolError()
	}
	control := second.GetControl()
	publicControl, err := client.NewInteractionControl(
		control.GetLatestRevision(), control.GetPageLastRevision(), control.GetHasMore(), control.GetTailing(),
	)
	if err != nil {
		return nil, nil, protocolError()
	}
	controlFrame, err := client.NewInteractionControlFrame(publicControl)
	if err != nil {
		return nil, nil, protocolError()
	}
	var validator *enginev1.InteractionTailValidator
	if options.Tail() {
		validator, err = enginev1.NewInteractionTailValidator(first.GetSnapshot(), control, wireLimits)
		if err != nil {
			return nil, nil, protocolError()
		}
	}
	return []client.InteractionFrame{snapshotFrame, controlFrame}, validator, nil
}

func receiveInteractionTail(
	lifetime *streamLifetime,
	wireStream grpc.ServerStreamingClient[enginev1.StreamInteractionsResponse],
	validator *enginev1.InteractionTailValidator,
	limits *commonv1.Limits,
	frames chan<- streamResult[client.InteractionFrame],
) {
	defer lifetime.finishRPC()
	defer close(frames)
	for {
		wireFrame, err := wireStream.Recv()
		if err != nil {
			sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{err: receiveError(lifetime, err)})
			return
		}
		if commonv1.ValidateEncodedSize(wireFrame, limits.GetMaxMessageBytes()) != nil {
			sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{err: protocolError()})
			return
		}
		if validator == nil {
			sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{err: protocolError()})
			return
		}
		if control := wireFrame.GetControl(); control != nil {
			if enginev1.ValidateInteractionStreamControl(control) != nil ||
				control.GetStatus().GetCode() == commonv1.ErrorCode_ERROR_CODE_OK {
				sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{err: protocolError()})
				return
			}
			sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{err: statusToError(
				control.GetStatus(), statusContext{readOnly: true},
			)})
			return
		}
		if validator.Accept(wireFrame) != nil {
			sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{err: protocolError()})
			return
		}
		update, convertErr := publicInteractionDelta(wireFrame.GetDelta())
		if convertErr != nil {
			sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{err: protocolError()})
			return
		}
		frame, convertErr := client.NewInteractionFrame(update)
		if convertErr != nil || !sendStreamResult(lifetime, frames, streamResult[client.InteractionFrame]{value: frame}) {
			return
		}
	}
}

func publicEventFrame(value *enginev1.RunEvent) (client.EventFrame, error) {
	eventValue, err := eventFromWire(value)
	if err != nil {
		return client.EventFrame{}, err
	}
	return client.NewEventFrame(eventValue)
}

func publicEventControl(value *enginev1.StreamControl) (client.EventControl, error) {
	if value.PageLastSequence == nil {
		return client.NewLegacyEventControl(
			value.GetEarliestSequence(), value.GetLatestSequence(), value.GetLastDeliveredSequence(),
		)
	}
	return client.NewEventControl(
		value.GetEarliestSequence(), value.GetLatestSequence(), value.GetLastDeliveredSequence(),
		value.GetPageLastSequence(), value.GetHasMore(), value.GetTailing(),
	)
}

func publicInteractionSnapshot(
	value *enginev1.InteractionSnapshot,
	limits client.Limits,
) (client.InteractionUpdate, error) {
	pending := make([]client.PendingInteraction, 0, len(value.GetPending()))
	for _, wirePending := range value.GetPending() {
		converted, err := pendingFromWire(wirePending)
		if err != nil {
			return client.InteractionUpdate{}, err
		}
		pending = append(pending, converted)
	}
	return client.NewInteractionSnapshot(value.GetRevision(), pending, limits)
}

func publicInteractionDelta(value *enginev1.InteractionDelta) (client.InteractionUpdate, error) {
	pending, err := pendingFromWire(value.GetInteraction())
	if err != nil {
		return client.InteractionUpdate{}, err
	}
	var kind client.InteractionUpdateKind
	switch value.GetKind() {
	case enginev1.InteractionDeltaKind_INTERACTION_DELTA_KIND_OPENED:
		kind = client.InteractionOpened
	case enginev1.InteractionDeltaKind_INTERACTION_DELTA_KIND_CLOSED:
		kind = client.InteractionClosed
	default:
		return client.InteractionUpdate{}, errors.New("interaction delta kind is unsupported")
	}
	return client.NewInteractionChange(kind, value.GetRevision(), pending)
}

func receiveError(lifetime *streamLifetime, err error) error {
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	if lifetime.closed.Load() {
		return client.ErrClosed
	}
	return transportError(context.Background(), err)
}

func sendStreamResult[T any](
	lifetime *streamLifetime,
	output chan<- streamResult[T],
	result streamResult[T],
) bool {
	select {
	case output <- result:
		return true
	case <-lifetime.done:
		return false
	}
}

var (
	_ client.EventStream       = (*eventStream)(nil)
	_ client.InteractionStream = (*interactionStream)(nil)
)
