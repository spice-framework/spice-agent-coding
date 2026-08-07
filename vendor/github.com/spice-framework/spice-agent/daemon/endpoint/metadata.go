package endpoint

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/spice-framework/spice-agent/client"
)

const (
	metadataSchema      = "spice.agent.local-endpoint/v1"
	maximumMetadataSize = 8 << 10
	processIDAttempts   = 4
	// localipc permits at most 128 bytes after \\.\pipe\. Reserve the
	// canonical Spice prefix so every published address is consumable.
	maximumWindowsPipeSuffixLength = 128 - len("spice-agent-")

	// ProcessInstanceIDBytes is the exact entropy size of a daemon process
	// lifetime identity.
	ProcessInstanceIDBytes = 16
)

// Transport identifies one supported user-local IPC address family.
type Transport string

const (
	TransportUnixSocket       Transport = "unix-socket"
	TransportWindowsNamedPipe Transport = "windows-named-pipe"
)

// Process identifies the exact daemon process lifetime that published an
// endpoint. InstanceID prevents PID reuse from making stale metadata current.
type Process struct {
	id         uint32
	startedAt  time.Time
	instanceID [ProcessInstanceIDBytes]byte
}

// GenerateProcess creates a process lifetime identity using the operating
// system CSPRNG. The caller supplies the positive PID and exact UTC start time
// because only the daemon process owns those platform facts.
func GenerateProcess(id uint32, startedAt time.Time) (Process, error) {
	return generateProcess(rand.Reader, id, startedAt)
}

func generateProcess(random io.Reader, id uint32, startedAt time.Time) (Process, error) {
	if random == nil {
		return Process{}, errors.New("endpoint process randomness is nil")
	}
	if err := validateProcessFacts(id, startedAt); err != nil {
		return Process{}, err
	}
	for range processIDAttempts {
		var instanceID [ProcessInstanceIDBytes]byte
		if _, err := io.ReadFull(random, instanceID[:]); err != nil {
			// Entropy-source details are deliberately omitted so a faulty custom
			// reader cannot place sensitive data in logs through its error.
			return Process{}, errors.New("generate endpoint process instance")
		}
		if !allZeroBytes(instanceID[:]) {
			return NewProcess(id, startedAt, instanceID[:])
		}
	}
	return Process{}, errors.New("generate nonzero endpoint process instance")
}

// NewProcess validates one process lifetime identity.
func NewProcess(id uint32, startedAt time.Time, instanceID []byte) (Process, error) {
	if err := validateProcessFacts(id, startedAt); err != nil {
		return Process{}, err
	}
	if len(instanceID) != ProcessInstanceIDBytes || allZeroBytes(instanceID) {
		return Process{}, errors.New("endpoint process instance ID is invalid")
	}
	value := Process{id: id, startedAt: startedAt}
	copy(value.instanceID[:], instanceID)
	return value, nil
}

func validateProcessFacts(id uint32, startedAt time.Time) error {
	if id == 0 {
		return errors.New("endpoint process ID must be positive")
	}
	nanoseconds := startedAt.UnixNano()
	if startedAt.IsZero() || startedAt.Location() != time.UTC || nanoseconds <= 0 ||
		!time.Unix(0, nanoseconds).UTC().Equal(startedAt) {
		return errors.New("endpoint process start time must be positive UTC")
	}
	return nil
}

func (process Process) ID() uint32           { return process.id }
func (process Process) StartedAt() time.Time { return process.startedAt }
func (process Process) InstanceID() []byte   { return slices.Clone(process.instanceID[:]) }

func (process Process) Validate() error {
	_, err := NewProcess(process.id, process.startedAt, process.instanceID[:])
	return err
}

// Metadata is one immutable authenticated local endpoint description. Default
// formatting and JSON serialization are redacted because it owns a Token.
type Metadata struct {
	transport Transport
	address   string
	token     Token
	server    client.Build
	protocol  client.ProtocolVersion
	process   Process
}

// NewMetadata validates one closed local-transport endpoint record. Protocol is
// the highest protocol version this daemon publication advertises; normal
// negotiation may select a compatible lower minor version.
func NewMetadata(
	transport Transport,
	address string,
	token Token,
	server client.Build,
	protocol client.ProtocolVersion,
	process Process,
) (Metadata, error) {
	if err := validateAddress(transport, address); err != nil {
		return Metadata{}, err
	}
	if err := token.Validate(); err != nil {
		return Metadata{}, err
	}
	if err := server.Validate(); err != nil {
		return Metadata{}, fmt.Errorf("endpoint server build: %w", err)
	}
	if err := protocol.Validate(); err != nil {
		return Metadata{}, fmt.Errorf("endpoint protocol: %w", err)
	}
	if err := process.Validate(); err != nil {
		return Metadata{}, err
	}
	return Metadata{
		transport: transport, address: address, token: token,
		server: server, protocol: protocol, process: process,
	}, nil
}

func (metadata Metadata) Transport() Transport { return metadata.transport }
func (metadata Metadata) Address() string      { return metadata.address }
func (metadata Metadata) Token() Token         { return metadata.token }
func (metadata Metadata) Server() client.Build { return metadata.server }

// Protocol returns the highest version advertised by this daemon publication.
func (metadata Metadata) Protocol() client.ProtocolVersion { return metadata.protocol }
func (metadata Metadata) Process() Process                 { return metadata.process }
func (Metadata) String() string                            { return "[REDACTED local endpoint metadata]" }

