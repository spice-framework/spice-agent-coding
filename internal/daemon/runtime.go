package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/grpcserver"
	"github.com/spice-framework/spice-agent/daemon/localipc"
	"google.golang.org/grpc"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"
// @import { OnStart, OnStop } from "github.com/spice-framework/spice/annotation/lifecycle"

type runtimeState uint8

const (
	runtimeNew runtimeState = iota
	runtimeStarting
	runtimeRunning
	runtimeStopping
	runtimeStopped
)

type runtimeServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
}

type runtimePublication interface {
	CloseContext(context.Context) error
}

// ListenerFactory owns local transport creation. The interface keeps Runtime
// transport-agnostic and gives process-level acceptance tests a typed seam for
// faulting only an established client connection without replacing the daemon
// or bypassing its generated Spice graph.
type ListenerFactory interface {
	Listen(string) (net.Listener, error)
}

type localListenerFactory struct{}

func (localListenerFactory) Listen(address string) (net.Listener, error) {
	return localipc.Listen(address)
}

// NewListenerFactory contributes the production current-user local IPC
// listener through ordinary generated constructor injection.
//
// @Bean(name="daemonListenerFactory")
func NewListenerFactory() ListenerFactory { return localListenerFactory{} }

type runtimeServices struct {
	listen   func(string) (net.Listener, error)
	publish  func(context.Context, endpoint.Metadata) (runtimePublication, error)
	metadata func() (endpoint.Metadata, error)
}

// Runtime owns listener publication and graceful transport draining. The
// generated application invokes its lifecycle hooks in ordinary Go.
type Runtime struct {
	scope      endpoint.UserScope
	token      endpoint.Token
	build      client.Build
	protocol   client.ProtocolVersion
	server     runtimeServer
	activation *RuntimePluginActivation
	services   runtimeServices

	lifecycle   sync.Mutex
	mu          sync.Mutex
	state       runtimeState
	listener    net.Listener
	publication runtimePublication
	serveDone   chan struct{}
	serveErr    error
}

// NewRuntime receives every runtime dependency through generated Spice
// constructor injection.
//
// @Bean(name="daemonRuntime")
func NewRuntime(
	scope endpoint.UserScope,
	store *endpoint.Store,
	token endpoint.Token,
	build client.Build,
	protocol client.ProtocolVersion,
	server *grpcserver.Server,
	activation *RuntimePluginActivation,
	listenerFactory ListenerFactory,
) (*Runtime, error) {
	if store == nil || server == nil || activation == nil || listenerFactory == nil {
		return nil, errors.New("daemon runtime requires endpoint store, gRPC server, runtime plugin activation, and listener factory")
	}
	if err := scope.Validate(); err != nil {
		return nil, fmt.Errorf("validate endpoint scope: %w", err)
	}
	if err := token.Validate(); err != nil {
		return nil, err
	}
	if err := build.Validate(); err != nil {
		return nil, err
	}
	if err := protocol.Validate(); err != nil {
		return nil, err
	}
	runtime := &Runtime{
		scope: scope, token: token, build: build,
		protocol: protocol, server: server, activation: activation,
		serveDone: make(chan struct{}),
	}
	runtime.services = runtimeServices{
		listen: listenerFactory.Listen,
		publish: func(ctx context.Context, metadata endpoint.Metadata) (runtimePublication, error) {
			return store.Publish(ctx, metadata)
		},
		metadata: runtime.endpointMetadata,
	}
	return runtime, nil
}

// Start binds local IPC, starts the authenticated server, and publishes only
// after the server has entered its accept loop.
//
// @OnStart
func (runtime *Runtime) Start(ctx context.Context) error {
	if runtime == nil || ctx == nil {
		return errors.New("daemon runtime start requires a runtime and context")
	}
	runtime.lifecycle.Lock()
	defer runtime.lifecycle.Unlock()
	if err := runtime.activation.PublicationReady(); err != nil {
		return err
	}
	runtime.mu.Lock()
	if runtime.state != runtimeNew {
		runtime.mu.Unlock()
		return errors.New("daemon runtime cannot start from its current state")
	}
	runtime.state = runtimeStarting
	runtime.mu.Unlock()

	listener, err := runtime.services.listen(runtime.scope.Address())
	if err != nil {
		runtime.resetAfterStartFailure()
		return fmt.Errorf("listen on local endpoint: %w", err)
	}
	ready := &readyListener{Listener: listener, ready: make(chan struct{})}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- runtime.server.Serve(ready)
		close(serveResult)
	}()
	err = waitForPublicationReadiness(ctx, ready.ready, serveResult)
	if err != nil {
		cleanupErr := stopUnpublished(runtime.server, listener, serveResult)
		runtime.resetAfterStartFailure()
		return errors.Join(err, cleanupErr)
	}

	metadata, err := runtime.services.metadata()
	if err != nil {
		cleanupErr := stopUnpublished(runtime.server, listener, serveResult)
		runtime.resetAfterStartFailure()
		return errors.Join(err, cleanupErr)
	}
	publication, err := runtime.services.publish(ctx, metadata)
	if err != nil {
		cleanupErr := stopUnpublished(runtime.server, listener, serveResult)
		runtime.resetAfterStartFailure()
		return errors.Join(fmt.Errorf("publish local endpoint: %w", err), cleanupErr)
	}

	runtime.mu.Lock()
	runtime.listener = listener
	runtime.publication = publication
	runtime.state = runtimeRunning
	runtime.mu.Unlock()
	go runtime.observeServe(serveResult)
	return nil
}

