package daemon

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

func TestRuntimePublishesAfterAcceptAndStopsInOwnershipOrder(t *testing.T) {
	t.Parallel()
	log := &orderedLog{}
	listener := newTestListener(log)
	server := &testServer{log: log}
	publication := &testPublication{log: log}
	runtime := testRuntime(listener, server, publication)

	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !listener.acceptedValue() {
		t.Fatal("endpoint published before the server entered Accept")
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got, want := log.values(), []string{"accept", "publish", "withdraw", "shutdown", "listener-close"}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle order = %v, want %v", got, want)
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("serve loop was not joined")
	}
	if err := runtime.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
	if err := runtime.Start(context.Background()); err == nil {
		t.Fatal("second Start() unexpectedly succeeded")
	}
}

func TestRuntimeReportsAsynchronousServeFailureAndStillReleasesOwnership(t *testing.T) {
	t.Parallel()
	serveFailure := errors.New("transport failed")
	log := &orderedLog{}
	listener := newTestListener(log)
	server := &testServer{log: log, failure: make(chan error, 1)}
	publication := &testPublication{log: log}
	runtime := testRuntime(listener, server, publication)

	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	server.failure <- serveFailure
	select {
	case <-runtime.Done():
	case <-time.After(time.Second):
		t.Fatal("serve failure was not observed")
	}
	if err := runtime.Err(); !errors.Is(err, serveFailure) {
		t.Fatalf("Err() = %v, want %v", err, serveFailure)
	}
	if err := runtime.Stop(context.Background()); !errors.Is(err, serveFailure) {
		t.Fatalf("Stop() error = %v, want %v", err, serveFailure)
	}
	runtime.mu.Lock()
	state := runtime.state
	runtime.mu.Unlock()
	if state != runtimeStopped {
		t.Fatalf("state = %v, want stopped", state)
	}
}

func TestRuntimeRetriesPublicationWithdrawalWithoutLosingOwnership(t *testing.T) {
	t.Parallel()
	withdrawalFailure := errors.New("withdrawal blocked")
	log := &orderedLog{}
	listener := newTestListener(log)
	server := &testServer{log: log}
	publication := &testPublication{log: log, failures: []error{withdrawalFailure, nil}}
	runtime := testRuntime(listener, server, publication)

	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := runtime.Stop(context.Background()); !errors.Is(err, withdrawalFailure) {
		t.Fatalf("first Stop() error = %v, want %v", err, withdrawalFailure)
	}
	runtime.mu.Lock()
	publicationRetained := runtime.publication != nil
	state := runtime.state
	runtime.mu.Unlock()
	if !publicationRetained || state != runtimeStopping {
		t.Fatalf("failed withdrawal lost ownership: retained=%v state=%v", publicationRetained, state)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if publication.calls != 2 {
		t.Fatalf("publication close calls = %d, want 2", publication.calls)
	}
}

func TestRuntimeCleansUpWhenPublicationIsCancelled(t *testing.T) {
	t.Parallel()
	log := &orderedLog{}
	listener := newTestListener(log)
	server := &testServer{log: log}
	runtime := testRuntime(listener, server, &testPublication{log: log})
	runtime.services.publish = func(ctx context.Context, _ endpoint.Metadata) (runtimePublication, error) {
		log.add("publish")
		<-ctx.Done()
		return nil, context.Cause(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want cancellation", err)
	}
	entries := log.values()
	if slices.Contains(entries, "publish") || !slices.Contains(entries, "shutdown") ||
		!slices.Contains(entries, "listener-close") {
		t.Fatalf("failed-start cleanup did not contain ownership: %v", entries)
	}
}

func testRuntime(listener *testListener, server *testServer, publication *testPublication) *Runtime {
	runtime := &Runtime{
		server: server, activation: readyRuntimePluginActivation(),
		serveDone: make(chan struct{}),
	}
	runtime.services = runtimeServices{
		listen: func(string) (net.Listener, error) { return listener, nil },
		metadata: func() (endpoint.Metadata, error) {
			return endpoint.Metadata{}, nil
		},
		publish: func(context.Context, endpoint.Metadata) (runtimePublication, error) {
			publication.log.add("publish")
			return publication, nil
		},
	}
	return runtime
}

func readyRuntimePluginActivation() *RuntimePluginActivation {
	return &RuntimePluginActivation{state: runtimePluginActivationReady}
}

type orderedLog struct {
	mu      sync.Mutex
	entries []string
}

func (log *orderedLog) add(value string) {
	log.mu.Lock()
	log.entries = append(log.entries, value)
	log.mu.Unlock()
}

func (log *orderedLog) values() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return slices.Clone(log.entries)
}

type testListener struct {
	log      *orderedLog
	accepted chan struct{}
	closed   chan struct{}
	once     sync.Once
}

func newTestListener(log *orderedLog) *testListener {
	return &testListener{log: log, accepted: make(chan struct{}), closed: make(chan struct{})}
}

func (listener *testListener) Accept() (net.Conn, error) {
	listener.log.add("accept")
	listener.once.Do(func() { close(listener.accepted) })
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *testListener) Close() error {
	listener.log.add("listener-close")
	select {
	case <-listener.closed:
	default:
		close(listener.closed)
	}
	return nil
}

func (*testListener) Addr() net.Addr { return testAddress("local") }

func (listener *testListener) acceptedValue() bool {
	select {
	case <-listener.accepted:
		return true
	default:
		return false
	}
}

type testAddress string

func (address testAddress) Network() string { return string(address) }
func (address testAddress) String() string  { return string(address) }

type testServer struct {
	log     *orderedLog
	failure chan error
}

func (server *testServer) Serve(listener net.Listener) error {
	if server.failure != nil {
		go func() {
			_, acceptErr := listener.Accept()
			if acceptErr != nil && !errors.Is(acceptErr, net.ErrClosed) {
				server.log.add("accept-error")
			}
		}()
		return <-server.failure
	}
	_, err := listener.Accept()
	return err
}

func (server *testServer) Shutdown(context.Context) error {
	server.log.add("shutdown")
	return nil
}

type testPublication struct {
	log      *orderedLog
	failures []error
	calls    int
}

func (publication *testPublication) CloseContext(context.Context) error {
	publication.log.add("withdraw")
	publication.calls++
	if publication.calls <= len(publication.failures) {
		return publication.failures[publication.calls-1]
	}
	return nil
}
