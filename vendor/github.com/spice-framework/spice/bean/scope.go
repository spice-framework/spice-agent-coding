package bean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/spice-framework/spice/lifecycle"
)

// ScopeKind identifies an explicit runtime ownership boundary.
type ScopeKind string

const (
	// ScopeRequest owns values for one HTTP or message request.
	ScopeRequest ScopeKind = "request"
	// ScopeSession owns values for one caller-managed session.
	ScopeSession ScopeKind = "session"
)

// Scope owns cleanup leases in reverse construction order.
type Scope struct {
	kind      ScopeKind
	mu        sync.Mutex
	cleanups  []lifecycle.Cleanup
	closed    bool
	closeErr  error
	closeDone chan struct{}
}

// NewScope constructs an isolated request or session scope.
func NewScope(kind ScopeKind) (*Scope, error) {
	if kind != ScopeRequest && kind != ScopeSession {
		return nil, fmt.Errorf("bean scope kind %q is unsupported", kind)
	}
	return &Scope{kind: kind, closeDone: make(chan struct{})}, nil
}

// Kind returns the immutable scope kind.
func (scope *Scope) Kind() ScopeKind {
	if scope == nil {
		return ""
	}
	return scope.kind
}

// Register assigns cleanup ownership to the scope.
func (scope *Scope) Register(cleanup lifecycle.Cleanup) error {
	if scope == nil {
		return errors.New("bean scope is nil")
	}
	if cleanup == nil {
		return errors.New("bean scope cleanup is nil")
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.closed {
		return errors.New("bean scope is closed")
	}
	scope.cleanups = append(scope.cleanups, cleanup)
	return nil
}

// Close is idempotent and releases owned values in reverse order.
func (scope *Scope) Close(ctx context.Context) error {
	if scope == nil {
		return errors.New("bean scope is nil")
	}
	if ctx == nil {
		return errors.New("bean scope close context is nil")
	}
	scope.mu.Lock()
	if scope.closeDone == nil {
		scope.mu.Unlock()
		return errors.New("bean scope is not initialized")
	}
	if scope.closed {
		done := scope.closeDone
		scope.mu.Unlock()
		<-done
		scope.mu.Lock()
		result := scope.closeErr
		scope.mu.Unlock()
		return result
	}
	scope.closed = true
	cleanups := append([]lifecycle.Cleanup(nil), scope.cleanups...)
	scope.cleanups = nil
	scope.mu.Unlock()

	var result error
	for _, cleanup := range slices.Backward(cleanups) {
		if err := cleanup(ctx); err != nil {
			result = errors.Join(result, err)
		}
	}
	scope.mu.Lock()
	scope.closeErr = result
	close(scope.closeDone)
	scope.mu.Unlock()
	return result
}

type scopeContextKey struct {
	kind ScopeKind
}

// WithScope attaches a matching explicit scope to ctx.
func WithScope(ctx context.Context, scope *Scope) (context.Context, error) {
	if ctx == nil {
		return nil, errors.New("bean scope context is nil")
	}
	if scope == nil {
		return nil, errors.New("bean scope is nil")
	}
	scope.mu.Lock()
	closed := scope.closed
	scope.mu.Unlock()
	if closed {
		return nil, errors.New("bean scope is closed")
	}
	return context.WithValue(
		ctx,
		scopeContextKey{kind: scope.kind},
		scope,
	), nil
}

func scopeFromContext(
	ctx context.Context,
	kind ScopeKind,
) (*Scope, error) {
	if ctx == nil {
		return nil, errors.New("bean scope context is nil")
	}
	scope, ok := ctx.Value(scopeContextKey{kind: kind}).(*Scope)
	if !ok || scope == nil || scope.kind != kind {
		return nil, fmt.Errorf(
			"bean %s scope is not present in context",
			kind,
		)
	}
	scope.mu.Lock()
	closed := scope.closed
	scope.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("bean %s scope is closed", kind)
	}
	return scope, nil
}

type scopedEntry[T any] struct {
	ready chan struct{}
	value T
	err   error
}

