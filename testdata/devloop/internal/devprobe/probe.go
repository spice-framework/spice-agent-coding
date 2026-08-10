package devprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"
// @import { OnStart, OnStop } from "github.com/spice-framework/spice/annotation/lifecycle"

func sourceRevision() string {
	return "one"
}

type probeResponse struct {
	PID      int    `json:"pid"`
	Revision string `json:"revision"`
	Instance string `json:"instance"`
}

// Probe exposes process identity over a loopback-only listener. It deliberately
// has no production dependency so the fixture proves the generated lifecycle.
type Probe struct {
	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	instance string
}

// NewProbe constructs the only application bean.
//
// @Bean
func NewProbe() (*Probe, error) {
	identity := make([]byte, 16)
	if _, err := rand.Read(identity); err != nil {
		return nil, err
	}
	return &Probe{instance: hex.EncodeToString(identity)}, nil
}

// Start binds a race-free ephemeral loopback address and publishes that address
// through the test-owned metadata file before reporting ready.
//
// @OnStart
func (probe *Probe) Start(_ context.Context) error {
	addressFile := os.Getenv("DEV_PROBE_ADDRESS_FILE")
	if !filepath.IsAbs(addressFile) || filepath.Base(addressFile) != "probe-address" {
		return errors.New("DEV_PROBE_ADDRESS_FILE must be an absolute test-owned probe-address path")
	}
	addressFile = filepath.Clean(addressFile)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	if err = publishAddress(addressFile, listener.Addr().String(), probe.instance); err != nil {
		return errors.Join(err, listener.Close())
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(probeResponse{
			PID: os.Getpid(), Revision: sourceRevision(), Instance: probe.instance,
		})
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	probe.mu.Lock()
	probe.listener = listener
	probe.server = server
	probe.mu.Unlock()
	go func() {
		_ = server.Serve(listener)
	}()
	return nil
}

func publishAddress(path, address, instance string) (resultErr error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "devprobe-address-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		// #nosec G703 -- CreateTemp returned this path inside the validated test-owned directory.
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, removeErr)
		}
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return errors.Join(err, temporary.Close())
	}
	metadata := struct {
		PID      int    `json:"pid"`
		Address  string `json:"address"`
		Instance string `json:"instance"`
	}{PID: os.Getpid(), Address: address, Instance: instance}
	if err = json.NewEncoder(temporary).Encode(metadata); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err = temporary.Sync(); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	// Both paths are confined to the validated test-owned directory. The
	// platform helper preserves replacement semantics when the previous probe
	// metadata is concurrently observed on Windows.
	return replacePublishedAddress(temporaryPath, path)
}

// Stop drains the probe before the generated application process exits.
//
// @OnStop
func (probe *Probe) Stop(ctx context.Context) error {
	probe.mu.Lock()
	server := probe.server
	probe.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}
