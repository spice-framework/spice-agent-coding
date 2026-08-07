package enginev1

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"google.golang.org/protobuf/proto"
)

const (
	// CapabilitySnapshotAuthorityV1 identifies authenticated snapshot transfer.
	CapabilitySnapshotAuthorityV1 = "snapshot-authority-v1"
	// SnapshotFormat is the only safe kernel snapshot format accepted by v1.
	SnapshotFormat = "spice.agent.snapshot/v1alpha2"

	snapshotAuthorityDomain = "spice.agent.run-authority/v1"
	snapshotAuthorityBytes  = sha256.Size
)

var (
	// ErrSnapshotAuthoritySigning is returned without exposing signer details.
	ErrSnapshotAuthoritySigning = errors.New("snapshot authority signing failed")
	// ErrSnapshotAuthorityVerification is returned for every keyed authority mismatch.
	ErrSnapshotAuthorityVerification = errors.New("snapshot authority verification failed")
)

// SnapshotAuthorityInput is immutable-by-convention semantic snapshot material.
// It deliberately excludes payload bytes and the authority's own HMAC.
type SnapshotAuthorityInput struct {
	format        string
	runID         string
	lastSequence  uint64
	lifecycle     SnapshotLifecycle
	payloadSHA256 []byte
}

// Format returns the snapshot wire format.
func (input SnapshotAuthorityInput) Format() string { return input.format }

// RunID returns the stable embedded run identity.
func (input SnapshotAuthorityInput) RunID() string { return input.runID }

// LastSequence returns the last committed event sequence.
func (input SnapshotAuthorityInput) LastSequence() uint64 { return input.lastSequence }

// Lifecycle returns the safe snapshot lifecycle boundary.
func (input SnapshotAuthorityInput) Lifecycle() SnapshotLifecycle { return input.lifecycle }

// PayloadSHA256 returns a defensive copy of the validated payload digest.
func (input SnapshotAuthorityInput) PayloadSHA256() []byte { return slices.Clone(input.payloadSHA256) }

// Canonical returns the domain-separated length-prefixed HMAC input for one
// authority scope and generation.
func (input SnapshotAuthorityInput) Canonical(scopeID []byte, generation uint64) ([]byte, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	if len(scopeID) != snapshotAuthorityBytes {
		return nil, fmt.Errorf("snapshot authority scope ID must be exactly %d bytes", snapshotAuthorityBytes)
	}
	if generation == 0 {
		return nil, errors.New("snapshot authority generation must be positive")
	}

	generationBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(generationBytes, generation)
	sequenceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(sequenceBytes, input.lastSequence)
	lifecycleBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lifecycleBytes, uint32(input.lifecycle)) // #nosec G115 -- validated enum values are positive int32 values.

	result := make([]byte, 0, 192+len(input.format)+len(input.runID))
	for _, value := range [][]byte{
		[]byte(snapshotAuthorityDomain),
		scopeID,
		generationBytes,
		[]byte(input.format),
		[]byte(input.runID),
		sequenceBytes,
		lifecycleBytes,
		input.payloadSHA256,
	} {
		result = binary.BigEndian.AppendUint64(result, uint64(len(value)))
		result = append(result, value...)
	}
	return result, nil
}

func (input SnapshotAuthorityInput) validate() error {
	if input.format != SnapshotFormat {
		return errors.New("snapshot authority input format is unsupported")
	}
	if err := token("snapshot authority input run ID", input.runID, 128); err != nil {
		return err
	}
	if input.lastSequence == 0 || input.lastSequence == math.MaxUint64 {
		return errors.New("snapshot authority input sequence must be positive and resumable")
	}
	if !safeSnapshotLifecycle(input.lifecycle) {
		return errors.New("snapshot authority input lifecycle is unsafe")
	}
	if len(input.payloadSHA256) != sha256.Size {
		return errors.New("snapshot authority input payload digest must be SHA-256")
	}
	return nil
}

// SnapshotAuthoritySigner signs validated semantic snapshot material. A signer
// owns its scope, generation, and secret; it must never expose the secret.
type SnapshotAuthoritySigner interface {
	SignSnapshot(context.Context, SnapshotAuthorityInput) (*SnapshotAuthority, error)
}

// SnapshotAuthorityVerifier verifies validated semantic snapshot material
// against a server-owned scope generation.
type SnapshotAuthorityVerifier interface {
	VerifySnapshot(context.Context, SnapshotAuthorityInput, *SnapshotAuthority) error
}

