package terminal

import (
	"runtime"
	"testing"

	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	"github.com/spice-framework/spice-agent-coding/internal/tuisession"
	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client/managed"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

func TestTerminalClientValuesAreExactAndValid(t *testing.T) {
	t.Parallel()
	build, err := NewClientBuild()
	if err != nil {
		t.Fatal(err)
	}
	if build.Component() != distribution.TerminalComponent || build.Version() != distribution.Version ||
		build.Commit() != distribution.Commit || build.GoVersion() != runtime.Version() {
		t.Fatal("client build provenance is not exact")
	}
	protocol, err := NewClientProtocol()
	if err != nil {
		t.Fatal(err)
	}
	if protocol.Minimum() != protocol.Maximum() || protocol.Minimum().Major() != 1 ||
		protocol.Minimum().Minor() != 3 || protocol.Minimum().Patch() != 0 {
		t.Fatal("client protocol is not exactly 1.3.0")
	}
	limits, err := NewClientLimits()
	if err != nil {
		t.Fatal(err)
	}
	if limits.MessageBytes() != 4<<20 || limits.CollectionItems() != 512 ||
		limits.ReplayEvents() != 4096 || limits.ReplayBytes() != 8<<20 ||
		limits.ConcurrentStreams() != 8 || limits.ActiveRuns() != 64 {
		t.Fatal("client limits are not exact")
	}
	request, err := NewInitializeRequest(protocol, build, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err = request.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalPresentationValuesAreTyped(t *testing.T) {
	t.Parallel()
	definition, err := NewDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID() != "coding" || definition.Revision() != "v1" {
		t.Fatal("terminal definition is not coding/v1")
	}
	workspace, err := NewWorkspace(Properties{Workspace: t.TempDir()})
	if err != nil || workspace.Title().String() == "" {
		t.Fatalf("workspace = %q, %v", workspace.Title().String(), err)
	}
	status, err := NewInitialStatus()
	if err != nil || status.Level() != agenttui.StatusReconnecting {
		t.Fatalf("status = %q, %v", status.Level(), err)
	}
	protocol, err := NewClientProtocol()
	if err != nil {
		t.Fatal(err)
	}
	build, err := NewClientBuild()
	if err != nil {
		t.Fatal(err)
	}
	limits, err := NewClientLimits()
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewInitializeRequest(protocol, build, limits)
	if err != nil {
		t.Fatal(err)
	}
	config, err := NewSessionConfig(request, definition, workspace, status)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Validate(); err != nil {
		t.Fatal(err)
	}
	if source := NewIdentifierSource(); source == nil {
		t.Fatal("identifier source is unavailable")
	}
}

func TestClientConnectorSelectionFailsClosed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		properties Properties
	}{
		{name: "unsupported", properties: Properties{TerminalMode: "other"}},
		{name: "managed endpoint", properties: Properties{TerminalMode: ModeManaged, TerminalEndpoint: "secret"}},
		{name: "managed dependency", properties: Properties{TerminalMode: ModeManaged}},
		{name: "check dependency", properties: Properties{TerminalMode: ModeCheck}},
		{name: "attach endpoint", properties: Properties{TerminalMode: ModeAttach}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			connector, cleanup, err := NewClientConnector(test.properties, nil, nil)
			if err == nil || connector != nil || cleanup != nil {
				t.Fatalf("selection = %T, %v, %v", connector, cleanup, err)
			}
		})
	}
}

func TestManagedClientConnectorSelectionIsExact(t *testing.T) {
	t.Parallel()
	managedConnector := new(managed.Connector)
	for _, mode := range []string{ModeManaged, ModeCheck} {
		connector, cleanup, err := NewClientConnector(
			Properties{TerminalMode: mode}, managedConnector, nil,
		)
		if err != nil || connector != managedConnector || cleanup != nil {
			t.Fatalf("mode %q selection = %T, %v, %v", mode, connector, cleanup, err)
		}
	}
}

func TestEndpointCompositionRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()
	if store, cleanup, err := NewEndpointStore(endpoint.UserScope{}); err == nil || store != nil || cleanup != nil {
		t.Fatalf("endpoint store = %T, %v, %v", store, cleanup, err)
	}
	if discovery, cleanup, err := NewManagedDiscovery(nil); err == nil || discovery != nil || cleanup != nil {
		t.Fatalf("managed discovery = %T, %v, %v", discovery, cleanup, err)
	}
	if lock, err := NewStartupLock(nil); err == nil || lock != nil {
		t.Fatalf("startup lock = %T, %v", lock, err)
	}
	if connector, cleanup, err := NewManagedConnector(nil, nil, nil); err == nil || connector != nil || cleanup != nil {
		t.Fatalf("managed connector = %T, %v, %v", connector, cleanup, err)
	}
}

func TestSessionConstructionRejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	if session, cleanup, err := NewSession(tuisession.Config{}, nil, nil); err == nil || session != nil || cleanup != nil {
		t.Fatalf("session = %T, %v, %v", session, cleanup, err)
	}
}
