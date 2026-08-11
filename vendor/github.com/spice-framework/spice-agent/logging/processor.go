package logging

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/tool"
	"github.com/spice-framework/spice/lifecycle"
	spicelogging "github.com/spice-framework/spice/logging"
)

type processorStats struct {
	processed      uint64
	handled        uint64
	logFailures    uint64
	decodeFailures uint64
	suppressed     uint64
	closed         bool
}

// Snapshot is one immutable accounting and health view.
type Snapshot struct {
	processed       uint64
	handled         uint64
	filtered        uint64
	overflowDropped uint64
	logFailures     uint64
	decodeFailures  uint64
	closed          bool
}

func (snapshot Snapshot) Processed() uint64       { return snapshot.processed }
func (snapshot Snapshot) Handled() uint64         { return snapshot.handled }
func (snapshot Snapshot) Filtered() uint64        { return snapshot.filtered }
func (snapshot Snapshot) OverflowDropped() uint64 { return snapshot.overflowDropped }
func (snapshot Snapshot) LogFailures() uint64     { return snapshot.logFailures }
func (snapshot Snapshot) DecodeFailures() uint64  { return snapshot.decodeFailures }
func (snapshot Snapshot) Closed() bool            { return snapshot.closed }

// Processor owns exactly one consumer for one Agent logging mailbox.
type Processor struct {
	mailbox         *event.BestEffortObserver
	logger          *spicelogging.Logger
	key             [correlationKeyBytes]byte
	includeProgress bool

	mu    sync.Mutex
	stats processorStats

	consumerDone chan struct{}
	closeDone    chan struct{}
	closeOnce    sync.Once
}

// NewMailbox constructs the sole queue used by Agent logging. Model deltas are
// always filtered; tool progress is admitted only by explicit configuration.
func NewMailbox(config Config) (*event.BestEffortObserver, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	excluded := []event.Kind{event.ModelDelta}
	if !config.IncludeProgress {
		excluded = append(excluded, event.ToolProgress)
	}
	filter, err := event.NewBestEffortFilter(excluded...)
	if err != nil {
		return nil, err
	}
	return event.NewFilteredBestEffortObserver(config.MailboxCapacity, filter)
}

// NewProcessor starts one lifecycle-owned consumer. Logger failures remain
// diagnostic accounting and never become Agent execution failures.
func NewProcessor(
	config Config,
	mailbox *event.BestEffortObserver,
	logger *spicelogging.Logger,
) (*Processor, lifecycle.Cleanup, error) {
	if err := config.Validate(); err != nil {
		return nil, nil, err
	}
	if mailbox == nil {
		return nil, nil, errors.New("agent logging processor requires a mailbox")
	}
	if logger == nil {
		return nil, nil, errors.New("agent logging processor requires a Spice logger")
	}
	key := config.CorrelationKey.material
	if !config.CorrelationKey.set {
		if _, err := rand.Read(key[:]); err != nil {
			return nil, nil, errors.New("agent logging correlation key generation failed")
		}
	}
	processor := &Processor{
		mailbox: mailbox, logger: logger, key: key,
		includeProgress: config.IncludeProgress,
		consumerDone:    make(chan struct{}), closeDone: make(chan struct{}),
	}
	go processor.consume()
	return processor, processor.Close, nil
}

// Snapshot returns exact race-safe accounting. Expected filtering and capacity
// overflow are kept separate.
func (processor *Processor) Snapshot() Snapshot {
	if processor == nil {
		return Snapshot{}
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()
	filtered := processor.stats.suppressed
	dropped := uint64(0)
	if processor.mailbox != nil {
		filtered += processor.mailbox.Filtered()
		dropped = processor.mailbox.Dropped()
	}
	return Snapshot{
		processed: processor.stats.processed, handled: processor.stats.handled,
		filtered: filtered, overflowDropped: dropped,
		logFailures: processor.stats.logFailures, decodeFailures: processor.stats.decodeFailures,
		closed: processor.stats.closed,
	}
}

// Close closes the mailbox after producers stop and waits for every accepted
// envelope to drain. Cancellation ends the wait without claiming completion.
func (processor *Processor) Close(ctx context.Context) error {
	if processor == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("agent logging cleanup context must not be nil")
	}
	processor.closeOnce.Do(func() {
		processor.mailbox.Close()
		go processor.finish()
	})
	select {
	case <-processor.closeDone:
		return nil
	case <-ctx.Done():
		return errors.New("agent logging final drain did not complete before cancellation")
	}
}

func (processor *Processor) finish() {
	<-processor.consumerDone
	processor.mu.Lock()
	processor.stats.closed = true
	processor.mu.Unlock()
	close(processor.closeDone)
}

func (processor *Processor) consume() {
	defer close(processor.consumerDone)
	lastDropped := uint64(0)
	var lastAt time.Time
	for envelope := range processor.mailbox.Events() {
		lastAt = envelope.At()
		processor.process(envelope)
		lastDropped = processor.reportDrops(lastDropped, lastAt)
	}
	processor.reportDrops(lastDropped, lastAt)
}

func (processor *Processor) reportDrops(previous uint64, timestamp time.Time) uint64 {
	current := processor.mailbox.Dropped()
	if current <= previous {
		return current
	}
	processor.emit(spicelogging.Record{
		Timestamp: timestamp, Level: spicelogging.LevelWarn, Event: "agent.events_dropped",
		Message: "Agent logging events were dropped.",
		Fields: []spicelogging.Field{
			spicelogging.Uint64("dropped_count", current-previous),
			spicelogging.Uint64("dropped_total", current),
		},
	})
	return current
}

