package pluginhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/process"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	maximumRetainedGenerations = 128
	maximumRetainedCandidates  = MaximumExecutables * 2
	hostEpochBytes             = 32
)

// HostConfig contains the Spice-constructed static dependencies of a runtime
// plugin host. Runtime plugin processes can only add tools to this immutable
// compiled dispatcher; they cannot mutate the Spice bean graph.
type HostConfig struct {
	HostIdentity *pluginv1.BuildIdentity
	Compiled     stage.ToolDispatcher
	Decorators   []stage.ToolDispatchDecorator
	Processes    process.Launcher
	Endpoints    LocalEndpointFactory
}

// Host owns immutable runtime-tool generations and implements
// stage.ToolPlanSource. Activation is serialized and atomic: a complete Set is
// authenticated and composed before the current-generation pointer changes.
type Host struct {
	mu sync.Mutex

	starter     generationStarter
	compiled    stage.ToolDispatcher
	decorators  []stage.ToolDispatchDecorator
	rootDone    <-chan struct{}
	cancel      context.CancelFunc
	activation  chan struct{}
	closeGate   chan struct{}
	changed     chan struct{}
	current     *hostGeneration
	available   map[stage.PlanID]*hostGeneration
	owned       map[*hostGeneration]struct{}
	sequence    uint64
	epoch       [hostEpochBytes]byte
	activations int
	closing     bool
	closeTry    uint64
}

type generationCandidate interface {
	tools() map[string]tool.Tool
	done() <-chan struct{}
	healthFailure() error
	close(context.Context) error
	launchIdentity() []byte
}

type generationStarter interface {
	start(context.Context, Executable) (generationCandidate, error)
}

type productionStarter struct{ launcher *candidateLauncher }

func (starter productionStarter) start(ctx context.Context, executable Executable) (generationCandidate, error) {
	value, err := starter.launcher.launch(ctx, executable)
	if err != nil {
		if value == nil {
			return nil, err
		}
		return &rejectedGenerationCandidate{candidate: value}, err
	}
	accepted, acceptErr := newAcceptedCandidate(value)
	if acceptErr != nil {
		return &rejectedGenerationCandidate{candidate: value}, acceptErr
	}
	return &acceptedGenerationCandidate{accepted: accepted, identity: value.launchIdentity()}, nil
}

type acceptedGenerationCandidate struct {
	accepted *acceptedCandidate
	identity []byte
}

func (candidate *acceptedGenerationCandidate) tools() map[string]tool.Tool {
	return candidate.accepted.tools()
}

func (candidate *acceptedGenerationCandidate) done() <-chan struct{} {
	return candidate.accepted.done()
}

func (candidate *acceptedGenerationCandidate) healthFailure() error {
	return candidate.accepted.healthFailure()
}

func (candidate *acceptedGenerationCandidate) close(ctx context.Context) error {
	return candidate.accepted.close(ctx)
}

func (candidate *acceptedGenerationCandidate) launchIdentity() []byte {
	return slices.Clone(candidate.identity)
}

type rejectedGenerationCandidate struct{ candidate *candidate }

func (*rejectedGenerationCandidate) tools() map[string]tool.Tool { return nil }
func (*rejectedGenerationCandidate) done() <-chan struct{} {
	result := make(chan struct{})
	close(result)
	return result
}

func (*rejectedGenerationCandidate) healthFailure() error {
	return errors.New("runtime plugin candidate was rejected")
}

func (candidate *rejectedGenerationCandidate) close(ctx context.Context) error {
	return candidate.candidate.cleanup(ctx)
}
func (candidate *rejectedGenerationCandidate) launchIdentity() []byte { return nil }

type hostGeneration struct {
	id         stage.PlanID
	dispatcher stage.ToolDispatcher
	candidates []generationCandidate
	refs       int
	current    bool
	unhealthy  error
	cleaning   bool
	cleaned    bool
	cleanupErr error
	cleanupTry uint64
}

// NewHost constructs a production runtime-plugin host with cryptographic
// launch material. The initial current generation is the compiled dispatcher.
func NewHost(config HostConfig) (*Host, error) {
	return newHost(config, rand.Reader, nil)
}

