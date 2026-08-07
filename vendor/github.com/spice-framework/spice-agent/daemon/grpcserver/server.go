package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/daemon"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"github.com/spice-framework/spice-agent/event"
	"google.golang.org/grpc"
)

// ServerConfig contains the generated daemon dependencies and immutable public
// build facts for one local engine server. The server opens no listener and
// does not own Host or Sessions.
type ServerConfig struct {
	Root            context.Context //nolint:containedctx // transferred as the server service lifetime.
	EndpointToken   EndpointToken
	Host            *daemon.RunHost
	Sessions        *daemon.SessionStore
	Build           client.Build
	Capabilities    []string
	MaximumSessions int
}

type runHostBoundary interface {
	Describe(context.Context) (daemon.RunHostDescription, error)
	Health(context.Context, daemon.Session) (client.Health, error)
	Start(context.Context, daemon.Session, client.StartRequest) (client.StartResult, error)
	Cancel(context.Context, daemon.Session, client.CancelRequest) (client.CancelResult, error)
	Respond(context.Context, daemon.Session, client.RespondRequest) (client.RespondResult, error)
	Suspend(context.Context, daemon.Session, client.RunMutation) (client.SuspendResult, error)
	Resume(context.Context, daemon.Session, client.RunMutation) (client.ResumeResult, error)
	Export(context.Context, daemon.Session, client.RunRef) (client.Snapshot, error)
	Import(context.Context, daemon.Session, client.ImportRequest) (client.ImportResult, error)
	ReplayEvents(context.Context, daemon.Session, client.RunRef, event.ReplayRequest) (ownedEventObservation, error)
	SnapshotInteractions(context.Context, daemon.Session) (interactionObservation, error)
	SubscribeInteractions(context.Context, daemon.Session) (interactionObservation, error)
}

type runHostAdapter struct{ *daemon.RunHost }

func (adapter runHostAdapter) ReplayEvents(
	ctx context.Context,
	session daemon.Session,
	run client.RunRef,
	request event.ReplayRequest,
) (ownedEventObservation, error) {
	observation, err := adapter.RunHost.ReplayEvents(ctx, session, run, request)
	return normalizeEventObservation(observation, err)
}

func (adapter runHostAdapter) SnapshotInteractions(
	ctx context.Context,
	session daemon.Session,
) (interactionObservation, error) {
	observation, err := adapter.RunHost.SnapshotInteractions(ctx, session)
	return normalizeInteractionObservation(observation, err)
}

func (adapter runHostAdapter) SubscribeInteractions(
	ctx context.Context,
	session daemon.Session,
) (interactionObservation, error) {
	observation, err := adapter.RunHost.SubscribeInteractions(ctx, session)
	return normalizeInteractionObservation(observation, err)
}

func normalizeEventObservation(
	observation *daemon.EventObservation,
	err error,
) (ownedEventObservation, error) {
	if observation == nil {
		return nil, err
	}
	return observation, err
}

func normalizeInteractionObservation(
	observation *daemon.InteractionObservation,
	err error,
) (interactionObservation, error) {
	if observation == nil {
		return nil, err
	}
	return observation, err
}

type sessionStoreBoundary interface {
	Fresh() (daemon.Session, error)
	ReconnectContext(context.Context, string, uint64) (daemon.Session, error)
	Check(string, uint64) error
}

type serverDependencies struct {
	root            context.Context //nolint:containedctx // transferred as the adapter service lifetime.
	token           EndpointToken
	host            runHostBoundary
	sessions        sessionStoreBoundary
	build           client.Build
	capabilities    []string
	maximumSessions int
}

// Server is one authenticated local gRPC boundary. It wraps gRPC so callers
// cannot register the engine service without both authentication interceptors.
type Server struct {
	grpc         *grpc.Server
	registry     *negotiatedSessionRegistry
	cancel       context.CancelFunc
	shutdownOnce sync.Once
	forceOnce    sync.Once
	shutdownDone chan struct{}
}

// NewServer constructs the authenticated boundary without opening an OS
// listener. Generated applications remain responsible for endpoint ownership.
func NewServer(config ServerConfig) (*Server, error) {
	if config.Host == nil || config.Sessions == nil {
		return nil, errors.New("gRPC server requires run host and session store")
	}
	return newServer(serverDependencies{
		root: config.Root, token: config.EndpointToken, host: runHostAdapter{RunHost: config.Host},
		sessions: config.Sessions, build: config.Build,
		capabilities: config.Capabilities, maximumSessions: config.MaximumSessions,
	})
}

