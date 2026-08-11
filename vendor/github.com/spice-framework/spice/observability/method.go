package observability

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrMethodPanicked identifies a method observation completed by a panic.
// Observe reports it and then re-panics with the original value.
var ErrMethodPanicked = errors.New("observed method panicked")

// MethodDefinition is compiler-owned metadata for one observed service method.
type MethodDefinition struct {
	ID      string
	Module  string
	Service string
	Method  string
}

// MethodResult describes one completed observed method invocation.
type MethodResult struct {
	Definition MethodDefinition
	Duration   time.Duration
	Err        error
	Panicked   bool
}

// MethodObserver begins one method observation on the calling goroutine. It
// may return a derived context and an optional completion callback.
type MethodObserver interface {
	BeginMethod(context.Context, MethodDefinition) (context.Context, func(MethodResult))
}

// MethodWork is generated service-method logic executed within observation.
type MethodWork func(context.Context) error

// Observe executes work once and reports completion to instance-owned
// observers. Observer contexts are composed in declaration order and
// completion callbacks run in reverse order. Panics are observed and re-raised.
func Observe(
	ctx context.Context,
	definition MethodDefinition,
	observers []MethodObserver,
	work MethodWork,
) (resultErr error) {
	if ctx == nil {
		return errors.New("observe method: context is nil")
	}
	if err := validateMethodDefinition(definition); err != nil {
		return err
	}
	if work == nil {
		return fmt.Errorf("observe method %q: callback is nil", definition.ID)
	}
	for index, observer := range observers {
		if observer == nil {
			return fmt.Errorf("observe method %q: observer %d is nil", definition.ID, index)
		}
	}
	current := ctx
	finishes := make([]func(MethodResult), 0, len(observers))
	for index, observer := range observers {
		observed, finish := observer.BeginMethod(current, definition)
		if observed == nil {
			err := fmt.Errorf(
				"observe method %q: observer %d returned a nil context",
				definition.ID,
				index,
			)
			finishMethodObservations(finishes, MethodResult{
				Definition: definition,
				Err:        err,
			})
			return err
		}
		current = observed //nolint:fatcontext // Observer contexts intentionally compose in the bounded caller-owned slice order.
		if finish != nil {
			finishes = append(finishes, finish)
		}
	}
	started := time.Now()
	finish := func(result MethodResult) {
		finishMethodObservations(finishes, result)
	}
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		finish(MethodResult{
			Definition: definition,
			Duration:   time.Since(started),
			Err:        ErrMethodPanicked,
			Panicked:   true,
		})
		panic(recovered)
	}()
	resultErr = work(current)
	finish(MethodResult{
		Definition: definition,
		Duration:   time.Since(started),
		Err:        resultErr,
	})
	return resultErr
}

func finishMethodObservations(
	finishes []func(MethodResult),
	result MethodResult,
) {
	for _, finish := range slices.Backward(finishes) {
		finish(result)
	}
}

func validateMethodDefinition(definition MethodDefinition) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "ID", value: definition.ID},
		{name: "module", value: definition.Module},
		{name: "service", value: definition.Service},
		{name: "method", value: definition.Method},
	}
	for _, field := range fields {
		if field.value == "" {
			return fmt.Errorf("observe method: %s is required", field.name)
		}
	}
	return nil
}
