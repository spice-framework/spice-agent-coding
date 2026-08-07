// Program spice-agent-distribution-fixture is an independent runtime-plugin
// executable used by the distribution's offline process-boundary acceptance.
package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spice-framework/spice-agent/daemon/localipc"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"google.golang.org/grpc"
)

const shutdownGrace = 25 * time.Millisecond

func main() {
	if err := run(); err != nil {
		// The process boundary deliberately keeps bootstrap addresses, handshake
		// material, RPC payloads, and implementation errors out of stderr.
		_, _ = fmt.Fprintln(os.Stderr, "spice-agent-distribution-fixture: process failed")
		os.Exit(1)
	}
}

func run() error {
	address, secret, err := pluginv1.DecodeBootstrap(os.Stdin)
	if err != nil {
		return errors.New("decode private bootstrap")
	}
	defer clear(secret)

	listener, err := localipc.Listen(address)
	if err != nil {
		return errors.New("listen on private endpoint")
	}
	defer func() { _ = listener.Close() }()

	shutdown := make(chan struct{})
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(pluginv1.InitializeBootstrapMaximumBytes),
		grpc.MaxSendMsgSize(pluginv1.InitializeBootstrapMaximumBytes),
	)
	service, err := newPluginService(secret, func() {
		time.AfterFunc(shutdownGrace, func() { close(shutdown) })
	})
	if err != nil {
		return err
	}
	defer service.clearSecrets()
	pluginv1.RegisterPluginServiceServer(server, service)

	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case <-serveDone:
		return errors.New("serve private endpoint")
	default:
	}
	if err = pluginv1.WriteReadiness(os.Stdout); err != nil {
		server.Stop()
		<-serveDone
		return err
	}

	select {
	case <-shutdown:
		server.GracefulStop()
		err = <-serveDone
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return errors.New("stop private endpoint")
		}
		return nil
	case <-serveDone:
		return errors.New("serve private endpoint")
	}
}