func newServer(dependencies serverDependencies) (*Server, error) {
	if dependencies.root == nil || dependencies.host == nil || dependencies.sessions == nil {
		return nil, errors.New("gRPC server requires root, run host, and session store")
	}
	if err := dependencies.root.Err(); err != nil {
		return nil, errors.New("gRPC server root is already canceled")
	}
	build, err := buildToWire(dependencies.build)
	if err != nil {
		return nil, err
	}
	capabilities, err := capabilitiesToWire(dependencies.capabilities)
	if err != nil {
		return nil, err
	}
	description, err := dependencies.host.Describe(dependencies.root)
	if err != nil {
		return nil, fmt.Errorf("describe run host: %w", err)
	}
	if err = description.Validate(); err != nil {
		return nil, fmt.Errorf("validate run host description: %w", err)
	}
	limits, err := limitsToWire(description.Health().Limits())
	if err != nil {
		return nil, err
	}
	if _, err = healthToWire(description.Health()); err != nil {
		return nil, err
	}
	if _, err = definitionsToWire(description.Definitions(), limits); err != nil {
		return nil, err
	}
	maximumMessageBytes := max(limits.GetMaxMessageBytes(), uint64(enginev1.InitializeBootstrapMaximumBytes))
	if maximumMessageBytes > uint64(math.MaxInt) {
		return nil, errors.New("gRPC server message limit exceeds platform integer capacity")
	}
	unary, stream, err := newAuthenticationInterceptors(dependencies.token)
	if err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(dependencies.root)
	registry, err := newNegotiatedSessionRegistry(root, dependencies.maximumSessions)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("construct negotiated session registry: %w", err)
	}
	service := &engineService{
		root: root, host: dependencies.host, sessions: dependencies.sessions,
		registry: registry, build: build, capabilities: capabilities, limits: limits,
	}
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(int(maximumMessageBytes)),
		grpc.MaxSendMsgSize(int(maximumMessageBytes)),
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
	enginev1.RegisterEngineServiceServer(server, service)
	return &Server{
		grpc: server, registry: registry, cancel: cancel,
		shutdownDone: make(chan struct{}),
	}, nil
}

// Serve accepts authenticated connections from the caller-owned listener.
func (server *Server) Serve(listener net.Listener) error {
	if server == nil || server.grpc == nil || listener == nil {
		return errors.New("gRPC server and listener are required")
	}
	return server.grpc.Serve(listener)
}

// Stop immediately stops gRPC work and closes negotiated-session lookup. It
// does not close the transport-independent RunHost or SessionStore.
func (server *Server) Stop() {
	if server == nil || server.grpc == nil {
		return
	}
	server.beginGracefulShutdown()
	server.forceOnce.Do(server.grpc.Stop)
	<-server.shutdownDone
}

// GracefulStop stops admission and waits without a deadline. New code should
// use Shutdown with an explicit deadline so a flow-control-blocked client
// cannot make process shutdown unbounded.
func (server *Server) GracefulStop() {
	if server == nil || server.grpc == nil {
		return
	}
	_ = server.Shutdown(context.Background())
}

// Shutdown stops admission, cancels adapter-owned stream observations, and
// waits for accepted RPCs. If ctx ends first, it force-stops gRPC so a client
// blocked by transport flow control cannot strand process shutdown.
func (server *Server) Shutdown(ctx context.Context) error {
	if server == nil || server.grpc == nil {
		return errors.New("gRPC server is required")
	}
	if ctx == nil {
		return errors.New("gRPC shutdown context is required")
	}
	server.beginGracefulShutdown()
	select {
	case <-server.shutdownDone:
		return nil
	default:
	}
	select {
	case <-server.shutdownDone:
		return nil
	case <-ctx.Done():
		select {
		case <-server.shutdownDone:
			return nil
		default:
		}
		server.forceOnce.Do(server.grpc.Stop)
		<-server.shutdownDone
		return ctx.Err()
	}
}

func (server *Server) beginGracefulShutdown() {
	server.shutdownOnce.Do(func() {
		if server.cancel != nil {
			server.cancel()
		}
		go func() {
			server.grpc.GracefulStop()
			if server.registry != nil {
				server.registry.close()
			}
			close(server.shutdownDone)
		}()
	})
}
