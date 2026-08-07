package terminalconnector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

func TestExplicitConnectorIsLazyValidatedAndRedacted(t *testing.T) {
	if connector, err := NewExplicit(nil, "/private/endpoint"); err == nil || connector != nil {
		t.Fatalf("nil store connector = %v, %v", connector, err)
	}
	scope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatal(err)
	}
	store, err := scope.OpenStore(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	connector, err := NewExplicit(store, scope.Address())
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		fmt.Sprint(connector), fmt.Sprintf("%#v", connector), connector.LogValue().String(),
	} {
		if strings.Contains(rendered, scope.Address()) || !strings.Contains(rendered, "[REDACTED]") {
			t.Fatalf("unsafe formatting = %q", rendered)
		}
	}
	encoded, err := json.Marshal(connector)
	if err != nil || strings.Contains(string(encoded), scope.Address()) {
		t.Fatalf("unsafe JSON = %q, %v", encoded, err)
	}
	if session, initErr := connector.Initialize(t.Context(), client.InitializeRequest{}); initErr == nil || session != nil {
		t.Fatalf("invalid initialization = %T, %v", session, initErr)
	}
	if err = connector.Close(); err != nil {
		t.Fatal(err)
	}
	if err = connector.Close(); err != nil {
		t.Fatal(err)
	}
	if session, initErr := connector.Initialize(t.Context(), client.InitializeRequest{}); initErr == nil || session != nil {
		t.Fatalf("closed initialization = %T, %v", session, initErr)
	}
}

func TestExplicitConnectorRejectsNilAndCancelledContexts(t *testing.T) {
	scope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatal(err)
	}
	store, err := scope.OpenStore(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	connector, err := NewExplicit(store, scope.Address())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := connector.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if _, err = connector.Initialize(nil, client.InitializeRequest{}); err == nil { //nolint:staticcheck // Boundary deliberately rejects nil context.
		t.Fatal("nil initialization context succeeded")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = connector.Initialize(ctx, client.InitializeRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initialization = %v", err)
	}
}

func TestExplicitConnectorInitializationOutcomes(t *testing.T) {
	t.Parallel()
	request := validInitializeRequest(t)
	discoveryFailure := errors.New("private discovery failure")
	connector := &Explicit{discovery: &fakeDiscovery{err: discoveryFailure}}
	if session, err := connector.Initialize(t.Context(), request); session != nil ||
		err == nil || err.Error() != "discover explicit local endpoint" || !errors.Is(err, discoveryFailure) {
		t.Fatalf("discovery failure = %T, %v", session, err)
	}
	connector = &Explicit{discovery: &fakeDiscovery{}}
	if session, err := connector.Initialize(t.Context(), request); session != nil ||
		err == nil || !strings.Contains(err.Error(), "no connector") {
		t.Fatalf("nil discovery = %T, %v", session, err)
	}

	initializeFailure := errors.New("initialize failed")
	connector = &Explicit{discovery: &fakeDiscovery{connector: fakeConnector{err: initializeFailure}}}
	if session, err := connector.Initialize(t.Context(), request); session != nil || !errors.Is(err, initializeFailure) {
		t.Fatalf("initialize failure = %T, %v", session, err)
	}
	connector = &Explicit{discovery: &fakeDiscovery{connector: fakeConnector{}}}
	if session, err := connector.Initialize(t.Context(), request); session != nil ||
		err == nil || !strings.Contains(err.Error(), "no session") {
		t.Fatalf("nil session = %T, %v", session, err)
	}

	session := &fakeSession{}
	connector = &Explicit{discovery: &fakeDiscovery{connector: fakeConnector{session: session}}}
	initialized, err := connector.Initialize(t.Context(), request)
	if err != nil || initialized != session {
		t.Fatalf("initialized session = %T, %v", initialized, err)
	}
}

func TestExplicitConnectorClosesSessionWhenFencedDuringInitialization(t *testing.T) {
	t.Parallel()
	request := validInitializeRequest(t)
	session := &fakeSession{}
	discovery := &fakeDiscovery{}
	connector := &Explicit{discovery: discovery}
	discovery.connector = connectorFunc(func(context.Context, client.InitializeRequest) (client.Session, error) {
		if err := connector.Close(); err != nil {
			t.Fatal(err)
		}
		return session, nil
	})
	initialized, err := connector.Initialize(t.Context(), request)
	if initialized != nil || !errors.Is(err, client.ErrClosed) || session.closeCalls != 1 {
		t.Fatalf("fenced initialization = %T, %v, closes %d", initialized, err, session.closeCalls)
	}
}

func TestExplicitConnectorFormattingAndCloseFailures(t *testing.T) {
	t.Parallel()
	closeFailure := errors.New("close failed")
	discovery := &fakeDiscovery{closeErr: closeFailure}
	connector := &Explicit{discovery: discovery}
	if connector.GoString() != connector.String() {
		t.Fatal("GoString differs from redacted String")
	}
	if err := connector.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("close failure = %v", err)
	}
	if err := connector.Close(); !errors.Is(err, closeFailure) || discovery.closeCalls != 1 {
		t.Fatalf("repeated close = %v, calls %d", err, discovery.closeCalls)
	}
	if err := (*Explicit)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	failure := &opaqueError{message: "safe", cause: closeFailure}
	if failure.Error() != "safe" || !errors.Is(failure, closeFailure) || (*opaqueError)(nil).Unwrap() != nil {
		t.Fatal("opaque error contract is invalid")
	}
}