func newHost(config HostConfig, random io.Reader, starter generationStarter) (*Host, error) {
	if config.Compiled == nil {
		return nil, errors.New("runtime plugin host requires a compiled dispatcher")
	}
	decorators := slices.Clone(config.Decorators)
	for _, decorator := range decorators {
		if decorator == nil {
			return nil, errors.New("runtime plugin host contains a nil decorator")
		}
	}
	if random == nil {
		random = rand.Reader
	}
	epoch, err := readHostEpoch(random)
	if err != nil {
		return nil, err
	}
	if starter == nil {
		launcher, launchErr := newCandidateLauncher(config.Processes, config.Endpoints, random, config.HostIdentity)
		if launchErr != nil {
			return nil, launchErr
		}
		starter = productionStarter{launcher: launcher}
	}
	base, err := stage.ApplyToolDispatchDecorators(config.Compiled, nil)
	if err != nil {
		return nil, fmt.Errorf("snapshot compiled runtime plugin generation: %w", err)
	}
	compiled, err := stage.ApplyToolDispatchDecorators(base, decorators)
	if err != nil {
		return nil, fmt.Errorf("compose compiled runtime plugin generation: %w", err)
	}
	id, err := generationPlanID(epoch[:], 0, nil, compiled.Definitions())
	if err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	initial := &hostGeneration{id: id, dispatcher: compiled, current: true}
	host := &Host{
		starter: starter, compiled: base, decorators: decorators,
		rootDone: root.Done(), cancel: cancel, activation: make(chan struct{}, 1), closeGate: make(chan struct{}, 1),
		changed: make(chan struct{}), epoch: epoch,
		current: initial, available: map[stage.PlanID]*hostGeneration{id: initial},
		owned: map[*hostGeneration]struct{}{initial: {}},
	}
	host.activation <- struct{}{}
	host.closeGate <- struct{}{}
	return host, nil
}

// Activate stages a complete runtime plugin Set and atomically makes its
// immutable dispatch generation current. A failure never changes the current
// generation.
func (host *Host) Activate(ctx context.Context, set Set) (stage.PlanID, error) {
	if ctx == nil || host == nil {
		return "", errors.New("runtime plugin activation context is required")
	}
	if err := set.Validate(); err != nil {
		return "", err
	}
	operation, sequence, finish, err := host.beginActivation(ctx, set.Len())
	if err != nil {
		return "", err
	}
	defer finish()

	candidates := make([]generationCandidate, 0, set.Len())
	for _, executable := range set.Executables() {
		value, startErr := host.starter.start(operation, executable)
		if value != nil {
			candidates = append(candidates, value)
		}
		if startErr != nil {
			host.cleanAborted(candidates)
			return "", errors.New("runtime plugin activation failed while staging a candidate")
		}
	}
	dispatcher, composeErr := host.compose(candidates)
	if composeErr != nil {
		host.cleanAborted(candidates)
		return "", composeErr
	}
	if err = operation.Err(); err != nil {
		host.cleanAborted(candidates)
		return "", err
	}
	if err = candidateHealth(candidates); err != nil {
		host.cleanAborted(candidates)
		return "", errors.New("runtime plugin activation candidate became unhealthy")
	}
	id, err := generationPlanID(host.epoch[:], sequence, candidates, dispatcher.Definitions())
	if err != nil {
		host.cleanAborted(candidates)
		return "", err
	}
	next := &hostGeneration{id: id, dispatcher: dispatcher, candidates: candidates, current: true}

	host.mu.Lock()
	if host.closing || operation.Err() != nil {
		host.mu.Unlock()
		host.cleanAborted(candidates)
		if operation.Err() != nil {
			return "", operation.Err()
		}
		return "", errors.New("runtime plugin host is closing")
	}
	if candidateHealth(candidates) != nil {
		host.mu.Unlock()
		host.cleanAborted(candidates)
		return "", errors.New("runtime plugin activation candidate became unhealthy")
	}
	previous := host.current
	previous.current = false
	host.current = next
	host.available[id] = next
	host.owned[next] = struct{}{}
	if previous.refs == 0 {
		host.queueCleanupLocked(previous, 0)
	}
	host.signalLocked()
	host.mu.Unlock()
	for _, value := range candidates {
		go host.observe(next, value)
	}
	return id, nil
}

