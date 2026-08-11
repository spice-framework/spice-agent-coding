package pluginhost

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net"
	"slices"
	"sync"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/process"
	"github.com/spice-framework/spice-agent/tool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var errProcessExited = errors.New("runtime plugin process exited during startup")

// candidateLauncher establishes one authenticated, digest-pinned runtime-tool
// candidate. It never retries or activates a candidate.
type candidateLauncher struct {
	processes process.VerifiedLauncher
	endpoints LocalEndpointFactory
	random    io.Reader
	randomMu  sync.Mutex
	host      *pluginv1.BuildIdentity
}

// newCandidateLauncher validates and snapshots candidate-launch dependencies.
func newCandidateLauncher(
	processes process.VerifiedLauncher,
	endpoints LocalEndpointFactory,
	randomSource io.Reader,
	host *pluginv1.BuildIdentity,
) (*candidateLauncher, error) {
	if processes == nil || endpoints == nil {
		return nil, launchFailure(launchPhaseValidate, errors.New("launch dependency is required"))
	}
	if randomSource == nil {
		return nil, launchFailure(launchPhaseValidate, errors.New("random source is required"))
	}
	if host == nil {
		return nil, launchFailure(launchPhaseValidate, errors.New("host build identity is required"))
	}
	hostCopy := cloneProto(host)
	probe := initializeRequest(hostCopy, make([]byte, pluginv1.LaunchIDBytes),
		make([]byte, pluginv1.HandshakeChallengeBytes), probeLimits())
	if err := pluginv1.ValidateInitializeRequest(probe); err != nil {
		return nil, launchFailure(launchPhaseValidate, err)
	}
	return &candidateLauncher{
		processes: processes,
		endpoints: endpoints,
		random:    randomSource,
		host:      hostCopy,
	}, nil
}

// Launch starts and authenticates one candidate. Once any file, endpoint, or
// process ownership has been acquired, candidate is non-nil even on error and
// the caller must invoke Cleanup.
func (launcher *candidateLauncher) launch(ctx context.Context, executable Executable) (*candidate, error) {
	if ctx == nil || launcher == nil {
		return nil, launchFailure(launchPhaseValidate, errors.New("candidate launcher input is invalid"))
	}
	if err := executable.Validate(); err != nil {
		return nil, launchFailure(launchPhaseValidate, err)
	}
	startup, cancelStartup := boundedContext(ctx, executable.StartupTimeout())
	defer cancelStartup()

	candidate, material, spec, err := launcher.prepare(startup, executable)
	if material != nil {
		defer material.clear()
	}
	if err != nil {
		return candidate, err
	}
	if err = launcher.start(startup, candidate, spec); err != nil {
		return candidate, err
	}
	if err = launcher.authenticate(startup, candidate, executable, material); err != nil {
		return candidate, err
	}
	return candidate, nil
}

type launchMaterial struct {
	launchID  []byte
	challenge []byte
	secret    []byte
}

func (material *launchMaterial) clear() {
	if material == nil {
		return
	}
	clear(material.launchID)
	clear(material.challenge)
	clear(material.secret)
}

