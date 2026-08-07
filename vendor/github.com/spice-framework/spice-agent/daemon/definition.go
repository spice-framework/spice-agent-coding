package daemon

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/spice-framework/spice-agent/agent"
)

const maximumDefinitions = 4096

// Definition is one immutable generated daemon definition. The embedded agent
// definition is the exact value used to start a run; the daemon does not
// reconstruct model or turn policy from client input.
type Definition struct {
	id       string
	revision string
	value    agent.Definition
}

// NewDefinition validates one generated definition identity and its exact
// kernel definition.
func NewDefinition(id, revision string, value agent.Definition) (Definition, error) {
	if err := boundedToken("definition ID", id); err != nil {
		return Definition{}, err
	}
	if err := boundedToken("definition revision", revision); err != nil {
		return Definition{}, err
	}
	if _, err := agent.NewDefinition(value.Name(), value.Model(), value.MaxTurns()); err != nil {
		return Definition{}, fmt.Errorf("agent definition: %w", err)
	}
	return Definition{id: id, revision: revision, value: value}, nil
}

// ID returns the stable server-owned definition identity.
func (definition Definition) ID() string { return definition.id }

// Revision returns the exact generated definition revision.
func (definition Definition) Revision() string { return definition.revision }

// Agent returns the exact immutable kernel definition.
func (definition Definition) Agent() agent.Definition { return definition.value }

// DefinitionSet is an immutable, canonically sorted generated catalog.
type DefinitionSet struct {
	revision string
	values   []Definition
}

// NewDefinitionSet validates, sorts, and fingerprints a generated catalog.
func NewDefinitionSet(values []Definition) (DefinitionSet, error) {
	if len(values) == 0 || len(values) > maximumDefinitions {
		return DefinitionSet{}, fmt.Errorf("definition set count must be between 1 and %d", maximumDefinitions)
	}
	result := slices.Clone(values)
	sort.Slice(result, func(first, second int) bool {
		if result[first].id == result[second].id {
			return result[first].revision < result[second].revision
		}
		return result[first].id < result[second].id
	})
	hasher := sha256.New()
	writeUint32(hasher, uint32(len(result))) // #nosec G115 -- count is bounded above.
	for index, value := range result {
		validated, err := NewDefinition(value.id, value.revision, value.value)
		if err != nil {
			return DefinitionSet{}, fmt.Errorf("definition %d: %w", index, err)
		}
		result[index] = validated
		if index > 0 && result[index-1].id == value.id && result[index-1].revision == value.revision {
			return DefinitionSet{}, fmt.Errorf("definition %q revision %q is duplicated", value.id, value.revision)
		}
		writeString(hasher, value.id)
		writeString(hasher, value.revision)
		writeString(hasher, value.value.Name())
		writeString(hasher, value.value.Model())
		writeUint32(hasher, value.value.MaxTurns())
	}
	return DefinitionSet{revision: hex.EncodeToString(hasher.Sum(nil)), values: result}, nil
}

// Revision returns the deterministic catalog fingerprint.
func (set DefinitionSet) Revision() string { return set.revision }

// Definitions returns a defensive copy in canonical order.
func (set DefinitionSet) Definitions() []Definition { return slices.Clone(set.values) }

// Resolve returns an exact server-owned definition identity.
func (set DefinitionSet) Resolve(id, revision string) (Definition, error) {
	if err := boundedToken("definition ID", id); err != nil {
		return Definition{}, err
	}
	if err := boundedToken("definition revision", revision); err != nil {
		return Definition{}, err
	}
	index, found := slices.BinarySearchFunc(set.values, Definition{id: id, revision: revision}, func(first, second Definition) int {
		if comparison := strings.Compare(first.id, second.id); comparison != 0 {
			return comparison
		}
		return strings.Compare(first.revision, second.revision)
	})
	if !found {
		return Definition{}, fmt.Errorf("definition %q revision %q is unavailable", id, revision)
	}
	return set.values[index], nil
}

func writeString(target hash.Hash, value string) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(len(value)))
	_, _ = target.Write(encoded[:])
	_, _ = target.Write([]byte(value))
}

func writeUint32(target hash.Hash, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = target.Write(encoded[:])
}

func boundedToken(label, value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return fmt.Errorf("%s must be non-empty, trimmed, and at most 128 bytes", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s must not contain control characters", label)
		}
	}
	return nil
}
