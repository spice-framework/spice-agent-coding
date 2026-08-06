package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

const planFingerprintPrefix = "sha256:"

// PlanIdentity combines the generated compiled bean identities with one leased
// tool-plan generation and its immutable definition set.
type PlanIdentity struct {
	compiled              []string
	snapshotCompatibility string
	toolPlanID            stage.PlanID
	fingerprint           string
}

// NewPlanIdentity constructs a combined identity from generated bean identities
// and the exact immutable tool definitions associated with one plan ID.
func NewPlanIdentity(
	compiled []string,
	snapshotCompatibility string,
	toolPlanID stage.PlanID,
	definitions []tool.Definition,
) (PlanIdentity, error) {
	if err := validateCompiledPlan(compiled); err != nil {
		return PlanIdentity{}, err
	}
	if err := toolPlanID.Validate(); err != nil {
		return PlanIdentity{}, err
	}
	if snapshotCompatibility != "" {
		if err := snapshotToken("compatibility identity", snapshotCompatibility, 256); err != nil {
			return PlanIdentity{}, err
		}
	}
	definitionsCopy := make([]tool.Definition, len(definitions))
	for index, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return PlanIdentity{}, fmt.Errorf("tool plan definition %d: %w", index, err)
		}
		definitionsCopy[index] = definition.Clone()
	}
	slices.SortFunc(definitionsCopy, func(left, right tool.Definition) int {
		return strings.Compare(left.Name(), right.Name())
	})
	for index := 1; index < len(definitionsCopy); index++ {
		if definitionsCopy[index-1].Name() == definitionsCopy[index].Name() {
			return PlanIdentity{}, fmt.Errorf("tool plan definition %q is duplicated", definitionsCopy[index].Name())
		}
	}
	hash := sha256.New()
	writeIdentityField(hash, "spice-agent-plan/v2")
	for _, value := range compiled {
		writeIdentityField(hash, value)
	}
	writeIdentityField(hash, toolPlanID.String())
	writeIdentityField(hash, snapshotCompatibility)
	for _, definition := range definitionsCopy {
		writeIdentityField(hash, definition.Name())
		writeIdentityField(hash, definition.Fingerprint())
	}
	result := PlanIdentity{
		compiled:              append([]string(nil), compiled...),
		snapshotCompatibility: snapshotCompatibility,
		toolPlanID:            toolPlanID,
		fingerprint:           fmt.Sprintf("%s%x", planFingerprintPrefix, hash.Sum(nil)),
	}
	return result, result.Validate()
}

// Validate rejects malformed or mutable-looking plan identity data.
func (identity PlanIdentity) Validate() error {
	if err := validateCompiledPlan(identity.compiled); err != nil {
		return err
	}
	if err := identity.toolPlanID.Validate(); err != nil {
		return err
	}
	if identity.snapshotCompatibility != "" {
		if err := snapshotToken("compatibility identity", identity.snapshotCompatibility, 256); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(identity.fingerprint, planFingerprintPrefix) {
		return errors.New("agent plan fingerprint must use sha256")
	}
	digest := strings.TrimPrefix(identity.fingerprint, planFingerprintPrefix)
	if len(digest) != sha256.Size*2 {
		return errors.New("agent plan fingerprint has an invalid SHA-256 length")
	}
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return errors.New("agent plan fingerprint must be lowercase hexadecimal SHA-256")
	}
	return nil
}

// CompiledIdentities returns the generated static bean identities.
func (identity PlanIdentity) CompiledIdentities() []string {
	return append([]string(nil), identity.compiled...)
}

// ToolPlanID returns the exact leased tool generation.
func (identity PlanIdentity) ToolPlanID() stage.PlanID { return identity.toolPlanID }

// SnapshotCompatibilityIdentity returns the compiler-issued portable-import
// identity. Empty means the plan is intentionally not importable by an engine.
func (identity PlanIdentity) SnapshotCompatibilityIdentity() string {
	return identity.snapshotCompatibility
}

// Fingerprint returns the combined stable identity suitable for transport.
func (identity PlanIdentity) Fingerprint() string { return identity.fingerprint }

func (identity PlanIdentity) clone() PlanIdentity {
	identity.compiled = append([]string(nil), identity.compiled...)
	return identity
}

func (identity PlanIdentity) equal(other PlanIdentity) bool {
	return identity.toolPlanID == other.toolPlanID &&
		identity.snapshotCompatibility == other.snapshotCompatibility &&
		identity.fingerprint == other.fingerprint &&
		slices.Equal(identity.compiled, other.compiled)
}

func newPlanIdentity(
	compiled []string,
	snapshotCompatibility string,
	lease *stage.ToolPlanLease,
) (PlanIdentity, error) {
	if err := lease.Validate(); err != nil {
		return PlanIdentity{}, err
	}
	return NewPlanIdentity(compiled, snapshotCompatibility, lease.ToolPlanID(), lease.Definitions())
}

func reconstructPlanIdentity(
	compiled []string,
	snapshotCompatibility string,
	toolPlanID string,
	fingerprint string,
) (PlanIdentity, error) {
	id, err := stage.NewPlanID(toolPlanID)
	if err != nil {
		return PlanIdentity{}, err
	}
	result := PlanIdentity{
		compiled:              append([]string(nil), compiled...),
		snapshotCompatibility: snapshotCompatibility,
		toolPlanID:            id,
		fingerprint:           fingerprint,
	}
	return result, result.Validate()
}

type identityHashWriter interface {
	Write([]byte) (int, error)
}

func writeIdentityField(destination identityHashWriter, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = destination.Write(size[:])
	_, _ = destination.Write([]byte(value))
}

func validateCompiledPlan(values []string) error {
	if len(values) == 0 || len(values) > maximumPlanIdentities {
		return fmt.Errorf("agent compiled plan count must be between 1 and %d", maximumPlanIdentities)
	}
	if !slices.IsSorted(values) {
		return errors.New("agent compiled plan identities must be sorted")
	}
	for index, value := range values {
		if err := snapshotToken("compiled plan identity", value, 256); err != nil {
			return err
		}
		if index > 0 && values[index-1] == value {
			return fmt.Errorf("agent compiled plan identity %q is duplicated", value)
		}
		category, name, found := strings.Cut(value, ":")
		if !found || name == "" {
			return fmt.Errorf("agent compiled plan identity %q must use category:name", value)
		}
		switch category {
		case "provider", "stage", "observer", "broker", "tool", "decorator":
		default:
			return fmt.Errorf("agent compiled plan identity %q has unsupported category", value)
		}
	}
	return nil
}

func validateSnapshotCompatibilityIdentity(value string) error {
	if value == "" {
		return nil
	}
	return snapshotToken("compatibility identity", value, 256)
}
