package daemon

import (
	"math"
	"testing"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/event"
)

func TestEngineLogLimitsMatchAdvertisedReplayCapacity(t *testing.T) {
	t.Parallel()
	limits, err := NewLimits()
	if err != nil {
		t.Fatalf("NewLimits() error = %v", err)
	}
	got, err := (engineLogPolicy{}).limits(limits)
	if err != nil {
		t.Fatalf("engineLogLimits() error = %v", err)
	}
	if got.SubscriberMaxEvents != int(limits.ReplayEvents()) ||
		got.SubscriberMaxBytes != int(limits.ReplayBytes()) {
		t.Fatalf(
			"subscriber limits = %d events/%d bytes, advertised = %d events/%d bytes",
			got.SubscriberMaxEvents,
			got.SubscriberMaxBytes,
			limits.ReplayEvents(),
			limits.ReplayBytes(),
		)
	}
	defaults := event.DefaultLogLimits()
	if got.MaxEvents != defaults.MaxEvents || got.MaxBytes != defaults.MaxBytes ||
		got.TerminalReserveEvents != defaults.TerminalReserveEvents ||
		got.TerminalReserveBytes != defaults.TerminalReserveBytes {
		t.Fatalf("authoritative limits changed: got %#v, defaults %#v", got, defaults)
	}
}

func TestEngineLogLimitsAcceptExactRetainedBoundary(t *testing.T) {
	t.Parallel()
	defaults := event.DefaultLogLimits()
	const (
		retainedEvents = 8192
		retainedBytes  = 32 << 20
	)
	if defaults.MaxEvents != retainedEvents || defaults.MaxBytes != retainedBytes {
		t.Fatalf("unexpected retained defaults: %#v", defaults)
	}
	limits, err := client.NewLimits(
		1,
		1,
		retainedEvents,
		retainedBytes,
		1,
		1,
	)
	if err != nil {
		t.Fatalf("NewLimits() error = %v", err)
	}
	got, err := (engineLogPolicy{}).limits(limits)
	if err != nil {
		t.Fatalf("engineLogLimits() error = %v", err)
	}
	if got.SubscriberMaxEvents != defaults.MaxEvents || got.SubscriberMaxBytes != defaults.MaxBytes {
		t.Fatalf("boundary subscriber limits = %#v, want %#v", got, defaults)
	}
}

func TestEngineLogLimitsRejectReplayBytesOutsidePlatformInt(t *testing.T) {
	t.Parallel()
	limits, err := client.NewLimits(1, 1, 1, math.MaxUint64, 1, 1)
	if err != nil {
		t.Fatalf("NewLimits() error = %v", err)
	}
	if _, err = (engineLogPolicy{}).limits(limits); err == nil {
		t.Fatal("engineLogLimits() accepted replay bytes outside platform int")
	}
}

func TestEngineLogLimitsRejectInvalidOrUnretainableCapacity(t *testing.T) {
	t.Parallel()
	if _, err := (engineLogPolicy{}).limits(client.Limits{}); err == nil {
		t.Fatal("engineLogLimits() accepted zero limits")
	}
	defaults := event.DefaultLogLimits()
	tests := []struct {
		name   string
		events uint32
		bytes  uint64
	}{
		{
			name:   "events exceed retained history",
			events: uint32(defaults.MaxEvents + 1),
			bytes:  uint64(defaults.MaxEvents + 1),
		},
		{name: "bytes exceed retained history", events: 1, bytes: uint64(defaults.MaxBytes + 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			limits, err := client.NewLimits(1, 1, test.events, test.bytes, 1, 1)
			if err != nil {
				t.Fatalf("NewLimits() error = %v", err)
			}
			if _, err = (engineLogPolicy{}).limits(limits); err == nil {
				t.Fatal("engineLogLimits() accepted an unretainable replay capacity")
			}
		})
	}
}