// SnapshotAuthorityCodec combines the trusted signing and verification seams.
type SnapshotAuthorityCodec interface {
	SnapshotAuthoritySigner
	SnapshotAuthorityVerifier
}

// HMACSnapshotAuthority is an in-memory HMAC-SHA256 authority. Its secret
// fields and string forms are deliberately redacted.
type HMACSnapshotAuthority struct {
	scopeID    [snapshotAuthorityBytes]byte
	generation uint64
	key        [snapshotAuthorityBytes]byte
}

// NewHMACSnapshotAuthority constructs an in-memory trusted HMAC-SHA256 codec.
// The key is copied and is never included in a snapshot or error.
func NewHMACSnapshotAuthority(scopeID []byte, generation uint64, key []byte) (*HMACSnapshotAuthority, error) {
	if len(scopeID) != snapshotAuthorityBytes {
		return nil, fmt.Errorf("snapshot authority scope ID must be exactly %d bytes", snapshotAuthorityBytes)
	}
	if generation == 0 {
		return nil, errors.New("snapshot authority generation must be positive")
	}
	if len(key) != snapshotAuthorityBytes {
		return nil, fmt.Errorf("snapshot authority key must be exactly %d bytes", snapshotAuthorityBytes)
	}
	result := &HMACSnapshotAuthority{generation: generation}
	copy(result.scopeID[:], scopeID)
	copy(result.key[:], key)
	return result, nil
}

func (*HMACSnapshotAuthority) String() string   { return "snapshot authority <redacted>" }
func (*HMACSnapshotAuthority) GoString() string { return "snapshot authority <redacted>" }

// SignSnapshot signs one validated semantic snapshot input.
func (authority *HMACSnapshotAuthority) SignSnapshot(
	ctx context.Context,
	input SnapshotAuthorityInput,
) (*SnapshotAuthority, error) {
	if authority == nil {
		return nil, ErrSnapshotAuthoritySigning
	}
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	canonical, err := input.Canonical(authority.scopeID[:], authority.generation)
	if err != nil {
		return nil, ErrSnapshotAuthoritySigning
	}
	mac := hmac.New(sha256.New, authority.key[:])
	_, _ = mac.Write(canonical)
	return &SnapshotAuthority{
		ScopeId:    slices.Clone(authority.scopeID[:]),
		Generation: authority.generation,
		HmacSha256: mac.Sum(nil),
	}, nil
}

// VerifySnapshot verifies one HMAC without exposing which authority component
// failed. It uses a constant-time comparison for both fixed-size byte values.
func (authority *HMACSnapshotAuthority) VerifySnapshot(
	ctx context.Context,
	input SnapshotAuthorityInput,
	claim *SnapshotAuthority,
) error {
	if authority == nil {
		return ErrSnapshotAuthorityVerification
	}
	if err := contextCause(ctx); err != nil {
		return err
	}
	if ValidateSnapshotAuthority(claim) != nil {
		return ErrSnapshotAuthorityVerification
	}
	canonical, err := input.Canonical(claim.GetScopeId(), claim.GetGeneration())
	if err != nil {
		return ErrSnapshotAuthorityVerification
	}
	mac := hmac.New(sha256.New, authority.key[:])
	_, _ = mac.Write(canonical)
	expected := mac.Sum(nil)
	validScope := hmac.Equal(claim.GetScopeId(), authority.scopeID[:])
	validGeneration := claim.GetGeneration() == authority.generation
	validMAC := hmac.Equal(claim.GetHmacSha256(), expected)
	if !validScope || !validGeneration || !validMAC {
		return ErrSnapshotAuthorityVerification
	}
	return nil
}

// ValidateSnapshotAuthority performs structural validation only. It does not
// and cannot verify an HMAC without a trusted verifier.
func ValidateSnapshotAuthority(value *SnapshotAuthority) error {
	if value == nil {
		return errors.New("snapshot authority is required")
	}
	if len(value.GetScopeId()) != snapshotAuthorityBytes {
		return fmt.Errorf("snapshot authority scope ID must be exactly %d bytes", snapshotAuthorityBytes)
	}
	if value.GetGeneration() == 0 {
		return errors.New("snapshot authority generation must be positive")
	}
	if len(value.GetHmacSha256()) != snapshotAuthorityBytes {
		return fmt.Errorf("snapshot authority HMAC-SHA256 must be exactly %d bytes", snapshotAuthorityBytes)
	}
	return nil
}

