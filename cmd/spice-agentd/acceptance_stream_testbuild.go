//go:build spice_acceptance && !spice_generate

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent/model"
)

type acceptanceStream struct {
	mu         sync.Mutex
	events     []model.StreamEvent
	next       int
	checkpoint string
	release    string
	closed     bool
}

func (stream *acceptanceStream) Recv(ctx context.Context) (model.StreamEvent, error) {
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return model.StreamEvent{}, io.EOF
	}
	index := stream.next
	if index >= len(stream.events) {
		stream.mu.Unlock()
		return model.StreamEvent{}, io.EOF
	}
	if index == 0 && stream.checkpoint != "" {
		if err := os.WriteFile(stream.checkpoint, []byte("checkpoint\n"), 0o600); err != nil {
			stream.mu.Unlock()
			return model.StreamEvent{}, fmt.Errorf("write acceptance checkpoint: %w", err)
		}
	}
	if index == 1 && stream.release != "" {
		release := stream.release
		stream.mu.Unlock()
		if err := stream.waitForRelease(ctx, release); err != nil {
			return model.StreamEvent{}, err
		}
		stream.mu.Lock()
		if stream.closed || stream.next != index {
			stream.mu.Unlock()
			return model.StreamEvent{}, io.EOF
		}
	}
	event := stream.events[index]
	stream.next++
	stream.mu.Unlock()
	return event, nil
}

func (stream *acceptanceStream) Close() error {
	stream.mu.Lock()
	stream.closed = true
	stream.mu.Unlock()
	return nil
}

func (stream *acceptanceStream) waitForRelease(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
}
