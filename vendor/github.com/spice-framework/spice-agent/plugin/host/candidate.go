package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/process"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type launchPhase string

const (
	launchPhaseValidate   launchPhase = "validate"
	launchPhaseVerify     launchPhase = "verify"
	launchPhaseRandom     launchPhase = "random"
	launchPhaseEndpoint   launchPhase = "endpoint"
	launchPhaseBootstrap  launchPhase = "bootstrap"
	launchPhaseStart      launchPhase = "start"
	launchPhaseRecheck    launchPhase = "recheck"
	launchPhaseReadiness  launchPhase = "readiness"
	launchPhaseConnect    launchPhase = "connect"
	launchPhaseInitialize launchPhase = "initialize"
	launchPhaseManifest   launchPhase = "manifest"
	launchPhaseAlive      launchPhase = "alive"
)

// LaunchError preserves a cause for deliberate inspection while keeping
// paths, digests, environment, addresses, secrets, stderr, and plugin-owned
// fields out of incidental formatting and serialization.
type launchError struct {
	phase launchPhase
	cause error
}

func launchFailure(phase launchPhase, cause error) error {
	if cause == nil {
		return nil
	}
	return &launchError{phase: phase, cause: cause}
}

func (failure *launchError) Error() string {
	if failure == nil || !validLaunchPhase(failure.phase) {
		return "runtime plugin candidate launch failed"
	}
	return "runtime plugin candidate launch failed during " + string(failure.phase)
}

func (failure *launchError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure *launchError) phaseName() launchPhase {
	if failure == nil {
		return ""
	}
	return failure.phase
}

func (failure *launchError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, failure.Error())
}

func (failure *launchError) MarshalJSON() ([]byte, error) { return json.Marshal(failure.Error()) }

func validLaunchPhase(phase launchPhase) bool {
	switch phase {
	case launchPhaseValidate, launchPhaseVerify, launchPhaseRandom, launchPhaseEndpoint,
		launchPhaseBootstrap, launchPhaseStart, launchPhaseRecheck, launchPhaseReadiness,
		launchPhaseConnect, launchPhaseInitialize, launchPhaseManifest, launchPhaseAlive:
		return true
	default:
		return false
	}
}

// Candidate owns every resource acquired for one authenticated runtime-plugin
// process. A Candidate returned with an error remains caller-owned and must be
// cleaned up. It is immutable after successful launch except for idempotent,
// retryable cleanup ownership state.
type candidate struct {
	mu sync.Mutex

	executable Executable
	lease      *verifiedExecutable
	endpoint   LocalEndpoint
	process    process.Process
	connection *grpc.ClientConn
	client     pluginv1.PluginServiceClient
	input      *privateInput
	stdout     *readinessSink
	stderr     *stderrSink

	catalog  pluginv1.Catalog
	plugin   *pluginv1.BuildIdentity
	protocol *commonv1.ProtocolVersion
	limits   *pluginv1.Limits
	launchID []byte
	session  []byte
	closed   bool
	result   error
}

// Catalog returns a defensive immutable catalog snapshot.
func (candidate *candidate) catalogSnapshot() pluginv1.Catalog {
	if candidate == nil {
		return pluginv1.Catalog{}
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	manifest, err := candidate.catalog.Manifest()
	if err != nil {
		return pluginv1.Catalog{}
	}
	catalog, err := pluginv1.DecodeManifest(manifest, candidate.limits)
	if err != nil {
		return pluginv1.Catalog{}
	}
	return catalog
}

// PluginBuild returns an independently backed negotiated build identity.
func (candidate *candidate) pluginBuild() *pluginv1.BuildIdentity {
	if candidate == nil {
		return nil
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return cloneProto(candidate.plugin)
}

// Protocol returns an independently backed negotiated protocol version.
func (candidate *candidate) selectedProtocol() *commonv1.ProtocolVersion {
	if candidate == nil {
		return nil
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return cloneProto(candidate.protocol)
}

// Limits returns independently backed negotiated limits.
func (candidate *candidate) negotiatedLimits() *pluginv1.Limits {
	if candidate == nil {
		return nil
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return cloneProto(candidate.limits)
}

// SessionID returns a caller-owned copy of the authenticated session identity.
func (candidate *candidate) sessionID() []byte {
	if candidate == nil {
		return nil
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return slices.Clone(candidate.session)
}

func (candidate *candidate) launchIdentity() []byte {
	if candidate == nil {
		return nil
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return slices.Clone(candidate.launchID)
}

// Cleanup forcefully rejects a candidate and releases local endpoint and file
// ownership only after process containment is proved. A failed Wait retains
// ownership so a later Cleanup call can retry safely.
func (candidate *candidate) cleanup(ctx context.Context) error {
	if candidate == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("runtime plugin candidate cleanup failed")
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if candidate.closed {
		return candidate.result
	}
	if candidate.input != nil {
		candidate.input.Clear()
	}
	clear(candidate.session)
	candidate.session = nil
	if candidate.connection != nil {
		_ = candidate.connection.Close()
		candidate.connection = nil
		candidate.client = nil
	}

	var failures []error
	if candidate.process != nil {
		operation, cancel := boundedContext(ctx, candidate.executable.ContainmentTimeout())
		select {
		case <-candidate.process.Done():
		default:
			if err := candidate.process.ForceKill(operation); err != nil {
				failures = append(failures, err)
			}
		}
		waitErr := candidate.process.Wait(operation)
		cancel()
		if waitErr != nil {
			failures = append(failures, waitErr)
			return cleanupFailure(errors.Join(failures...))
		}
		candidate.process = nil
	}
	clear(candidate.launchID)
	candidate.launchID = nil
	candidate.stderr.clear()
	if candidate.endpoint != nil {
		if err := candidate.endpoint.Close(); err != nil {
			failures = append(failures, err)
		}
		candidate.endpoint = nil
	}
	if candidate.lease != nil {
		if err := candidate.lease.Close(); err != nil {
			failures = append(failures, err)
		}
		candidate.lease = nil
	}
	result := cleanupFailure(errors.Join(failures...))
	candidate.closed = true
	candidate.result = result
	return result
}

func (candidate *candidate) postReadyFailure() error {
	if candidate == nil {
		return errors.New("runtime plugin candidate is unavailable")
	}
	if err := candidate.stdout.err(); err != nil {
		return err
	}
	select {
	case <-candidate.process.Done():
		return errors.New("runtime plugin process exited")
	default:
		return nil
	}
}

func (*candidate) String() string   { return "pluginhost.candidate([REDACTED])" }
func (*candidate) GoString() string { return "pluginhost.candidate([REDACTED])" }
func (*candidate) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "pluginhost.candidate([REDACTED])")
}

func (*candidate) MarshalJSON() ([]byte, error) {
	return json.Marshal("pluginhost.candidate([REDACTED])")
}

type cleanupError struct{ cause error }

func cleanupFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return &cleanupError{cause: cause}
}
func (*cleanupError) Error() string { return "runtime plugin candidate cleanup failed" }
func (failure *cleanupError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure *cleanupError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, failure.Error())
}
func (failure *cleanupError) MarshalJSON() ([]byte, error) { return json.Marshal(failure.Error()) }

func boundedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

func cloneProto[T proto.Message](value T) T {
	var zero T
	if any(value) == nil {
		return zero
	}
	result, ok := proto.Clone(value).(T)
	if !ok {
		return zero
	}
	return result
}