// ValidateSnapshotEnvelope validates structure, safe lifecycle, payload bound,
// digest, embedded identity, and authority shape. It does not perform keyed
// HMAC verification; imports must use ValidateImportSnapshotRequest.
func ValidateSnapshotEnvelope(value *SnapshotEnvelope) error {
	if err := validateSnapshotEnvelopeContent(value); err != nil {
		return err
	}
	if len(value.ProtoReflect().GetUnknown()) != 0 {
		return errors.New("snapshot envelope contains unsupported fields")
	}
	if proto.Size(value) > MaximumSnapshotEnvelopeBytes {
		return fmt.Errorf("snapshot envelope exceeds %d encoded bytes", MaximumSnapshotEnvelopeBytes)
	}
	return ValidateSnapshotAuthority(value.GetAuthority())
}

func validateSnapshotEnvelopeContent(value *SnapshotEnvelope) error {
	if value == nil {
		return errors.New("snapshot envelope is required")
	}
	if value.GetFormat() != SnapshotFormat {
		return fmt.Errorf("snapshot format %q is unsupported", value.GetFormat())
	}
	if err := token("snapshot run ID", value.GetRunId(), 128); err != nil {
		return err
	}
	if value.GetLastSequence() == 0 || value.GetLastSequence() == math.MaxUint64 {
		return errors.New("snapshot sequence must be positive and resumable")
	}
	if !safeSnapshotLifecycle(value.GetLifecycle()) {
		return fmt.Errorf("snapshot lifecycle %d is unsafe", value.GetLifecycle())
	}
	if len(value.GetPayload()) == 0 || len(value.GetPayload()) > MaximumSnapshotBytes {
		return fmt.Errorf("snapshot payload must be between 1 and %d bytes", MaximumSnapshotBytes)
	}
	digest := sha256.Sum256(value.GetPayload())
	if !hmac.Equal(value.GetSha256(), digest[:]) {
		return errors.New("snapshot SHA-256 digest does not match its payload")
	}
	var identity struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(value.GetPayload(), &identity); err != nil {
		return errors.New("snapshot payload must be valid v1alpha2 JSON")
	}
	if identity.RunID != value.GetRunId() {
		return errors.New("snapshot envelope run ID does not match its embedded run ID")
	}
	return nil
}

func safeSnapshotLifecycle(value SnapshotLifecycle) bool {
	switch value {
	case SnapshotLifecycle_SNAPSHOT_LIFECYCLE_SUSPENDED,
		SnapshotLifecycle_SNAPSHOT_LIFECYCLE_COMPLETED,
		SnapshotLifecycle_SNAPSHOT_LIFECYCLE_FAILED,
		SnapshotLifecycle_SNAPSHOT_LIFECYCLE_CANCELLED:
		return true
	default:
		return false
	}
}

func snapshotAuthorityInput(value *SnapshotEnvelope) SnapshotAuthorityInput {
	return SnapshotAuthorityInput{
		format:        value.GetFormat(),
		runID:         value.GetRunId(),
		lastSequence:  value.GetLastSequence(),
		lifecycle:     value.GetLifecycle(),
		payloadSHA256: slices.Clone(value.GetSha256()),
	}
}

// NewSnapshotEnvelope constructs a fully signed immutable-by-convention wire
// value. It cannot produce an unsigned snapshot.
func NewSnapshotEnvelope(
	ctx context.Context,
	signer SnapshotAuthoritySigner,
	runID string,
	lastSequence uint64,
	lifecycle SnapshotLifecycle,
	payload []byte,
) (*SnapshotEnvelope, error) {
	if signer == nil {
		return nil, ErrSnapshotAuthoritySigning
	}
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	result := &SnapshotEnvelope{
		Format:       SnapshotFormat,
		RunId:        runID,
		LastSequence: lastSequence,
		Lifecycle:    lifecycle,
		Payload:      slices.Clone(payload),
		Sha256:       slices.Clone(digest[:]),
	}
	if err := validateSnapshotEnvelopeContent(result); err != nil {
		return nil, err
	}
	authority, err := callSnapshotSigner(ctx, signer, snapshotAuthorityInput(result))
	if err != nil {
		return nil, err
	}
	result.Authority = clone(authority)
	if err = ValidateSnapshotEnvelope(result); err != nil {
		return nil, ErrSnapshotAuthoritySigning
	}
	return result, nil
}