func (host *Host) beginActivation(
	ctx context.Context,
	candidateCount int,
) (context.Context, uint64, func(), error) {
	select {
	case <-ctx.Done():
		return nil, 0, nil, ctx.Err()
	case <-host.rootDone:
		return nil, 0, nil, errors.New("runtime plugin host is closing")
	case <-host.activation:
	}
	host.mu.Lock()
	if host.closing {
		host.mu.Unlock()
		host.activation <- struct{}{}
		return nil, 0, nil, errors.New("runtime plugin host is closing")
	}
	if len(host.owned) >= maximumRetainedGenerations || host.candidateCountLocked()+candidateCount > maximumRetainedCandidates {
		host.mu.Unlock()
		host.activation <- struct{}{}
		return nil, 0, nil, errors.New("runtime plugin retained-generation limit is reached")
	}
	host.sequence++
	sequence := host.sequence
	host.activations++
	host.signalLocked()
	host.mu.Unlock()
	operation, cancel := context.WithCancel(ctx)
	stopRoot := make(chan struct{})
	go func() {
		select {
		case <-host.rootDone:
			cancel()
		case <-stopRoot:
		}
	}()
	return operation, sequence, func() {
		close(stopRoot)
		cancel()
		host.mu.Lock()
		host.activations--
		host.signalLocked()
		host.mu.Unlock()
		host.activation <- struct{}{}
	}, nil
}

func (host *Host) compose(candidates []generationCandidate) (stage.ToolDispatcher, error) {
	compiledNames := make(map[string]struct{})
	for _, definition := range host.compiled.Definitions() {
		if err := definition.Validate(); err != nil {
			return nil, errors.New("compiled tool definition is invalid")
		}
		compiledNames[definition.Name()] = struct{}{}
	}
	runtimeTools := make(map[string]tool.Tool)
	for _, candidate := range candidates {
		for name, implementation := range candidate.tools() {
			if _, collision := compiledNames[name]; collision {
				return nil, fmt.Errorf("runtime tool %q collides with a compiled tool", name)
			}
			if _, collision := runtimeTools[name]; collision {
				return nil, fmt.Errorf("runtime tool %q is provided by multiple plugins", name)
			}
			runtimeTools[name] = implementation
		}
	}
	runtime, err := stage.NewDispatcher(runtimeTools)
	if err != nil {
		return nil, fmt.Errorf("compose runtime tool dispatcher: %w", err)
	}
	merged, err := newMergedDispatcher(host.compiled, runtime)
	if err != nil {
		return nil, err
	}
	decorated, err := stage.ApplyToolDispatchDecorators(merged, host.decorators)
	if err != nil {
		return nil, fmt.Errorf("decorate runtime tool dispatcher: %w", err)
	}
	return decorated, nil
}

// LeaseCurrent acquires the generation selected for a new run. A crashed
// current generation fails closed and is never replaced by the compiled base.
func (host *Host) LeaseCurrent(ctx context.Context) (*stage.ToolPlanLease, error) {
	return host.lease(ctx, "", true)
}

// LeaseGeneration acquires exactly the requested immutable generation. A
// retired generation remains recoverable only while another lease retains it.
func (host *Host) LeaseGeneration(ctx context.Context, id stage.PlanID) (*stage.ToolPlanLease, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return host.lease(ctx, id, false)
}

