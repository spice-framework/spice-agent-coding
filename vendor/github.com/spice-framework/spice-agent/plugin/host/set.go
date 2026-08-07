package pluginhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	// MaximumExecutables bounds one atomic activation attempt and the number of
	// independently owned plugin processes in a future generation.
	MaximumExecutables = 128
)

// Set is an immutable complete desired runtime-plugin configuration. An empty
// set is valid and means that only compiled tools participate in a future
// generation. Executables are stored in canonical ID order.
type Set struct {
	executables []Executable
}

// NewSet validates, copies, and canonically orders a complete plugin set.
// Stable IDs and manifest names are unique within one set; activation never
// interprets this value as an incremental mutation.
func NewSet(executables []Executable) (Set, error) {
	if len(executables) > MaximumExecutables {
		return Set{}, errors.New("plugin executable set exceeds its process limit")
	}
	result := make([]Executable, len(executables))
	for index, executable := range executables {
		if err := executable.Validate(); err != nil {
			return Set{}, fmt.Errorf("plugin executable set item %d: %w", index, err)
		}
		result[index] = executable.Clone()
	}
	slices.SortFunc(result, func(left, right Executable) int {
		return strings.Compare(left.ID(), right.ID())
	})
	for index := 1; index < len(result); index++ {
		if result[index-1].ID() == result[index].ID() {
			return Set{}, errors.New("plugin executable set contains a duplicate id")
		}
	}
	manifests := make(map[string]struct{}, len(result))
	for _, executable := range result {
		if _, duplicate := manifests[executable.ManifestName()]; duplicate {
			return Set{}, errors.New("plugin executable set contains a duplicate manifest name")
		}
		manifests[executable.ManifestName()] = struct{}{}
	}
	return Set{executables: result}, nil
}

// Validate rejects corrupted package-internal state without performing I/O.
func (set Set) Validate() error {
	_, err := NewSet(set.executables)
	return err
}

// Executables returns independently backed configurations in canonical ID
// order.
func (set Set) Executables() []Executable {
	result := make([]Executable, len(set.executables))
	for index, executable := range set.executables {
		result[index] = executable.Clone()
	}
	return result
}

// Len returns the number of configured plugin processes.
func (set Set) Len() int { return len(set.executables) }

func (set Set) String() string {
	return fmt.Sprintf("pluginhost.Set(count=%d)", len(set.executables))
}

func (set Set) GoString() string { return set.String() }

func (set Set) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, set.String())
}

func (set Set) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Count int `json:"count"`
	}{Count: len(set.executables)})
}
