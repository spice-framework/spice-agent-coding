//go:build spice_acceptance && !spice_generate

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/spice-framework/spice-agent/model"
)

type blockingAcceptanceStream struct {
	directory  string
	closed     chan struct{}
	closeOnce  sync.Once
	readyOnce  sync.Once
	cancelOnce sync.Once
}

func newBlockingAcceptanceStream(directory string) *blockingAcceptanceStream {
	return &blockingAcceptanceStream{directory: directory, closed: make(chan struct{})}
}

func (stream *blockingAcceptanceStream) Recv(ctx context.Context) (model.StreamEvent, error) {
	stream.readyOnce.Do(func() {
		_ = os.WriteFile(filepath.Join(stream.directory, "provider.ready"), []byte("ready\n"), 0o600)
	})
	select {
	case <-ctx.Done():
		stream.cancelOnce.Do(func() {
			_ = os.WriteFile(filepath.Join(stream.directory, "provider.cancelled"), []byte("cancelled\n"), 0o600)
		})
		return model.StreamEvent{}, context.Cause(ctx)
	case <-stream.closed:
		return model.StreamEvent{}, io.EOF
	}
}

func (stream *blockingAcceptanceStream) Close() error {
	stream.closeOnce.Do(func() { close(stream.closed) })
	return nil
}