func (processor *Processor) process(envelope event.Envelope) {
	if envelope.Kind() == event.ModelDelta ||
		envelope.Kind() == event.ToolProgress && !processor.includeProgress {
		processor.mu.Lock()
		processor.stats.suppressed++
		processor.mu.Unlock()
		return
	}
	processor.mu.Lock()
	processor.stats.processed++
	processor.mu.Unlock()

	runCorrelation := spicelogging.String("run_correlation", processor.digest("run", envelope.RunID()))
	fields := []spicelogging.Field{
		runCorrelation,
		spicelogging.Uint64("sequence", envelope.Sequence()),
	}
	if envelope.Kind() == event.ToolStarted || envelope.Kind() == event.ToolCompleted || envelope.Kind() == event.ToolFailed {
		var err error
		fields, err = processor.toolFields(envelope, fields)
		if err != nil {
			processor.recordDecodeFailure(envelope, runCorrelation)
			return
		}
	}
	level, message := eventPresentation(envelope.Kind())
	processor.emit(spicelogging.Record{
		Timestamp: envelope.At(), Level: level,
		Event: "agent." + string(envelope.Kind()), Message: message, Fields: fields,
	})
}

func (processor *Processor) toolFields(
	envelope event.Envelope,
	fields []spicelogging.Field,
) ([]spicelogging.Field, error) {
	switch envelope.Kind() {
	case event.ToolStarted:
		occurrence, err := agent.DecodeToolStartedOccurrence(envelope.Data())
		if err != nil {
			return nil, err
		}
		fields = append(
			fields,
			spicelogging.String("tool_call_correlation", processor.digest("call", envelope.RunID()+"\x00"+string(occurrence.CallID()))),
			spicelogging.String("effect", string(occurrence.Effect())),
			spicelogging.String("replay_safety", string(occurrence.ReplaySafety())),
			spicelogging.String("capabilities", canonicalCapabilities(occurrence.Capabilities())),
			spicelogging.Bool("declared", occurrence.Declared()),
			spicelogging.Bool("executable", occurrence.Executable()),
		)
		return fields, nil
	case event.ToolCompleted, event.ToolFailed:
		occurrence, err := agent.DecodeToolTerminalOccurrence(envelope.Kind(), envelope.Data())
		if err != nil {
			return nil, err
		}
		fields = append(
			fields,
			spicelogging.String("tool_call_correlation", processor.digest("call", envelope.RunID()+"\x00"+string(occurrence.CallID()))),
		)
		if occurrence.ExecutionState() != "" {
			fields = append(fields, spicelogging.String("execution_state", string(occurrence.ExecutionState())))
		}
		if occurrence.RetryDisposition() != "" {
			fields = append(fields, spicelogging.String("retry_disposition", string(occurrence.RetryDisposition())))
		}
		return fields, nil
	default:
		return fields, nil
	}
}

func (processor *Processor) recordDecodeFailure(
	envelope event.Envelope,
	runCorrelation spicelogging.Field,
) {
	processor.mu.Lock()
	processor.stats.decodeFailures++
	processor.mu.Unlock()
	processor.emit(spicelogging.Record{
		Timestamp: envelope.At(), Level: spicelogging.LevelWarn,
		Event: "agent.event_decode_failed", Message: "An Agent event could not be decoded safely.",
		Fields: []spicelogging.Field{
			runCorrelation,
			spicelogging.Uint64("sequence", envelope.Sequence()),
			spicelogging.String("event_kind", string(envelope.Kind())),
		},
	})
}

func (processor *Processor) emit(record spicelogging.Record) {
	err := processor.logger.Emit(context.Background(), record)
	processor.mu.Lock()
	if err != nil {
		processor.stats.logFailures++
	} else {
		processor.stats.handled++
	}
	processor.mu.Unlock()
}

func (processor *Processor) digest(domain, value string) string {
	mac := hmac.New(sha256.New, processor.key[:])
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func canonicalCapabilities(capabilities []tool.Capability) string {
	values := make([]string, len(capabilities))
	for index, capability := range capabilities {
		values[index] = string(capability)
	}
	slices.Sort(values)
	return strings.Join(values, ",")
}

func eventPresentation(kind event.Kind) (spicelogging.Level, string) {
	switch kind {
	case event.RunStarted:
		return spicelogging.LevelInfo, "Agent run started."
	case event.RunCompleted:
		return spicelogging.LevelInfo, "Agent run completed."
	case event.RunCancelled:
		return spicelogging.LevelInfo, "Agent run was cancelled."
	case event.RunFailed:
		return spicelogging.LevelError, "Agent run failed."
	case event.TurnFailed, event.ModelFailed, event.ToolFailed, event.InteractionFailed:
		return spicelogging.LevelError, "Agent operation failed."
	case event.InteractionCancelled:
		return spicelogging.LevelInfo, "Agent interaction was cancelled."
	case event.ToolProgress:
		return spicelogging.LevelTrace, "Agent tool progress was observed."
	case event.TurnStarted, event.ModelStarted, event.ToolStarted, event.InteractionStarted:
		return spicelogging.LevelDebug, "Agent operation started."
	default:
		return spicelogging.LevelDebug, "Agent operation completed."
	}
}
