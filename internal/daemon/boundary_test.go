package daemon

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/spice-framework/spice-agent/client"
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/grpcserver"
	"github.com/spice-framework/spice-agent/tool"
)

func TestHostConstructorsRejectInvalidOwnedDependencies(t *testing.T) {
	t.Parallel()
	if _, _, err := NewEndpointStore(endpoint.UserScope{}); err == nil {
		t.Fatal("NewEndpointStore() accepted an invalid scope")
	}
	if _, err := NewSessionStore(nil); err == nil {
		t.Fatal("NewSessionStore() accepted a nil root")
	}
	if _, err := NewSessionStore(&Root{}); err == nil {
		t.Fatal("NewSessionStore() accepted a root without context")
	}
	if _, _, err := NewRunAuthority(Properties{RunAuthorityDirectory: "relative"}); err == nil {
		t.Fatal("NewRunAuthority() accepted a relative directory")
	}
	if _, err := NewDefinitionSet(Properties{}); err == nil {
		t.Fatal("NewDefinitionSet() accepted an empty model")
	}
	if _, _, err := NewRunHost(nil, nil, nil, nil, nil, nil, agentdaemon.DefinitionSet{}, client.Limits{}); err == nil {
		t.Fatal("NewRunHost() accepted a nil root")
	}
	if _, err := NewGRPCServer(nil, endpoint.Token{}, nil, nil, client.Build{}); err == nil {
		t.Fatal("NewGRPCServer() accepted a nil root")
	}
	if _, err := NewToolDispatcher(map[string]tool.Tool{"invalid": nil}); err == nil {
		t.Fatal("NewToolDispatcher() accepted a nil tool")
	}
	if _, err := rootContext(nil); err == nil {
		t.Fatal("rootContext() accepted a nil root")
	}
}

func TestNewRuntimeValidatesEveryPublicIdentityAndBuildsMetadata(t *testing.T) {
	scope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatal(err)
	}
	token, err := endpoint.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	build, err := client.NewBuild("spice-agentd", "test", "commit", "go1.26.5")
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := client.NewProtocolVersion(1, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := &endpoint.Store{}
	server := &grpcserver.Server{}

	for _, test := range []struct {
		name     string
		scope    endpoint.UserScope
		store    *endpoint.Store
		token    endpoint.Token
		build    client.Build
		protocol client.ProtocolVersion
		server   *grpcserver.Server
	}{
		{name: "nil store", scope: scope, token: token, build: build, protocol: protocol, server: server},
		{name: "nil server", scope: scope, store: store, token: token, build: build, protocol: protocol},
		{name: "scope", store: store, token: token, build: build, protocol: protocol, server: server},
		{name: "token", scope: scope, store: store, build: build, protocol: protocol, server: server},
		{name: "build", scope: scope, store: store, token: token, protocol: protocol, server: server},
		{name: "protocol", scope: scope, store: store, token: token, build: build, server: server},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, constructErr := NewRuntime(
				test.scope, test.store, test.token, test.build, test.protocol, test.server,
			); constructErr == nil {
				t.Fatal("NewRuntime() accepted an invalid dependency")
			}
		})
	}

	runtime, err := NewRuntime(scope, store, token, build, protocol, server)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	metadata, err := runtime.endpointMetadata()
	if err != nil {
		t.Fatalf("endpointMetadata() error = %v", err)
	}
	if err = metadata.Validate(); err != nil {
		t.Fatalf("metadata validation = %v", err)
	}
	if metadata.Address() != scope.Address() || metadata.Protocol() != protocol {
		t.Fatal("endpoint metadata did not preserve runtime identity")
	}
}

