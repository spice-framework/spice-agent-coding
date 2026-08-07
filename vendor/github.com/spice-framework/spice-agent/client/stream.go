package client

import (
	"errors"
	"math"
)

// EventControl describes one captured replay page and live-tail position.
type EventControl struct {
	earliest      uint64
	latest        uint64
	lastDelivered uint64
	pageLast      uint64
	hasPageLast   bool
	hasMore       bool
	tailing       bool
}

// NewEventControl constructs the current explicit-page control form.
func NewEventControl(
	earliest,
	latest,
	lastDelivered,
	pageLast uint64,
	hasMore,
	tailing bool,
) (EventControl, error) {
	value := EventControl{
		earliest: earliest, latest: latest, lastDelivered: lastDelivered,
		pageLast: pageLast, hasPageLast: true, hasMore: hasMore, tailing: tailing,
	}
	if err := value.Validate(); err != nil {
		return EventControl{}, err
	}
	return value, nil
}

// NewLegacyEventControl constructs the compatibility control form without an
// explicit page cursor. Legacy controls cannot page or tail.
func NewLegacyEventControl(earliest, latest, lastDelivered uint64) (EventControl, error) {
	value := EventControl{earliest: earliest, latest: latest, lastDelivered: lastDelivered}
	if err := value.Validate(); err != nil {
		return EventControl{}, err
	}
	return value, nil
}

func (control EventControl) EarliestSequence() uint64      { return control.earliest }
func (control EventControl) LatestSequence() uint64        { return control.latest }
func (control EventControl) LastDeliveredSequence() uint64 { return control.lastDelivered }
func (control EventControl) PageLastSequence() (uint64, bool) {
	return control.pageLast, control.hasPageLast
}
func (control EventControl) HasMore() bool { return control.hasMore }
func (control EventControl) Tailing() bool { return control.tailing }

func (control EventControl) Validate() error {
	validWindow := control.earliest > 0 && control.latest >= control.earliest
	emptyWindow := control.earliest > 0 && control.latest < math.MaxUint64 && control.earliest == control.latest+1
	if !validWindow && !emptyWindow {
		return errors.New("event control retained bounds are invalid")
	}
	if !control.hasPageLast {
		return control.validateLegacy()
	}
	return control.validateCurrent()
}

func (control EventControl) validateLegacy() error {
	if control.hasMore || control.tailing || control.pageLast != 0 {
		return errors.New("legacy event control cannot page or tail")
	}
	if control.lastDelivered < control.earliest-1 || control.lastDelivered > control.latest {
		return errors.New("legacy event delivery cursor is outside retained bounds")
	}
	return nil
}

func (control EventControl) validateCurrent() error {
	if control.pageLast < control.earliest-1 || control.pageLast > control.latest ||
		control.lastDelivered < control.earliest-1 || control.lastDelivered > control.pageLast {
		return errors.New("event control cursors exceed captured bounds")
	}
	if control.hasMore != (control.pageLast < control.latest) {
		return errors.New("event control has-more flag is inconsistent")
	}
	if control.hasMore && control.tailing {
		return errors.New("event control cannot tail a non-final page")
	}
	if control.tailing && control.pageLast != control.latest {
		return errors.New("event control may tail only from the captured latest sequence")
	}
	return nil
}

// EventFrameKind identifies the active event-stream payload.
type EventFrameKind string

const (
	EventFrameEvent   EventFrameKind = "event"
	EventFrameControl EventFrameKind = "control"
)

// EventFrame is a closed union of one event or one successful control record.
// Error controls are translated into typed client errors by the adapter.
type EventFrame struct {
	kind    EventFrameKind
	event   Event
	control EventControl
}

func NewEventFrame(event Event) (EventFrame, error) {
	if _, err := NewEvent(event.run, event.sequence, event.at, event.kind, event.detail); err != nil {
		return EventFrame{}, err
	}
	return EventFrame{kind: EventFrameEvent, event: event}, nil
}

func NewEventControlFrame(control EventControl) (EventFrame, error) {
	if err := control.Validate(); err != nil {
		return EventFrame{}, err
	}
	return EventFrame{kind: EventFrameControl, control: control}, nil
}

func (frame EventFrame) Kind() EventFrameKind { return frame.kind }
func (frame EventFrame) Event() (Event, bool) {
	return frame.event, frame.kind == EventFrameEvent
}

func (frame EventFrame) Control() (EventControl, bool) {
	return frame.control, frame.kind == EventFrameControl
}

// InteractionControl describes one complete-snapshot/change page and tail.
type InteractionControl struct {
	latestRevision   uint64
	pageLastRevision uint64
	hasMore          bool
	tailing          bool
}

func NewInteractionControl(latestRevision, pageLastRevision uint64, hasMore, tailing bool) (InteractionControl, error) {
	value := InteractionControl{
		latestRevision: latestRevision, pageLastRevision: pageLastRevision,
		hasMore: hasMore, tailing: tailing,
	}
	if err := value.Validate(); err != nil {
		return InteractionControl{}, err
	}
	return value, nil
}

func (control InteractionControl) LatestRevision() uint64   { return control.latestRevision }
func (control InteractionControl) PageLastRevision() uint64 { return control.pageLastRevision }
func (control InteractionControl) HasMore() bool            { return control.hasMore }
func (control InteractionControl) Tailing() bool            { return control.tailing }

func (control InteractionControl) Validate() error {
	if control.pageLastRevision > control.latestRevision {
		return errors.New("interaction page cursor exceeds captured latest revision")
	}
	if control.hasMore != (control.pageLastRevision < control.latestRevision) {
		return errors.New("interaction control has-more flag is inconsistent")
	}
	if control.hasMore && control.tailing {
		return errors.New("interaction control cannot tail a non-final page")
	}
	if control.tailing && control.pageLastRevision != control.latestRevision {
		return errors.New("interaction control may tail only from the captured latest revision")
	}
	return nil
}

// InteractionFrameKind identifies the active interaction-stream payload.
type InteractionFrameKind string

const (
	InteractionFrameUpdate  InteractionFrameKind = "update"
	InteractionFrameControl InteractionFrameKind = "control"
)

// InteractionFrame is a closed union of a snapshot/change or successful
// control record. A valid stream begins with a snapshot update.
type InteractionFrame struct {
	kind    InteractionFrameKind
	update  InteractionUpdate
	control InteractionControl
}

func NewInteractionFrame(update InteractionUpdate) (InteractionFrame, error) {
	if err := update.validate(); err != nil {
		return InteractionFrame{}, err
	}
	return InteractionFrame{kind: InteractionFrameUpdate, update: update}, nil
}

func NewInteractionControlFrame(control InteractionControl) (InteractionFrame, error) {
	if err := control.Validate(); err != nil {
		return InteractionFrame{}, err
	}
	return InteractionFrame{kind: InteractionFrameControl, control: control}, nil
}

func (frame InteractionFrame) Kind() InteractionFrameKind { return frame.kind }
func (frame InteractionFrame) Update() (InteractionUpdate, bool) {
	return frame.update, frame.kind == InteractionFrameUpdate
}

func (frame InteractionFrame) Control() (InteractionControl, bool) {
	return frame.control, frame.kind == InteractionFrameControl
}