func waitForPublicationReadiness(
	ctx context.Context,
	ready <-chan struct{},
	serveResult <-chan error,
) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case err := <-serveResult:
		if err == nil {
			return errors.New("gRPC server stopped before endpoint publication")
		}
		return err
	case <-ready:
		select {
		case err := <-serveResult:
			if err = normalizedServeError(err); err == nil {
				return errors.New("gRPC server stopped before endpoint publication")
			}
			return err
		default:
			return nil
		}
	}
}

// Done closes if the transport exits after successful publication. It lets the
// distribution runner turn an asynchronous serving failure into orderly
// application shutdown instead of leaving live endpoint metadata behind.
func (runtime *Runtime) Done() <-chan struct{} {
	if runtime == nil || runtime.serveDone == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return runtime.serveDone
}

// Err returns the redacted serving failure observed after publication.
func (runtime *Runtime) Err() error {
	if runtime == nil {
		return errors.New("daemon runtime is unavailable")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.serveErr
}

// Stop withdraws publication before draining the server and joining its serve
// loop. Failed publication cleanup remains visible and a later Stop can retry.
//
// @OnStop
func (runtime *Runtime) Stop(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("daemon runtime stop context is required")
	}
	runtime.lifecycle.Lock()
	defer runtime.lifecycle.Unlock()
	runtime.mu.Lock()
	if runtime.state == runtimeNew || runtime.state == runtimeStopped {
		runtime.mu.Unlock()
		return nil
	}
	publication := runtime.publication
	listener := runtime.listener
	serveDone := runtime.serveDone
	runtime.state = runtimeStopping
	runtime.mu.Unlock()

	var result error
	publicationClosed := publication == nil
	if publication != nil {
		if err := publication.CloseContext(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("withdraw endpoint publication: %w", err))
		} else {
			publicationClosed = true
		}
	}
	result = errors.Join(result, runtime.server.Shutdown(ctx))
	if listener != nil {
		result = errors.Join(result, ignoreClosed(listener.Close()))
	}
	joined, serveErr := waitDone(ctx, serveDone)
	result = errors.Join(result, serveErr)
	runtime.mu.Lock()
	result = errors.Join(result, runtime.serveErr)
	runtime.mu.Unlock()

	runtime.mu.Lock()
	if publicationClosed {
		runtime.publication = nil
	}
	if publicationClosed && joined {
		runtime.listener = nil
		runtime.state = runtimeStopped
	}
	runtime.mu.Unlock()
	return result
}

func (runtime *Runtime) resetAfterStartFailure() {
	runtime.mu.Lock()
	runtime.state = runtimeStopped
	runtime.mu.Unlock()
}

func (runtime *Runtime) endpointMetadata() (endpoint.Metadata, error) {
	processID := os.Getpid()
	if processID <= 0 || uint64(processID) > math.MaxUint32 {
		return endpoint.Metadata{}, errors.New("daemon process ID is invalid")
	}
	process, err := endpoint.GenerateProcess(uint32(processID), time.Now().UTC()) // #nosec G115 -- bounded immediately above.
	if err != nil {
		return endpoint.Metadata{}, err
	}
	return endpoint.NewMetadata(
		runtime.scope.Transport(), runtime.scope.Address(), runtime.token,
		runtime.build, runtime.protocol, process,
	)
}

func (runtime *Runtime) observeServe(result <-chan error) {
	err, ok := <-result
	if !ok {
		err = nil
	}
	err = normalizedServeError(err)
	runtime.mu.Lock()
	if err != nil {
		runtime.serveErr = err
	}
	runtime.mu.Unlock()
	close(runtime.serveDone)
}

func stopUnpublished(server runtimeServer, listener net.Listener, serveResult <-chan error) error {
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result error
	if server != nil {
		result = errors.Join(result, server.Shutdown(shutdownContext))
	}
	if listener != nil {
		result = errors.Join(result, ignoreClosed(listener.Close()))
	}
	_, serveErr := waitServe(shutdownContext, serveResult)
	return errors.Join(result, serveErr)
}

func waitServe(ctx context.Context, result <-chan error) (bool, error) {
	if result == nil {
		return true, nil
	}
	select {
	case <-ctx.Done():
		return false, context.Cause(ctx)
	case err, ok := <-result:
		if !ok {
			return true, nil
		}
		return true, normalizedServeError(err)
	}
}

func waitDone(ctx context.Context, done <-chan struct{}) (bool, error) {
	if done == nil {
		return true, nil
	}
	select {
	case <-ctx.Done():
		return false, context.Cause(ctx)
	case <-done:
		return true, nil
	}
}

func normalizedServeError(err error) error {
	if err == nil || errors.Is(err, grpc.ErrServerStopped) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("serve local endpoint: %w", err)
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type readyListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (listener *readyListener) Accept() (net.Conn, error) {
	listener.once.Do(func() { close(listener.ready) })
	return listener.Listener.Accept()
}
