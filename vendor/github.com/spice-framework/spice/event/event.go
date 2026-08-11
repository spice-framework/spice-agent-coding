// Package event provides typed, instance-owned application event topics for
// generated Spice applications.
package event

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// ErrPanicked identifies an observed event-handler panic. Publish reports the
// panic to observers and then re-panics with the original value.
var ErrPanicked = errors.New("event handler panicked")

// Definition identifies one compiler-owned event contract and its publishing
// module.
type Definition struct {
	ID     string
	Module string
}

// Handler consumes one strongly typed event on the publisher's goroutine.
type Handler[T any] func(context.Context, T) error

// Subscriber declares one generated event handler. Lower Order values run
// first; ties are resolved by module and stable subscriber ID.
type Subscriber[T any] struct {
	ID     string
	Module string
	Order  int
	Handle Handler[T]
}

// SubscriberMetadata is the payload-free identity exposed to observers.
type SubscriberMetadata struct {
	ID     string
	Module string
	Order  int
}

// Interaction identifies one delivery from an event contract to a subscriber.
type Interaction struct {
	Event      Definition
	Subscriber SubscriberMetadata
}

// Result describes one completed event-handler interaction.
type Result struct {
	Interaction Interaction
	Duration    time.Duration
	Err         error
	Panicked    bool
}

// Observer receives event interaction begin/end information on the publishing
// goroutine. Implementations must not panic or block indefinitely.
type Observer interface {
	BeginEvent(context.Context, Interaction) (context.Context, func(Result))
}

// Publisher is the narrow event dependency injected into producers.
type Publisher[T any] interface {
	Publish(context.Context, T) error
}

// Topic is one immutable typed event contract and its generated subscribers.
type Topic[T any] struct {
	definition  Definition
	subscribers []Subscriber[T]
	observers   []Observer
}

// NewTopic validates and freezes one generated event topic. It starts no
// goroutines and installs no global registry.
func NewTopic[T any](
	definition Definition,
	subscribers []Subscriber[T],
	observers ...Observer,
) (*Topic[T], error) {
	if err := validateDefinition(definition); err != nil {
		return nil, err
	}
	frozen := append([]Subscriber[T](nil), subscribers...)
	seen := make(map[string]struct{}, len(frozen))
	for index, subscriber := range frozen {
		if subscriber.ID == "" {
			return nil, fmt.Errorf("construct event %q: subscriber %d has no ID", definition.ID, index)
		}
		if subscriber.Module == "" {
			return nil, fmt.Errorf(
				"construct event %q: subscriber %q has no module",
				definition.ID,
				subscriber.ID,
			)
		}
		if subscriber.Handle == nil {
			return nil, fmt.Errorf(
				"construct event %q: subscriber %q has no handler",
				definition.ID,
				subscriber.ID,
			)
		}
		if _, duplicate := seen[subscriber.ID]; duplicate {
			return nil, fmt.Errorf(
				"construct event %q: duplicate subscriber ID %q",
				definition.ID,
				subscriber.ID,
			)
		}
		seen[subscriber.ID] = struct{}{}
	}
	slices.SortFunc(frozen, compareSubscribers[T])

	for index, observer := range observers {
		if nilObserver(observer) {
			return nil, fmt.Errorf("construct event %q: observer %d is nil", definition.ID, index)
		}
	}
	return &Topic[T]{
		definition:  definition,
		subscribers: frozen,
		observers:   append([]Observer(nil), observers...),
	}, nil
}

// Publish delivers an event synchronously in deterministic subscriber order.
// Delivery stops at cancellation, panic, or the first handler error.
func (topic *Topic[T]) Publish(ctx context.Context, value T) error {
	if ctx == nil {
		return errors.New("publish event: context is nil")
	}
	if topic == nil {
		return errors.New("publish event: topic is nil")
	}
	for _, subscriber := range topic.subscribers {
		if cause := context.Cause(ctx); cause != nil {
			return fmt.Errorf("publish event %q: %w", topic.definition.ID, cause)
		}
		interaction := Interaction{
			Event: topic.definition,
			Subscriber: SubscriberMetadata{
				ID:     subscriber.ID,
				Module: subscriber.Module,
				Order:  subscriber.Order,
			},
		}
		if err := topic.deliver(ctx, interaction, subscriber.Handle, value); err != nil {
			return err
		}
	}
	return nil
}

func (topic *Topic[T]) deliver(
	ctx context.Context,
	interaction Interaction,
	handler Handler[T],
	value T,
) (resultErr error) {
	observedContext, finish := beginObservation(ctx, interaction, topic.observers)
	started := time.Now()
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		finish(Result{
			Interaction: interaction,
			Duration:    time.Since(started),
			Err:         ErrPanicked,
			Panicked:    true,
		})
		panic(recovered)
	}()
	if err := handler(observedContext, value); err != nil {
		resultErr = fmt.Errorf(
			"publish event %q to subscriber %q: %w",
			interaction.Event.ID,
			interaction.Subscriber.ID,
			err,
		)
		finish(Result{Interaction: interaction, Duration: time.Since(started), Err: resultErr})
		return resultErr
	}
	finish(Result{Interaction: interaction, Duration: time.Since(started)})
	return nil
}

func validateDefinition(definition Definition) error {
	if definition.ID == "" {
		return errors.New("construct event: event ID is required")
	}
	if definition.Module == "" {
		return fmt.Errorf("construct event %q: publishing module is required", definition.ID)
	}
	return nil
}

func compareSubscribers[T any](left, right Subscriber[T]) int {
	if compared := cmp.Compare(left.Order, right.Order); compared != 0 {
		return compared
	}
	if compared := cmp.Compare(left.Module, right.Module); compared != 0 {
		return compared
	}
	return cmp.Compare(left.ID, right.ID)
}

func beginObservation(
	ctx context.Context,
	interaction Interaction,
	observers []Observer,
) (context.Context, func(Result)) {
	finishers := make([]func(Result), 0, len(observers))
	observedContext := beginObservers(ctx, interaction, observers, &finishers)
	return observedContext, func(result Result) {
		for _, finish := range slices.Backward(finishers) {
			finish(result)
		}
	}
}

func beginObservers(
	ctx context.Context,
	interaction Interaction,
	observers []Observer,
	finishers *[]func(Result),
) context.Context {
	if len(observers) == 0 {
		return ctx
	}
	next, finish := observers[0].BeginEvent(ctx, interaction)
	if next == nil {
		next = ctx
	}
	if finish != nil {
		*finishers = append(*finishers, finish)
	}
	return beginObservers(next, interaction, observers[1:], finishers)
}

func nilObserver(observer Observer) bool {
	if observer == nil {
		return true
	}
	value := reflect.ValueOf(observer)
	return (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) && value.IsNil()
}

var _ Publisher[any] = (*Topic[any])(nil)