// ValidateImportSnapshotRequest rejects unsafe, unsigned, unowned, or
// unauthenticated snapshot imports. Keyed verification cannot be omitted.
func ValidateImportSnapshotRequest(
	ctx context.Context,
	request *ImportSnapshotRequest,
	verifier SnapshotAuthorityVerifier,
	limits *commonv1.Limits,
) error {
	if request == nil {
		return errors.New("import snapshot request is required")
	}
	if verifier == nil {
		return ErrSnapshotAuthorityVerification
	}
	if err := contextCause(ctx); err != nil {
		return err
	}
	if err := ValidateImportSnapshotRequestStructure(request, limits); err != nil {
		return err
	}
	return callSnapshotVerifier(ctx, verifier, snapshotAuthorityInput(request.GetSnapshot()), request.GetSnapshot().GetAuthority())
}

// ValidateImportSnapshotRequestStructure validates the complete unkeyed import
// contract. It proves client and operation identity, the negotiated encoded
// size, snapshot structure and digest, authority shape, and the suspended safe
// boundary. It deliberately does not authenticate the authority HMAC; callers
// admitting an import must additionally perform keyed authority verification.
func ValidateImportSnapshotRequestStructure(
	request *ImportSnapshotRequest,
	limits *commonv1.Limits,
) error {
	if request == nil {
		return errors.New("import snapshot request is required")
	}
	if err := validateClientMutation(
		request.GetClientId(),
		request.GetOwnershipEpoch(),
		request.GetClientOperationId(),
	); err != nil {
		return err
	}
	if err := validateUnarySize(request, limits); err != nil {
		return err
	}
	if err := ValidateSnapshotEnvelope(request.GetSnapshot()); err != nil {
		return err
	}
	if request.GetSnapshot().GetLifecycle() != SnapshotLifecycle_SNAPSHOT_LIFECYCLE_SUSPENDED {
		return errors.New("only a suspended snapshot may be imported")
	}
	return nil
}

func callSnapshotSigner(
	ctx context.Context,
	signer SnapshotAuthoritySigner,
	input SnapshotAuthorityInput,
) (authority *SnapshotAuthority, resultErr error) {
	defer func() {
		if recover() != nil {
			authority = nil
			resultErr = ErrSnapshotAuthoritySigning
		}
	}()
	authority, err := signer.SignSnapshot(ctx, input)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return nil, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return nil, context.DeadlineExceeded
		}
		return nil, ErrSnapshotAuthoritySigning
	}
	return authority, nil
}

func callSnapshotVerifier(
	ctx context.Context,
	verifier SnapshotAuthorityVerifier,
	input SnapshotAuthorityInput,
	authority *SnapshotAuthority,
) (resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = ErrSnapshotAuthorityVerification
		}
	}()
	if err := verifier.VerifySnapshot(ctx, input, clone(authority)); err != nil {
		if cause := contextCause(ctx); cause != nil {
			return cause
		}
		return ErrSnapshotAuthorityVerification
	}
	if cause := contextCause(ctx); cause != nil {
		return cause
	}
	return nil
}

func contextCause(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return context.Cause(ctx)
}

func protocolSupportsSnapshotAuthority(version *commonv1.ProtocolVersion) bool {
	return version.GetMajor() == commonv1.ProtocolMajor && version.GetMinor() >= 1
}

func snapshotCapabilitiesForProtocol(
	version *commonv1.ProtocolVersion,
	capabilities *commonv1.CapabilitySet,
) *commonv1.CapabilitySet {
	if capabilities == nil || protocolSupportsSnapshotAuthority(version) {
		return capabilities
	}
	names := make([]string, 0, len(capabilities.GetNames()))
	for _, name := range capabilities.GetNames() {
		if name != CapabilitySnapshotAuthorityV1 && name != "snapshots" {
			names = append(names, name)
		}
	}
	return &commonv1.CapabilitySet{Names: names}
}

func containsCapability(capabilities *commonv1.CapabilitySet, name string) bool {
	if capabilities == nil {
		return false
	}
	_, found := slices.BinarySearch(capabilities.GetNames(), name)
	return found
}
