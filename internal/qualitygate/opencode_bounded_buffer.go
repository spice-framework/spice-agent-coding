package main

import (
	"errors"
	"sync"
)

type opencodeBoundedBuffer struct {
	mu       sync.Mutex
	content  []byte
	maximum  int
	exceeded bool
}

func (buffer *opencodeBoundedBuffer) newOpenCodeBoundedBuffer(maximum int) *opencodeBoundedBuffer {
	return &opencodeBoundedBuffer{maximum: maximum}
}

func (buffer *opencodeBoundedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.maximum <= 0 || len(buffer.content)+len(value) > buffer.maximum {
		buffer.exceeded = true
		return 0, errors.New("bounded output exceeded")
	}
	buffer.content = append(buffer.content, value...)
	return len(value), nil
}

func (buffer *opencodeBoundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(buffer.content)
}

func (buffer *opencodeBoundedBuffer) Exceeded() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.exceeded
}
