// Package bean provides typed, reflection-free dependency handles used by
// generated Spice applications.
package bean

import (
	"context"
	"errors"
	"sync"

	"github.com/spice-framework/spice/lifecycle"
)

// Optional contains either one unambiguous typed bean or no bean. Ambiguity is
// rejected by the compiler and can never be represented at runtime.
type Optional[T any] struct {
	value   T
	present bool
}

// Some constructs a present optional value.
func Some[T any](value T) Optional[T] {
	return Optional[T]{value: value, present: true}
}

// None constructs an absent optional value.
func None[T any]() Optional[T] {
	return Optional[T]{}
}

// Get returns the value and whether it is present.
func (optional Optional[T]) Get() (T, bool) {
	return optional.value, optional.present
}

// Lazy resolves one typed value at most once. The first result, including an
// error, is stable for the lifetime of the owning generated scope.
type Lazy[T any] struct {
	once    *sync.Once
	resolve func(context.Context) (T, error)
	value   T
	err     error
}

// NewLazy constructs an isolated lazy dependency.
func NewLazy[T any](
	resolve func(context.Context) (T, error),
) Lazy[T] {
	return Lazy[T]{once: &sync.Once{}, resolve: resolve}
}

// Get resolves and returns the value. A nil context or resolver fails closed.
func (lazy *Lazy[T]) Get(ctx context.Context) (T, error) {
	if lazy == nil {
		var zero T
		return zero, errors.New("bean lazy handle is nil")
	}
	if ctx == nil {
		var zero T
		return zero, errors.New("bean lazy context is nil")
	}
	if lazy.once == nil {
		lazy.once = &sync.Once{}
	}
	lazy.once.Do(func() {
		if lazy.resolve == nil {
			lazy.err = errors.New("bean lazy resolver is nil")
			return
		}
		lazy.value, lazy.err = lazy.resolve(ctx)
	})
	return lazy.value, lazy.err
}

// AcquireFunc constructs or leases one typed bean.
type AcquireFunc[T any] func(
	context.Context,
) (T, lifecycle.Cleanup, error)

// OverrideFactory constructs one test- or embedding-owned replacement bean.
// Generated applications register returned cleanup in the original bean's
// module lifecycle and never place the value in a runtime container.
type OverrideFactory[T any] func(
	context.Context,
) (T, lifecycle.Cleanup, error)

// Override is an explicit typed replacement for one generated singleton bean.
// Its zero value is disabled, so production ApplicationOptions remain concise.
type Override[T any] struct {
	factory OverrideFactory[T]
	enabled bool
}

// Replace returns an enabled override for one caller-owned value.
func Replace[T any](value T) Override[T] {
	return Override[T]{
		enabled: true,
		factory: func(context.Context) (T, lifecycle.Cleanup, error) {
			return value, nil, nil
		},
	}
}

// ReplaceFactory returns an enabled override with caller-owned construction and
// cleanup. A nil factory fails when the generated application is constructed.
func ReplaceFactory[T any](factory OverrideFactory[T]) Override[T] {
	return Override[T]{factory: factory, enabled: true}
}

// Enabled reports whether generated construction should use this replacement.
func (override Override[T]) Enabled() bool {
	return override.enabled
}

// Acquire constructs the replacement. Generated code calls it only when
// Enabled returns true.
func (override Override[T]) Acquire(
	ctx context.Context,
) (T, lifecycle.Cleanup, error) {
	if ctx == nil {
		var zero T
		return zero, nil, errors.New("bean override context is nil")
	}
	if !override.enabled {
		var zero T
		return zero, nil, errors.New("bean override is disabled")
	}
	if override.factory == nil {
		var zero T
		return zero, nil, errors.New("bean override factory is nil")
	}
	return override.factory(ctx)
}

// Provider is an explicit typed acquisition handle. It contains no global
// registry and performs no reflection or string lookup.
type Provider[T any] struct {
	acquire AcquireFunc[T]
}

// NewProvider constructs a typed provider handle.
func NewProvider[T any](acquire AcquireFunc[T]) Provider[T] {
	return Provider[T]{acquire: acquire}
}

// Acquire obtains one value and an idempotent caller-owned cleanup lease.
func (provider Provider[T]) Acquire(
	ctx context.Context,
) (T, lifecycle.Cleanup, error) {
	if ctx == nil {
		var zero T
		return zero, nil, errors.New("bean provider context is nil")
	}
	if provider.acquire == nil {
		var zero T
		return zero, nil, errors.New("bean provider acquire function is nil")
	}
	value, cleanup, err := provider.acquire(ctx)
	if err != nil {
		var zero T
		return zero, nil, err
	}
	return value, onceCleanup(cleanup), nil
}

func onceCleanup(cleanup lifecycle.Cleanup) lifecycle.Cleanup {
	var once sync.Once
	var result error
	return func(ctx context.Context) error {
		once.Do(func() {
			if cleanup != nil {
				result = cleanup(ctx)
			}
		})
		return result
	}
}
