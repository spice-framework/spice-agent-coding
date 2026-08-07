package client

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// MaximumTextBytes bounds user-visible text crossing the client boundary.
	MaximumTextBytes = 1 << 20
	// MaximumSnapshotPayloadBytes is the maximum safe kernel snapshot payload.
	MaximumSnapshotPayloadBytes = 16 << 20
	// MaximumSnapshotEnvelopeOverheadBytes is the exact deterministic Protobuf
	// overhead for the current maximum-width engine/v1 signed envelope shape.
	MaximumSnapshotEnvelopeOverheadBytes = 295
	// MaximumSnapshotEnvelopeBytes bounds one complete opaque signed snapshot
	// transfer, including its deterministic Protobuf envelope.
	MaximumSnapshotEnvelopeBytes = MaximumSnapshotPayloadBytes + MaximumSnapshotEnvelopeOverheadBytes
	// MaximumSnapshotBytes is retained as the public compatibility spelling for
	// the complete opaque transfer bound.
	MaximumSnapshotBytes = MaximumSnapshotEnvelopeBytes
	// MaximumCapabilities bounds negotiated capability cardinality.
	MaximumCapabilities = 1024
	maximumTokenBytes   = 256
	maximumStatusBytes  = 2048
)

// Build identifies one client or server build without containing credentials.
type Build struct {
	component string
	version   string
	commit    string
	goVersion string
}

// NewBuild constructs validated non-secret build provenance.
func NewBuild(component, version, commit, goVersion string) (Build, error) {
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "build component", value: component},
		{label: "build version", value: version},
		{label: "build commit", value: commit},
		{label: "build Go version", value: goVersion},
	} {
		if err := token(field.label, field.value, maximumTokenBytes); err != nil {
			return Build{}, err
		}
	}
	return Build{component: component, version: version, commit: commit, goVersion: goVersion}, nil
}

func (build Build) Component() string { return build.component }
func (build Build) Version() string   { return build.version }
func (build Build) Commit() string    { return build.commit }
func (build Build) GoVersion() string { return build.goVersion }

// Validate rejects a zero or corrupted Build.
func (build Build) Validate() error {
	_, err := NewBuild(build.component, build.version, build.commit, build.goVersion)
	return err
}

// ProtocolVersion is one negotiated semantic protocol version.
type ProtocolVersion struct {
	major uint32
	minor uint32
	patch uint32
}

// NewProtocolVersion constructs a version with a positive major.
func NewProtocolVersion(major, minor, patch uint32) (ProtocolVersion, error) {
	if major == 0 {
		return ProtocolVersion{}, errors.New("protocol version major must be positive")
	}
	return ProtocolVersion{major: major, minor: minor, patch: patch}, nil
}

func (version ProtocolVersion) Major() uint32 { return version.major }
func (version ProtocolVersion) Minor() uint32 { return version.minor }
func (version ProtocolVersion) Patch() uint32 { return version.patch }

func (version ProtocolVersion) Validate() error {
	_, err := NewProtocolVersion(version.major, version.minor, version.patch)
	return err
}

func compareProtocol(left, right ProtocolVersion) int {
	for _, pair := range [][2]uint32{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

// ProtocolRange is one inclusive single-major compatibility range.
type ProtocolRange struct {
	minimum ProtocolVersion
	maximum ProtocolVersion
}

// NewProtocolRange constructs a non-inverted, single-major range.
func NewProtocolRange(minimum, maximum ProtocolVersion) (ProtocolRange, error) {
	if err := minimum.Validate(); err != nil {
		return ProtocolRange{}, fmt.Errorf("minimum protocol version: %w", err)
	}
	if err := maximum.Validate(); err != nil {
		return ProtocolRange{}, fmt.Errorf("maximum protocol version: %w", err)
	}
	if minimum.major != maximum.major {
		return ProtocolRange{}, errors.New("protocol range must not cross major versions")
	}
	if compareProtocol(minimum, maximum) > 0 {
		return ProtocolRange{}, errors.New("protocol range minimum exceeds maximum")
	}
	return ProtocolRange{minimum: minimum, maximum: maximum}, nil
}

func (current ProtocolRange) Minimum() ProtocolVersion { return current.minimum }
func (current ProtocolRange) Maximum() ProtocolVersion { return current.maximum }

func (current ProtocolRange) Validate() error {
	_, err := NewProtocolRange(current.minimum, current.maximum)
	return err
}

// Limits are the negotiated upper bounds for one client session.
type Limits struct {
	messageBytes      uint64
	collectionItems   uint32
	replayEvents      uint32
	replayBytes       uint64
	concurrentStreams uint32
	activeRuns        uint32
}

// NewLimits constructs internally consistent positive session limits.
func NewLimits(messageBytes uint64, collectionItems, replayEvents uint32, replayBytes uint64, concurrentStreams, activeRuns uint32) (Limits, error) {
	if messageBytes == 0 || collectionItems == 0 || replayEvents == 0 || replayBytes == 0 || concurrentStreams == 0 || activeRuns == 0 {
		return Limits{}, errors.New("client limits must all be positive")
	}
	if uint64(replayEvents) > replayBytes {
		return Limits{}, errors.New("replay event count cannot exceed the replay byte bound")
	}
	return Limits{
		messageBytes: messageBytes, collectionItems: collectionItems,
		replayEvents: replayEvents, replayBytes: replayBytes,
		concurrentStreams: concurrentStreams, activeRuns: activeRuns,
	}, nil
}

func (limits Limits) MessageBytes() uint64      { return limits.messageBytes }
func (limits Limits) CollectionItems() uint32   { return limits.collectionItems }
func (limits Limits) ReplayEvents() uint32      { return limits.replayEvents }
func (limits Limits) ReplayBytes() uint64       { return limits.replayBytes }
func (limits Limits) ConcurrentStreams() uint32 { return limits.concurrentStreams }
func (limits Limits) ActiveRuns() uint32        { return limits.activeRuns }

func (limits Limits) Validate() error {
	_, err := NewLimits(limits.messageBytes, limits.collectionItems, limits.replayEvents, limits.replayBytes, limits.concurrentStreams, limits.activeRuns)
	return err
}

func token(label, value string, maximum int) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%s must not contain NUL, carriage return, line feed, or tab", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return nil
}

func boundedText(label, value string, maximum int, empty bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if !empty && value == "" {
		return fmt.Errorf("%s must be non-empty", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return nil
}

func canonicalTokens(label string, values []string) ([]string, error) {
	return canonicalStrings(label, values, MaximumCapabilities, maximumTokenBytes)
}

func canonicalStrings(label string, values []string, maximumCount, maximumBytes int) ([]string, error) {
	if len(values) > maximumCount {
		return nil, fmt.Errorf("%s count exceeds %d", label, maximumCount)
	}
	result := slices.Clone(values)
	slices.Sort(result)
	for index, value := range result {
		if err := token(label, value, maximumBytes); err != nil {
			return nil, fmt.Errorf("%s %d: %w", label, index, err)
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("%s values must be unique", label)
		}
	}
	return result, nil
}

func containsAll(available, required []string) bool {
	for _, value := range required {
		if _, found := slices.BinarySearch(available, value); !found {
			return false
		}
	}
	return true
}