func (launcher *candidateLauncher) prepare(
	ctx context.Context,
	executable Executable,
) (*candidate, *launchMaterial, process.Spec, error) {
	lease, err := openVerifiedExecutable(ctx, executable)
	if err != nil {
		return nil, nil, process.Spec{}, launchFailure(launchPhaseVerify, err)
	}
	candidate := &candidate{executable: executable.Clone(), lease: lease}
	material := &launchMaterial{}

	material.launchID, err = launcher.randomBytes(pluginv1.LaunchIDBytes)
	if err != nil {
		return candidate, material, process.Spec{}, launchFailure(launchPhaseRandom, err)
	}
	material.challenge, err = launcher.randomBytes(pluginv1.HandshakeChallengeBytes)
	if err != nil {
		return candidate, material, process.Spec{}, launchFailure(launchPhaseRandom, err)
	}
	material.secret, err = launcher.randomBytes(pluginv1.HandshakeSecretBytes)
	if err != nil {
		return candidate, material, process.Spec{}, launchFailure(launchPhaseRandom, err)
	}

	endpoint, err := launcher.endpoints.Open(ctx, hex.EncodeToString(material.launchID))
	candidate.endpoint = endpoint
	if err != nil {
		return candidate, material, process.Spec{}, launchFailure(launchPhaseEndpoint, err)
	}
	if endpoint == nil {
		return candidate, material, process.Spec{}, launchFailure(launchPhaseEndpoint, errors.New("endpoint factory returned no ownership"))
	}

	bootstrap, err := pluginv1.EncodeBootstrap(endpoint.Address(), material.secret)
	if err != nil {
		return candidate, material, process.Spec{}, launchFailure(launchPhaseBootstrap, err)
	}
	candidate.input = newPrivateInput(bootstrap)
	clear(bootstrap)
	candidate.stdout = newReadinessSink()
	candidate.stderr = newStderrSink()

	spec, err := process.NewSpec(process.Config{
		Executable: executable.Path(), WorkingDirectory: executable.WorkingDirectory(),
		Environment: executable.Environment(), Stdin: candidate.input,
		Stdout: candidate.stdout, Stderr: candidate.stderr,
		Capabilities: appendProcessCapability(executable.ApprovedCapabilities()),
	})
	if err != nil {
		candidate.input.Clear()
		return candidate, material, process.Spec{}, launchFailure(launchPhaseBootstrap, err)
	}
	return candidate, material, spec, nil
}

func (launcher *candidateLauncher) start(
	ctx context.Context,
	candidate *candidate,
	spec process.Spec,
) error {
	ownedProcess, startErr := launcher.processes.StartVerified(ctx, candidate.lease, spec)
	candidate.process = ownedProcess
	if ownedProcess == nil && startErr == nil {
		startErr = errors.New("process launcher returned no ownership")
	}
	if ownedProcess != nil {
		if recheckErr := recheckVerifiedExecutable(ctx, candidate.lease); recheckErr != nil {
			candidate.input.Clear()
			return launchFailure(launchPhaseRecheck, recheckErr)
		}
	}
	if startErr != nil {
		candidate.input.Clear()
		return launchFailure(launchPhaseStart, startErr)
	}
	if ownedProcess == nil {
		candidate.input.Clear()
		return launchFailure(launchPhaseStart, errors.New("process launcher returned no ownership"))
	}

	if err := waitForReadiness(ctx, candidate.stdout, ownedProcess); err != nil {
		candidate.input.Clear()
		return launchFailure(launchPhaseReadiness, err)
	}
	candidate.input.Clear()
	return nil
}

func (launcher *candidateLauncher) authenticate(
	ctx context.Context,
	candidate *candidate,
	executable Executable,
	material *launchMaterial,
) error {
	endpoint := candidate.endpoint
	connection, err := dialCandidate(endpoint, executable.RequestedLimits())
	if err != nil {
		return launchFailure(launchPhaseConnect, err)
	}
	candidate.connection = connection
	candidate.client = pluginv1.NewPluginServiceClient(connection)

	request := initializeRequest(launcher.host, material.launchID, material.challenge, executable.RequestedLimits())
	response, err := initializeCandidate(ctx, candidate, request)
	if err != nil {
		return launchFailure(launchPhaseInitialize, err)
	}
	if err = pluginv1.ValidateInitializeResponseForRequest(request, response, material.secret); err != nil {
		return launchFailure(launchPhaseInitialize, err)
	}
	catalog, err := pluginv1.DecodeManifest(response.GetManifest(), response.GetLimits())
	if err != nil || catalog.Name() != executable.ManifestName() || catalog.Version() != executable.ManifestVersion() {
		if err == nil {
			err = errors.New("plugin manifest identity does not match executable configuration")
		}
		return launchFailure(launchPhaseManifest, err)
	}
	if err = approveCatalog(catalog, executable.ApprovedCapabilities()); err != nil {
		return launchFailure(launchPhaseManifest, err)
	}
	if err = candidate.postReadyFailure(); err != nil {
		return launchFailure(launchPhaseAlive, err)
	}

	candidate.catalog = catalog
	candidate.plugin = cloneProto(response.GetPlugin())
	candidate.protocol = cloneProto(response.GetProtocol())
	candidate.limits = cloneProto(response.GetLimits())
	candidate.launchID = slices.Clone(material.launchID)
	candidate.session = slices.Clone(response.GetSessionId())
	return nil
}