func TestRuntimeBoundaryHelpersCoverCancellationAndTerminalErrors(t *testing.T) {
	t.Parallel()
	if err := (*Runtime)(nil).Start(context.Background()); err == nil {
		t.Fatal("nil Runtime.Start() succeeded")
	}
	if err := (&Runtime{}).Start(nil); err == nil { //nolint:staticcheck // Deliberate nil API-boundary regression.
		t.Fatal("Runtime.Start() accepted a nil context")
	}
	if err := (*Runtime)(nil).Stop(context.Background()); err != nil {
		t.Fatalf("nil Runtime.Stop() error = %v", err)
	}
	if err := (&Runtime{}).Stop(nil); err == nil { //nolint:staticcheck // Deliberate nil API-boundary regression.
		t.Fatal("Runtime.Stop() accepted a nil context")
	}
	if err := (*Runtime)(nil).Err(); err == nil {
		t.Fatal("nil Runtime.Err() returned nil")
	}
	select {
	case <-(*Runtime)(nil).Done():
	default:
		t.Fatal("nil Runtime.Done() was not closed")
	}
	select {
	case <-(&Runtime{}).Done():
	default:
		t.Fatal("Runtime.Done() without a serve channel was not closed")
	}

	if joined, err := waitServe(context.Background(), nil); !joined || err != nil {
		t.Fatalf("waitServe(nil) = %v, %v", joined, err)
	}
	closed := make(chan error)
	close(closed)
	if joined, err := waitServe(context.Background(), closed); !joined || err != nil {
		t.Fatalf("waitServe(closed) = %v, %v", joined, err)
	}
	serveFailure := errors.New("serve failed")
	failed := make(chan error, 1)
	failed <- serveFailure
	if joined, err := waitServe(context.Background(), failed); !joined || !errors.Is(err, serveFailure) {
		t.Fatalf("waitServe(failed) = %v, %v", joined, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if joined, err := waitServe(cancelled, make(chan error)); joined || !errors.Is(err, context.Canceled) {
		t.Fatalf("waitServe(cancelled) = %v, %v", joined, err)
	}
	if joined, err := waitDone(context.Background(), nil); !joined || err != nil {
		t.Fatalf("waitDone(nil) = %v, %v", joined, err)
	}
	if joined, err := waitDone(cancelled, make(chan struct{})); joined || !errors.Is(err, context.Canceled) {
		t.Fatalf("waitDone(cancelled) = %v, %v", joined, err)
	}
	if err := ignoreClosed(net.ErrClosed); err != nil {
		t.Fatalf("ignoreClosed(net.ErrClosed) = %v", err)
	}
	other := errors.New("other")
	if err := ignoreClosed(other); !errors.Is(err, other) {
		t.Fatalf("ignoreClosed(other) = %v", err)
	}
}

func TestRuntimeStartFailuresRetainNoPublication(t *testing.T) {
	t.Parallel()
	listenFailure := errors.New("listen failed")
	runtime := &Runtime{
		server: &testServer{log: &orderedLog{}}, serveDone: make(chan struct{}),
		services: runtimeServices{
			listen: func(string) (net.Listener, error) { return nil, listenFailure },
		},
	}
	if err := runtime.Start(context.Background()); !errors.Is(err, listenFailure) {
		t.Fatalf("listen Start() error = %v", err)
	}

	metadataFailure := errors.New("metadata failed")
	log := &orderedLog{}
	listener := newTestListener(log)
	runtime = testRuntime(listener, &testServer{log: log}, &testPublication{log: log})
	runtime.services.metadata = func() (endpoint.Metadata, error) {
		return endpoint.Metadata{}, metadataFailure
	}
	if err := runtime.Start(context.Background()); !errors.Is(err, metadataFailure) {
		t.Fatalf("metadata Start() error = %v", err)
	}

	publishFailure := errors.New("publish failed")
	log = &orderedLog{}
	listener = newTestListener(log)
	runtime = testRuntime(listener, &testServer{log: log}, &testPublication{log: log})
	runtime.services.publish = func(context.Context, endpoint.Metadata) (runtimePublication, error) {
		return nil, publishFailure
	}
	if err := runtime.Start(context.Background()); !errors.Is(err, publishFailure) {
		t.Fatalf("publish Start() error = %v", err)
	}
}

func TestRuntimeRejectsServerExitBeforePublication(t *testing.T) {
	t.Parallel()
	server := immediateServer{}
	runtime := &Runtime{server: server, serveDone: make(chan struct{})}
	runtime.services = runtimeServices{
		listen: func(string) (net.Listener, error) { return idleListener{}, nil },
		metadata: func() (endpoint.Metadata, error) {
			return endpoint.Metadata{}, nil
		},
		publish: func(context.Context, endpoint.Metadata) (runtimePublication, error) {
			t.Fatal("published a stopped server")
			return nil, errors.New("unreachable publication")
		},
	}
	if err := runtime.Start(context.Background()); err == nil {
		t.Fatal("Start() accepted a server that exited before publication")
	}
}

type immediateServer struct{}

func (immediateServer) Serve(net.Listener) error       { return nil }
func (immediateServer) Shutdown(context.Context) error { return nil }

type idleListener struct{}

func (idleListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (idleListener) Close() error              { return nil }
func (idleListener) Addr() net.Addr            { return testAddress("idle") }
