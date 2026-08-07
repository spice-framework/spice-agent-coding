package pluginhost

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/process"
	"github.com/spice-framework/spice-agent/tool"
	"google.golang.org/protobuf/proto"
)

const (
	maximumIdentityBytes = 128
	maximumManifestBytes = 256

	// MaximumOperationTimeout bounds every process and protocol phase configured
	// for one executable. Callers must use context cancellation for shorter
	// operation-specific deadlines.
	MaximumOperationTimeout = 24 * time.Hour
)

// ExecutableConfig is mutable constructor input. NewExecutable validates and
// defensively copies every field. Environment is exact; an empty slice means
// the child receives no inherited variables. Arguments are deliberately not a
// contract: pinning an interpreter while an unpinned script remains mutable
// would provide false executable identity.
type ExecutableConfig struct {
	ID                   string
	ManifestName         string
	ManifestVersion      string
	Path                 string
	SHA256               SHA256
	WorkingDirectory     string
	Environment          []string
	ApprovedCapabilities []tool.Capability
	RequestedLimits      *pluginv1.Limits
	StartupTimeout       time.Duration
	CallTimeout          time.Duration
	DrainTimeout         time.Duration
	ShutdownTimeout      time.Duration
	ContainmentTimeout   time.Duration
}

// Executable is immutable validated process intent. Validation performs no
// filesystem access; openVerifiedExecutable establishes the pinned file lease
// immediately before launch.
type Executable struct {
	id                   string
	manifestName         string
	manifestVersion      string
	path                 string
	digest               SHA256
	workingDirectory     string
	environment          []string
	approvedCapabilities []tool.Capability
	requestedLimits      *pluginv1.Limits
	startupTimeout       time.Duration
	callTimeout          time.Duration
	drainTimeout         time.Duration
	shutdownTimeout      time.Duration
	containmentTimeout   time.Duration
}

// NewExecutable validates and copies one production plugin executable.
func NewExecutable(config ExecutableConfig) (Executable, error) {
	if err := validateIdentity("id", config.ID, maximumIdentityBytes, true); err != nil {
		return Executable{}, err
	}
	if err := validateIdentity("manifest_name", config.ManifestName, maximumManifestBytes, false); err != nil {
		return Executable{}, err
	}
	if err := validateIdentity("manifest_version", config.ManifestVersion, maximumManifestBytes, false); err != nil {
		return Executable{}, err
	}
	if config.SHA256.isZero() {
		return Executable{}, configFailure("sha256", -1, ProblemRequired)
	}
	if config.RequestedLimits == nil {
		return Executable{}, configFailure("requested_limits", -1, ProblemRequired)
	}
	if err := pluginv1.ValidateLimits(config.RequestedLimits); err != nil {
		return Executable{}, configFailure("requested_limits", -1, ProblemMalformed)
	}
	for _, timeout := range []struct {
		field string
		value time.Duration
	}{
		{"startup_timeout", config.StartupTimeout},
		{"call_timeout", config.CallTimeout},
		{"drain_timeout", config.DrainTimeout},
		{"shutdown_timeout", config.ShutdownTimeout},
		{"containment_timeout", config.ContainmentTimeout},
	} {
		if timeout.value <= 0 || timeout.value > MaximumOperationTimeout {
			return Executable{}, configFailure(timeout.field, -1, ProblemOutOfRange)
		}
	}
	for index, capability := range config.ApprovedCapabilities {
		if !validApprovedCapability(capability) {
			return Executable{}, configFailure("capabilities", index, ProblemMalformed)
		}
	}

	capabilities := appendProcessCapability(config.ApprovedCapabilities)
	spec, err := process.NewSpec(process.Config{
		Executable:       config.Path,
		WorkingDirectory: config.WorkingDirectory,
		Environment:      config.Environment,
		Stdin:            strings.NewReader(""),
		Stdout:           io.Discard,
		Stderr:           io.Discard,
		Capabilities:     capabilities,
	})
	if err != nil {
		return Executable{}, mapProcessConfigError(err)
	}

	approved := spec.Capabilities()
	if !slices.Contains(config.ApprovedCapabilities, tool.CapabilityProcessExecute) {
		approved = slices.DeleteFunc(approved, func(value tool.Capability) bool {
			return value == tool.CapabilityProcessExecute
		})
	}

	return Executable{
		id:                   config.ID,
		manifestName:         config.ManifestName,
		manifestVersion:      config.ManifestVersion,
		path:                 spec.Executable(),
		digest:               config.SHA256,
		workingDirectory:     spec.WorkingDirectory(),
		environment:          spec.Environment(),
		approvedCapabilities: approved,
		requestedLimits:      cloneLimits(config.RequestedLimits),
		startupTimeout:       config.StartupTimeout,
		callTimeout:          config.CallTimeout,
		drainTimeout:         config.DrainTimeout,
		shutdownTimeout:      config.ShutdownTimeout,
		containmentTimeout:   config.ContainmentTimeout,
	}, nil
}

