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

type BlockingAcceptanceStream struct {
	directory  string
	closed     chan struct{}
	closeOnce  sync.Once
	readyOnce  sync.Once
	cancelOnce sync.Once
}

func NewBlockingAcceptanceStream(directory string) *BlockingAcceptanceStream {
	return &BlockingAcceptanceStream{directory: directory, closed: make(chan struct{})}
}

func (stream *BlockingAcceptanceStream) Recv(ctx context.Context) (model.StreamEvent, error) {
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

func (stream *BlockingAcceptanceStream) Close() error {
	stream.closeOnce.Do(func() { close(stream.closed) })
	return nil
}
