package daemonprocess

import (
	"slices"
	"sync"
)

type boundedBuffer struct {
	mu      sync.Mutex
	maximum int
	value   []byte
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(value) >= buffer.maximum {
		buffer.value = append(buffer.value[:0], value[len(value)-buffer.maximum:]...)
		return len(value), nil
	}
	overflow := len(buffer.value) + len(value) - buffer.maximum
	if overflow > 0 {
		copy(buffer.value, buffer.value[overflow:])
		buffer.value = buffer.value[:len(buffer.value)-overflow]
	}
	buffer.value = append(buffer.value, value...)
	return len(value), nil
}

func (buffer *boundedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return slices.Clone(buffer.value)
}
