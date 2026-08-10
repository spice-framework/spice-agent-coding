package daemon

import (
	"fmt"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/event"
)

type engineLogPolicy struct{}

func (engineLogPolicy) limits(limits client.Limits) (event.LogLimits, error) {
	maxEvents := int(limits.ReplayEvents())
	if maxEvents < 1 || uint64(maxEvents) != uint64(limits.ReplayEvents()) {
		return event.LogLimits{}, fmt.Errorf(
			"construct daemon event log: replay event limit %d does not fit this platform",
			limits.ReplayEvents(),
		)
	}
	maxBytes := int(limits.ReplayBytes()) // #nosec G115 -- exact positive round-trip validation follows immediately.
	if maxBytes < 1 || uint64(maxBytes) != limits.ReplayBytes() {
		return event.LogLimits{}, fmt.Errorf(
			"construct daemon event log: replay byte limit %d does not fit this platform",
			limits.ReplayBytes(),
		)
	}
	logLimits := event.DefaultLogLimits()
	if maxEvents > logLimits.MaxEvents || maxBytes > logLimits.MaxBytes {
		return event.LogLimits{}, fmt.Errorf(
			"construct daemon event log: replay limit %d events/%d bytes exceeds retained history %d events/%d bytes",
			maxEvents,
			maxBytes,
			logLimits.MaxEvents,
			logLimits.MaxBytes,
		)
	}
	logLimits.SubscriberMaxEvents = maxEvents
	logLimits.SubscriberMaxBytes = maxBytes
	return logLimits, nil
}