func (host *Host) lease(ctx context.Context, id stage.PlanID, current bool) (*stage.ToolPlanLease, error) {
	if ctx == nil || host == nil {
		return nil, errors.New("runtime plugin lease context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host.mu.Lock()
	if host.closing {
		host.mu.Unlock()
		return nil, errors.New("runtime plugin host is closing")
	}
	generation := host.current
	if !current {
		generation = host.available[id]
	}
	if generation == nil || generation.cleaned || generation.cleaning || (!generation.current && generation.refs == 0) {
		host.mu.Unlock()
		return nil, errors.New("runtime plugin generation is unavailable")
	}
	if failure := host.generationHealthLocked(generation); failure != nil {
		host.mu.Unlock()
		return nil, errors.New("runtime plugin generation is unhealthy")
	}
	generation.refs++
	host.signalLocked()
	host.mu.Unlock()

	lease, err := stage.NewToolPlanLease(generation.id, generation.dispatcher, func() error {
		host.release(generation)
		return nil
	})
	if err != nil {
		host.release(generation)
		return nil, err
	}
	host.mu.Lock()
	failure := host.generationHealthLocked(generation)
	host.mu.Unlock()
	if failure != nil {
		_ = lease.Release()
		return nil, errors.New("runtime plugin generation is unhealthy")
	}
	if err = ctx.Err(); err != nil {
		_ = lease.Release()
		return nil, err
	}
	return lease, nil
}

func (host *Host) release(generation *hostGeneration) {
	host.mu.Lock()
	if generation.refs > 0 {
		generation.refs--
	}
	if !generation.current && generation.refs == 0 {
		host.queueCleanupLocked(generation, 0)
	}
	host.signalLocked()
	host.mu.Unlock()
}

func (host *Host) observe(generation *hostGeneration, candidate generationCandidate) {
	select {
	case <-candidate.done():
		host.mu.Lock()
		if _, owned := host.owned[generation]; owned && !generation.cleaned {
			failure := candidate.healthFailure()
			if failure == nil {
				failure = errors.New("runtime plugin candidate stopped")
			}
			generation.unhealthy = failure
			host.signalLocked()
		}
		host.mu.Unlock()
	case <-host.rootDone:
	}
}

// Close rejects new work, cancels and joins staging, and waits until every
// active lease and owned cleanup completes. If cleanup fails or ctx expires,
// ownership remains with Host and a later Close retries it.
func (host *Host) Close(ctx context.Context) error {
	if host == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("runtime plugin host close context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-host.closeGate:
	}
	defer func() { host.closeGate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	attempt := host.beginClose()

	for {
		done, changed, err := host.closeProgress(attempt)
		if done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (host *Host) beginClose() uint64 {
	host.mu.Lock()
	defer host.mu.Unlock()
	if !host.closing {
		host.closing = true
		host.cancel()
		if host.current != nil {
			host.current.current = false
		}
		host.signalLocked()
	}
	host.closeTry++
	return host.closeTry
}

func (host *Host) closeProgress(attempt uint64) (bool, <-chan struct{}, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.activations == 0 {
		for generation := range host.owned {
			if generation.refs == 0 && !generation.cleaning && !generation.cleaned && generation.cleanupTry < attempt {
				host.queueCleanupLocked(generation, attempt)
			}
		}
	}
	refs, cleaning, failed := host.closeStateLocked(attempt)
	if host.activations != 0 || refs != 0 || cleaning != 0 {
		return false, host.changed, nil
	}
	if len(host.owned) == 0 {
		clear(host.epoch[:])
		return true, nil, nil
	}
	if failed {
		return true, nil, errors.New("runtime plugin host cleanup failed")
	}
	return false, host.changed, nil
}

func (host *Host) closeStateLocked(attempt uint64) (refs, cleaning int, failed bool) {
	for generation := range host.owned {
		refs += generation.refs
		if generation.cleaning {
			cleaning++
		}
		failed = failed || generation.cleanupTry == attempt && generation.cleanupErr != nil
	}
	return refs, cleaning, failed
}

func (host *Host) cleanAborted(candidates []generationCandidate) {
	if len(candidates) == 0 {
		return
	}
	operation, cancel := context.WithTimeout(context.Background(), MaximumOperationTimeout)
	err := closeCandidates(operation, candidates)
	cancel()
	if err == nil {
		return
	}
	generation := &hostGeneration{candidates: slices.Clone(candidates), cleanupErr: err}
	host.mu.Lock()
	host.owned[generation] = struct{}{}
	host.signalLocked()
	host.mu.Unlock()
}

func (host *Host) queueCleanupLocked(generation *hostGeneration, attempt uint64) {
	if generation == nil || generation.cleaning || generation.cleaned || generation.refs != 0 {
		return
	}
	generation.cleaning = true
	generation.cleanupTry = attempt
	generation.cleanupErr = nil
	if generation.id != "" {
		delete(host.available, generation.id)
	}
	go host.cleanup(generation)
}

func (host *Host) cleanup(generation *hostGeneration) {
	operation, cancel := context.WithTimeout(context.Background(), MaximumOperationTimeout)
	err := closeCandidates(operation, generation.candidates)
	cancel()
	host.mu.Lock()
	generation.cleaning = false
	generation.cleanupErr = err
	if err == nil {
		generation.cleaned = true
		delete(host.owned, generation)
	}
	host.signalLocked()
	host.mu.Unlock()
}

func closeCandidates(ctx context.Context, candidates []generationCandidate) error {
	var failures []error
	for _, candidate := range slices.Backward(candidates) {
		var err error
		for range 2 {
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
			err = candidate.close(ctx)
			if err == nil {
				break
			}
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (host *Host) generationHealthLocked(generation *hostGeneration) error {
	if generation.unhealthy != nil {
		return generation.unhealthy
	}
	if failure := candidateHealth(generation.candidates); failure != nil {
		generation.unhealthy = failure
		return failure
	}
	return nil
}

func candidateHealth(candidates []generationCandidate) error {
	for _, candidate := range candidates {
		select {
		case <-candidate.done():
			if failure := candidate.healthFailure(); failure != nil {
				return failure
			}
			return errors.New("runtime plugin candidate stopped")
		default:
		}
	}
	return nil
}

func (host *Host) candidateCountLocked() int {
	count := 0
	for generation := range host.owned {
		count += len(generation.candidates)
	}
	return count
}

func (host *Host) signalLocked() {
	close(host.changed)
	host.changed = make(chan struct{})
}

func generationPlanID(
	epoch []byte,
	sequence uint64,
	candidates []generationCandidate,
	definitions []tool.Definition,
) (stage.PlanID, error) {
	if len(epoch) != hostEpochBytes {
		return "", errors.New("runtime plugin host epoch is invalid")
	}
	hash := sha256.New()
	writeHostHashField(hash, epoch)
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], sequence)
	_, _ = hash.Write(number[:])
	for _, candidate := range candidates {
		identity := candidate.launchIdentity()
		if len(identity) == 0 {
			return "", errors.New("runtime plugin candidate launch identity is missing")
		}
		writeHostHashField(hash, identity)
		clear(identity)
	}
	definitions = slices.Clone(definitions)
	slices.SortFunc(definitions, func(left, right tool.Definition) int {
		return strings.Compare(left.Name(), right.Name())
	})
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return "", errors.New("runtime plugin generation contains an invalid definition")
		}
		writeHostHashField(hash, []byte(definition.Name()))
		writeHostHashField(hash, []byte(definition.Fingerprint()))
	}
	return stage.NewPlanID(fmt.Sprintf("runtime:%020d:%x", sequence, hash.Sum(nil)))
}

func readHostEpoch(source io.Reader) ([hostEpochBytes]byte, error) {
	var epoch [hostEpochBytes]byte
	if _, err := io.ReadFull(source, epoch[:]); err != nil {
		clear(epoch[:])
		return epoch, errors.New("generate runtime plugin host identity")
	}
	allZero := true
	for _, value := range epoch {
		allZero = allZero && value == 0
	}
	if allZero {
		clear(epoch[:])
		return epoch, errors.New("runtime plugin host identity is zero")
	}
	return epoch, nil
}

type hostHashWriter interface{ Write([]byte) (int, error) }

func writeHostHashField(destination hostHashWriter, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = destination.Write(size[:])
	_, _ = destination.Write(value)
}

type mergedDispatcher struct {
	compiled    stage.ToolDispatcher
	runtime     stage.ToolDispatcher
	definitions []tool.Definition
	runtimeName map[string]struct{}
}

func newMergedDispatcher(compiled, runtime stage.ToolDispatcher) (*mergedDispatcher, error) {
	if compiled == nil || runtime == nil {
		return nil, errors.New("runtime plugin merged dispatcher is incomplete")
	}
	definitions := append(compiled.Definitions(), runtime.Definitions()...)
	slices.SortFunc(definitions, func(left, right tool.Definition) int {
		return strings.Compare(left.Name(), right.Name())
	})
	for index, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return nil, errors.New("runtime plugin merged definition is invalid")
		}
		if index > 0 && definitions[index-1].Name() == definition.Name() {
			return nil, errors.New("runtime plugin merged definitions collide")
		}
	}
	runtimeNames := make(map[string]struct{})
	for _, definition := range runtime.Definitions() {
		runtimeNames[definition.Name()] = struct{}{}
	}
	return &mergedDispatcher{
		compiled: compiled, runtime: runtime, definitions: definitions, runtimeName: runtimeNames,
	}, nil
}

func (dispatcher *mergedDispatcher) Definitions() []tool.Definition {
	result := make([]tool.Definition, len(dispatcher.definitions))
	for index, definition := range dispatcher.definitions {
		result[index] = definition.Clone()
	}
	return result
}

func (dispatcher *mergedDispatcher) Definition(name string) (tool.Definition, bool) {
	index, found := slices.BinarySearchFunc(dispatcher.definitions, name, func(definition tool.Definition, target string) int {
		return strings.Compare(definition.Name(), target)
	})
	if !found {
		return tool.Definition{}, false
	}
	return dispatcher.definitions[index].Clone(), true
}

func (dispatcher *mergedDispatcher) Dispatch(
	ctx context.Context,
	call tool.Call,
	reporter tool.Reporter,
) (tool.Result, error) {
	if _, runtime := dispatcher.runtimeName[call.Name()]; runtime {
		return dispatcher.runtime.Dispatch(ctx, call, reporter)
	}
	return dispatcher.compiled.Dispatch(ctx, call, reporter)
}

var _ stage.ToolPlanSource = (*Host)(nil)