// Validate rejects a zero or corrupted value without filesystem access.
func (executable Executable) Validate() error {
	_, err := NewExecutable(ExecutableConfig{
		ID: executable.id, ManifestName: executable.manifestName,
		ManifestVersion: executable.manifestVersion, Path: executable.path,
		SHA256: executable.digest, WorkingDirectory: executable.workingDirectory,
		Environment: executable.environment, ApprovedCapabilities: executable.approvedCapabilities,
		RequestedLimits: executable.requestedLimits, StartupTimeout: executable.startupTimeout,
		CallTimeout: executable.callTimeout, DrainTimeout: executable.drainTimeout,
		ShutdownTimeout: executable.shutdownTimeout, ContainmentTimeout: executable.containmentTimeout,
	})
	return err
}

func (executable Executable) ID() string               { return executable.id }
func (executable Executable) ManifestName() string     { return executable.manifestName }
func (executable Executable) ManifestVersion() string  { return executable.manifestVersion }
func (executable Executable) Path() string             { return executable.path }
func (executable Executable) SHA256() SHA256           { return executable.digest }
func (executable Executable) WorkingDirectory() string { return executable.workingDirectory }
func (executable Executable) Environment() []string    { return slices.Clone(executable.environment) }

func (executable Executable) ApprovedCapabilities() []tool.Capability {
	return slices.Clone(executable.approvedCapabilities)
}

func (executable Executable) RequestedLimits() *pluginv1.Limits {
	cloned := cloneLimits(executable.requestedLimits)
	if cloned == nil {
		return &pluginv1.Limits{}
	}
	return cloned
}
func (executable Executable) StartupTimeout() time.Duration  { return executable.startupTimeout }
func (executable Executable) CallTimeout() time.Duration     { return executable.callTimeout }
func (executable Executable) DrainTimeout() time.Duration    { return executable.drainTimeout }
func (executable Executable) ShutdownTimeout() time.Duration { return executable.shutdownTimeout }

func (executable Executable) ContainmentTimeout() time.Duration { return executable.containmentTimeout }

// Clone returns an independently backed immutable value.
func (executable Executable) Clone() Executable {
	executable.environment = slices.Clone(executable.environment)
	executable.approvedCapabilities = slices.Clone(executable.approvedCapabilities)
	executable.requestedLimits = cloneLimits(executable.requestedLimits)
	return executable
}

func (Executable) String() string   { return "pluginhost.Executable([REDACTED])" }
func (Executable) GoString() string { return "pluginhost.Executable([REDACTED])" }
func (Executable) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "pluginhost.Executable([REDACTED])")
}

func (Executable) MarshalJSON() ([]byte, error) {
	return json.Marshal("pluginhost.Executable([REDACTED])")
}

func validateIdentity(field, value string, maximum int, canonical bool) error {
	if value == "" {
		return configFailure(field, -1, ProblemRequired)
	}
	if !utf8.ValidString(value) {
		return configFailure(field, -1, ProblemInvalidUTF8)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return configFailure(field, -1, ProblemContainsNUL)
	}
	if len(value) > maximum {
		return configFailure(field, -1, ProblemTooLarge)
	}
	if value != strings.TrimSpace(value) {
		return configFailure(field, -1, ProblemMalformed)
	}
	for _, character := range value {
		if unicode.IsControl(character) || (canonical && !canonicalIdentityRune(character)) {
			return configFailure(field, -1, ProblemMalformed)
		}
	}
	if canonical && (value[0] < 'a' || value[0] > 'z') {
		return configFailure(field, -1, ProblemMalformed)
	}
	return nil
}

func canonicalIdentityRune(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9' ||
		character == '.' || character == '_' || character == '-'
}

func appendProcessCapability(values []tool.Capability) []tool.Capability {
	result := slices.Clone(values)
	if !slices.Contains(result, tool.CapabilityProcessExecute) {
		result = append(result, tool.CapabilityProcessExecute)
	}
	return result
}

func validApprovedCapability(value tool.Capability) bool {
	switch value {
	case tool.CapabilityFilesystemRead,
		tool.CapabilityFilesystemWrite,
		tool.CapabilityProcessExecute,
		tool.CapabilityNetworkAccess,
		tool.CapabilitySecretsRead,
		tool.CapabilityEnvironmentRead,
		tool.CapabilityEnvironmentWrite:
		return true
	default:
		return false
	}
}

func mapProcessConfigError(err error) error {
	if failure, ok := err.(*process.SpecError); ok { //nolint:errorlint // Preserve exact typed validation field.
		problem := ConfigProblem(failure.Problem())
		switch problem {
		case ProblemRequired, ProblemMalformed, ProblemNotAbsolute, ProblemNotCanonical,
			ProblemInvalidUTF8, ProblemContainsNUL, ProblemDuplicate, ProblemTooMany, ProblemTooLarge:
		default:
			problem = ProblemMalformed
		}
		return configFailure(failure.Field(), failure.Index(), problem)
	}
	return configFailure("process", -1, ProblemMalformed)
}

func cloneLimits(value *pluginv1.Limits) *pluginv1.Limits {
	if value == nil {
		return nil
	}
	cloned, ok := proto.Clone(value).(*pluginv1.Limits)
	if !ok {
		return nil
	}
	return cloned
}