func validInitializeRequest(t *testing.T) client.InitializeRequest {
	t.Helper()
	version, err := client.NewProtocolVersion(1, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := client.NewProtocolRange(version, version)
	if err != nil {
		t.Fatal(err)
	}
	build, err := client.NewBuild("test", "v1", "commit", "go1.26.5")
	if err != nil {
		t.Fatal(err)
	}
	limits, err := client.NewLimits(1024, 8, 8, 1024, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.NewInitializationAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.NewInitializeRequestWithAttempt(
		protocol, build, []string{"events"}, []string{"events"}, limits, attempt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type fakeDiscovery struct {
	connector  client.Connector
	err        error
	closeErr   error
	closeCalls int
}

func (discovery *fakeDiscovery) Discover(context.Context) (client.Connector, error) {
	return discovery.connector, discovery.err
}

func (discovery *fakeDiscovery) Close() error {
	discovery.closeCalls++
	return discovery.closeErr
}

type fakeConnector struct {
	session client.Session
	err     error
}

func (connector fakeConnector) Initialize(context.Context, client.InitializeRequest) (client.Session, error) {
	return connector.session, connector.err
}

type connectorFunc func(context.Context, client.InitializeRequest) (client.Session, error)

func (function connectorFunc) Initialize(ctx context.Context, request client.InitializeRequest) (client.Session, error) {
	return function(ctx, request)
}

type fakeSession struct{ closeCalls int }

func (*fakeSession) Connection() client.Connection { return client.Connection{} }
func (*fakeSession) Start(context.Context, client.StartRequest) (client.StartResult, error) {
	return client.StartResult{}, nil
}

func (*fakeSession) Events(context.Context, client.Cursor, client.EventStreamOptions) (client.EventStream, error) {
	return nil, errors.New("fake event stream is unavailable")
}

func (*fakeSession) Interactions(context.Context, client.InteractionStreamOptions) (client.InteractionStream, error) {
	return nil, errors.New("fake interaction stream is unavailable")
}

func (*fakeSession) Cancel(context.Context, client.CancelRequest) (client.CancelResult, error) {
	return client.CancelResult{}, nil
}

func (*fakeSession) Respond(context.Context, client.RespondRequest) (client.RespondResult, error) {
	return client.RespondResult{}, nil
}

func (*fakeSession) Suspend(context.Context, client.RunMutation) (client.SuspendResult, error) {
	return client.SuspendResult{}, nil
}

func (*fakeSession) Resume(context.Context, client.RunMutation) (client.ResumeResult, error) {
	return client.ResumeResult{}, nil
}

func (*fakeSession) Export(context.Context, client.RunRef) (client.Snapshot, error) {
	return client.Snapshot{}, nil
}

func (*fakeSession) Import(context.Context, client.ImportRequest) (client.ImportResult, error) {
	return client.ImportResult{}, nil
}
func (*fakeSession) Health(context.Context) (client.Health, error) { return client.Health{}, nil }
func (session *fakeSession) Close() error {
	session.closeCalls++
	return nil
}