// Scoped is a typed generated-scope factory. Each instance represents exactly
// one bean declaration, so lookup never uses a runtime bean name.
type Scoped[T any] struct {
	kind    ScopeKind
	factory AcquireFunc[T]
	mu      sync.Mutex
	values  map[*Scope]*scopedEntry[T]
}

// NewScoped constructs a typed request- or session-scoped factory.
func NewScoped[T any](
	kind ScopeKind,
	factory AcquireFunc[T],
) *Scoped[T] {
	return &Scoped[T]{
		kind:    kind,
		factory: factory,
		values:  make(map[*Scope]*scopedEntry[T]),
	}
}

// Provider exposes the scoped factory through the standard typed handle.
func (scoped *Scoped[T]) Provider() Provider[T] {
	return NewProvider(scoped.acquire)
}

func (scoped *Scoped[T]) acquire(
	ctx context.Context,
) (T, lifecycle.Cleanup, error) {
	if scoped == nil {
		var zero T
		return zero, nil, errors.New("bean scoped factory is nil")
	}
	if scoped.kind != ScopeRequest && scoped.kind != ScopeSession {
		var zero T
		return zero, nil, fmt.Errorf(
			"bean scoped factory kind %q is unsupported",
			scoped.kind,
		)
	}
	if scoped.factory == nil {
		var zero T
		return zero, nil, errors.New("bean scoped factory function is nil")
	}
	scope, err := scopeFromContext(ctx, scoped.kind)
	if err != nil {
		var zero T
		return zero, nil, err
	}

	scoped.mu.Lock()
	entry, found := scoped.values[scope]
	if !found {
		entry = &scopedEntry[T]{ready: make(chan struct{})}
		scoped.values[scope] = entry
	}
	scoped.mu.Unlock()
	if found {
		select {
		case <-entry.ready:
			return entry.value, noopCleanup(), entry.err
		case <-ctx.Done():
			var zero T
			return zero, nil, ctx.Err()
		}
	}

	value, cleanup, acquireErr := scoped.factory(ctx)
	if acquireErr == nil {
		acquireErr = scope.Register(func(closeContext context.Context) error {
			defer scoped.remove(scope)
			if cleanup == nil {
				return nil
			}
			return cleanup(closeContext)
		})
	}
	if acquireErr != nil {
		if cleanup != nil {
			acquireErr = errors.Join(
				acquireErr,
				cleanup(context.WithoutCancel(ctx)),
			)
		}
		scoped.remove(scope)
	}
	entry.value = value
	entry.err = acquireErr
	close(entry.ready)
	if acquireErr != nil {
		var zero T
		return zero, nil, acquireErr
	}
	return value, noopCleanup(), nil
}

func (scoped *Scoped[T]) remove(scope *Scope) {
	scoped.mu.Lock()
	delete(scoped.values, scope)
	scoped.mu.Unlock()
}

func noopCleanup() lifecycle.Cleanup {
	return func(context.Context) error { return nil }
}

// ScopeErrorHandler observes request-scope cleanup failures.
type ScopeErrorHandler func(*http.Request, error)

// RequestScopeMiddleware creates and closes one scope per HTTP request.
func RequestScopeMiddleware(
	next http.Handler,
	onError ScopeErrorHandler,
) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("bean request scope handler is nil")
	}
	return http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		scope, err := NewScope(ScopeRequest)
		if err != nil {
			if onError != nil {
				onError(request, err)
			}
			http.Error(
				writer,
				"request scope unavailable",
				http.StatusInternalServerError,
			)
			return
		}
		ctx, err := WithScope(request.Context(), scope)
		if err != nil {
			if onError != nil {
				onError(request, err)
			}
			http.Error(
				writer,
				"request scope unavailable",
				http.StatusInternalServerError,
			)
			return
		}
		defer func() {
			closeErr := scope.Close(
				context.WithoutCancel(request.Context()),
			)
			if closeErr != nil && onError != nil {
				onError(request, closeErr)
			}
		}()
		next.ServeHTTP(writer, request.WithContext(ctx))
	}), nil
}