func initializeRequest(
	host *pluginv1.BuildIdentity,
	launchID,
	challenge []byte,
	limits *pluginv1.Limits,
) *pluginv1.InitializeRequest {
	capabilities := &commonv1.CapabilitySet{Names: []string{pluginv1.CapabilityRuntimeToolsV1}}
	return &pluginv1.InitializeRequest{
		Protocol: pluginv1.SupportedProtocolRange(), Host: cloneProto(host),
		SupportedCapabilities: cloneProto(capabilities), RequiredCapabilities: cloneProto(capabilities),
		RequestedLimits: cloneProto(limits), LaunchId: slices.Clone(launchID),
		HandshakeChallenge: slices.Clone(challenge),
	}
}

func probeLimits() *pluginv1.Limits {
	return &pluginv1.Limits{
		MaxMessageBytes: 1, MaxTools: 1, MaxSchemaBytes: 1,
		MaxCallArgumentBytes: 1, MaxResultBytes: 1, MaxProgressBytes: 1,
		MaxConcurrentCalls: 1,
	}
}

func (launcher *candidateLauncher) randomBytes(size int) ([]byte, error) {
	launcher.randomMu.Lock()
	defer launcher.randomMu.Unlock()
	value := make([]byte, size)
	if _, err := io.ReadFull(launcher.random, value); err != nil {
		clear(value)
		return nil, errors.New("generate runtime plugin launch material")
	}
	allZero := true
	for _, current := range value {
		allZero = allZero && current == 0
	}
	if allZero {
		clear(value)
		return nil, errors.New("runtime plugin launch material is zero")
	}
	return value, nil
}

func waitForReadiness(ctx context.Context, stdout *readinessSink, owned process.Process) error {
	select {
	case <-stdout.transition:
		if err := stdout.err(); err != nil {
			return err
		}
	case <-owned.Done():
		return errProcessExited
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := stdout.err(); err != nil {
		return err
	}
	select {
	case <-owned.Done():
		return errProcessExited
	default:
		return nil
	}
}

func dialCandidate(endpoint LocalEndpoint, limits *pluginv1.Limits) (*grpc.ClientConn, error) {
	if endpoint == nil {
		return nil, errors.New("runtime plugin endpoint is unavailable")
	}
	if err := pluginv1.ValidateLimits(limits); err != nil {
		return nil, errors.New("runtime plugin requested limits are invalid")
	}
	maximumMessageBytes := limits.GetMaxMessageBytes()
	if maximumMessageBytes > uint64(math.MaxInt) {
		return nil, errors.New("runtime plugin message limit exceeds platform integer capacity")
	}
	maximum := int(maximumMessageBytes) // #nosec G115 -- the platform maximum is checked immediately above.
	connection, err := grpc.NewClient(
		"passthrough:///spice-runtime-plugin",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableRetry(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return endpoint.Dial(ctx)
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maximum),
			grpc.MaxCallSendMsgSize(maximum),
		),
	)
	if err != nil {
		return nil, err
	}
	return connection, nil
}

func initializeCandidate(
	ctx context.Context,
	candidate *candidate,
	request *pluginv1.InitializeRequest,
) (*pluginv1.InitializeResponse, error) {
	candidate.mu.Lock()
	ownedProcess := candidate.process
	stdout := candidate.stdout
	candidate.mu.Unlock()
	if ownedProcess == nil || stdout == nil {
		return nil, errors.New("runtime plugin candidate initialization ownership is unavailable")
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		select {
		case <-ownedProcess.Done():
			cancel()
		case <-stdout.failureSignal():
			cancel()
		case <-operation.Done():
		case <-finished:
		}
	}()
	response, err := candidate.client.Initialize(operation, request)
	close(finished)
	if stdoutErr := stdout.err(); stdoutErr != nil {
		return nil, stdoutErr
	}
	select {
	case <-ownedProcess.Done():
		return nil, errProcessExited
	default:
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return response, err
}

func approveCatalog(catalog pluginv1.Catalog, approved []tool.Capability) error {
	for _, definition := range catalog.Definitions() {
		for _, capability := range definition.Capabilities() {
			if !slices.Contains(approved, capability) {
				return errors.New("plugin manifest requests an unapproved tool capability")
			}
		}
	}
	return nil
}
