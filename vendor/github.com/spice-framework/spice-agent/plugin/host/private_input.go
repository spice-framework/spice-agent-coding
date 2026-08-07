package pluginhost

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// privateInput owns a short-lived copy of launch bootstrap bytes. Bytes are
// destroyed as they are consumed and the complete backing array is cleared at
// EOF or when startup reaches any terminal transition.
type privateInput struct {
	mu      sync.Mutex
	content []byte
	offset  int
}

func newPrivateInput(content []byte) *privateInput {
	return &privateInput{content: append([]byte(nil), content...)}
}

func (input *privateInput) Read(destination []byte) (int, error) {
	if input == nil {
		return 0, io.EOF
	}
	input.mu.Lock()
	defer input.mu.Unlock()
	if input.offset >= len(input.content) {
		input.clearLocked()
		return 0, io.EOF
	}
	count := copy(destination, input.content[input.offset:])
	clear(input.content[input.offset : input.offset+count])
	input.offset += count
	if input.offset == len(input.content) {
		input.clearLocked()
		return count, nil
	}
	return count, nil
}

func (input *privateInput) Clear() {
	if input == nil {
		return
	}
	input.mu.Lock()
	defer input.mu.Unlock()
	input.clearLocked()
}

func (input *privateInput) cleared() bool {
	if input == nil {
		return true
	}
	input.mu.Lock()
	defer input.mu.Unlock()
	return input.content == nil
}

func (input *privateInput) clearLocked() {
	clear(input.content)
	input.content = nil
	input.offset = 0
}

func (*privateInput) String() string   { return "pluginhost.privateInput([REDACTED])" }
func (*privateInput) GoString() string { return "pluginhost.privateInput([REDACTED])" }
func (*privateInput) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "pluginhost.privateInput([REDACTED])")
}

func (*privateInput) MarshalJSON() ([]byte, error) {
	return json.Marshal("pluginhost.privateInput([REDACTED])")
}