func (Metadata) GoString() string { return "endpoint.Metadata([REDACTED])" }

func (Metadata) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED local endpoint metadata]"`), nil
}

func (Metadata) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED local endpoint metadata]")
}

func (metadata Metadata) Validate() error {
	_, err := NewMetadata(metadata.transport, metadata.address, metadata.token, metadata.server, metadata.protocol, metadata.process)
	return err
}

type metadataWire struct {
	Schema          string `json:"schema"`
	Transport       string `json:"transport"`
	Address         string `json:"address"`
	Token           string `json:"token"`
	Component       string `json:"server_component"`
	Version         string `json:"server_version"`
	Commit          string `json:"server_commit"`
	GoVersion       string `json:"server_go_version"`
	ProtocolMajor   uint32 `json:"protocol_major"`
	ProtocolMinor   uint32 `json:"protocol_minor"`
	ProtocolPatch   uint32 `json:"protocol_patch"`
	ProcessID       uint32 `json:"process_id"`
	StartedUnixNano int64  `json:"started_unix_nano"`
	InstanceID      string `json:"instance_id"`
}

func encodeMetadata(metadata Metadata) ([]byte, error) {
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	authorization, err := metadata.token.AuthorizationValue()
	if err != nil {
		return nil, err
	}
	wire := metadataWire{
		Schema: metadataSchema, Transport: string(metadata.transport), Address: metadata.address,
		Token:     strings.TrimPrefix(authorization, BearerPrefix),
		Component: metadata.server.Component(), Version: metadata.server.Version(),
		Commit: metadata.server.Commit(), GoVersion: metadata.server.GoVersion(),
		ProtocolMajor: metadata.protocol.Major(), ProtocolMinor: metadata.protocol.Minor(),
		ProtocolPatch: metadata.protocol.Patch(), ProcessID: metadata.process.id,
		StartedUnixNano: metadata.process.startedAt.UnixNano(),
		InstanceID:      base64.RawURLEncoding.EncodeToString(metadata.process.instanceID[:]),
	}
	encoded, err := json.Marshal(wire)
	if err != nil || len(encoded)+1 > maximumMetadataSize {
		return nil, errors.New("encode local endpoint metadata")
	}
	return append(encoded, '\n'), nil
}

func decodeMetadata(encoded []byte) (Metadata, error) {
	if len(encoded) == 0 || len(encoded) > maximumMetadataSize {
		return Metadata{}, errors.New("local endpoint metadata size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire metadataWire
	if err := decoder.Decode(&wire); err != nil {
		return Metadata{}, errors.New("decode local endpoint metadata")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || wire.Schema != metadataSchema {
		return Metadata{}, errors.New("local endpoint metadata schema is invalid")
	}
	instanceID, err := base64.RawURLEncoding.DecodeString(wire.InstanceID)
	if err != nil || base64.RawURLEncoding.EncodeToString(instanceID) != wire.InstanceID {
		return Metadata{}, errors.New("local endpoint process instance is invalid")
	}
	process, err := NewProcess(wire.ProcessID, time.Unix(0, wire.StartedUnixNano).UTC(), instanceID)
	if err != nil {
		return Metadata{}, err
	}
	token, err := ParseToken(wire.Token)
	if err != nil {
		return Metadata{}, err
	}
	server, err := client.NewBuild(wire.Component, wire.Version, wire.Commit, wire.GoVersion)
	if err != nil {
		return Metadata{}, err
	}
	protocol, err := client.NewProtocolVersion(wire.ProtocolMajor, wire.ProtocolMinor, wire.ProtocolPatch)
	if err != nil {
		return Metadata{}, err
	}
	metadata, err := NewMetadata(Transport(wire.Transport), wire.Address, token, server, protocol, process)
	if err != nil {
		return Metadata{}, err
	}
	canonical, err := encodeMetadata(metadata)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return Metadata{}, errors.New("local endpoint metadata is not canonical")
	}
	return metadata, nil
}

func validateAddress(transport Transport, address string) error {
	if address == "" || len(address) > 1024 || address != strings.TrimSpace(address) ||
		strings.IndexFunc(address, unicode.IsControl) >= 0 {
		return errors.New("local endpoint address is invalid")
	}
	switch transport {
	case TransportUnixSocket:
		if !strings.HasPrefix(address, "/") || path.Clean(address) != address || len(address) > 100 ||
			!safeLocalName(path.Base(address)) {
			return errors.New("unix endpoint address must be a canonical absolute path")
		}
	case TransportWindowsNamedPipe:
		const prefix = `\\.\pipe\spice-agent-`
		name := strings.TrimPrefix(address, prefix)
		if name == address || len(name) > maximumWindowsPipeSuffixLength || !safeLocalName(name) {
			return errors.New("windows endpoint address must be a canonical Spice named pipe")
		}
	default:
		return fmt.Errorf("local endpoint transport %q is unsupported", transport)
	}
	return nil
}

func safeLocalName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return false
	}
	for _, current := range name {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' || strings.ContainsRune("._-", current) {
			continue
		}
		return false
	}
	return true
}

func allZeroBytes(value []byte) bool {
	var combined byte
	for _, current := range value {
		combined |= current
	}
	return combined == 0
}
