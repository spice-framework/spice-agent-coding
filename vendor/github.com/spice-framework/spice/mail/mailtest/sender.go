// Package mailtest provides an instance-owned, bounded mail sender for tests
// and local reference applications.
package mailtest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"net/textproto"
	"slices"
	"strings"
	"sync"

	spicemail "github.com/spice-framework/spice/mail"
)

const (
	// MaxCapacity bounds one sender's retained attempt and observation history.
	MaxCapacity          = 10_000
	maxSnapshotMIMEBytes = 25 << 20
)

var (
	// ErrCapacityExceeded reports a send that could not be retained because
	// the configured attempt history is full.
	ErrCapacityExceeded = errors.New("mail test sender capacity exceeded")
	// ErrInvalidMessage reports a Message value that does not contain the
	// valid MIME produced by mail.NewMessage.
	ErrInvalidMessage = errors.New("mail test sender received invalid message")
)

// Outcome classifies one test delivery attempt without retaining message
// content in observations.
type Outcome string

const (
	// OutcomeDelivered reports a successfully recorded delivery.
	OutcomeDelivered Outcome = "delivered"
	// OutcomeFailed reports a configured deterministic transport failure.
	OutcomeFailed Outcome = "failed"
	// OutcomeCanceled reports cancellation observed after snapshot creation.
	OutcomeCanceled Outcome = "canceled"
	// OutcomeRejected reports an attempt rejected by the capacity bound.
	OutcomeRejected Outcome = "rejected"
)

// Observation is bounded delivery metadata. It deliberately excludes
// recipients, subjects, bodies, attachments, and error text.
type Observation struct {
	Attempt   uint64
	MessageID string
	Outcome   Outcome
}

// Observer receives one synchronous observation after sender state is
// unlocked.
type Observer func(context.Context, Observation)

// Config defines one immutable test sender policy. Failures are indexed by
// one-based accepted attempt number; a nil entry succeeds.
type Config struct {
	Capacity int
	Failures []error
	Observer Observer
}

// CapacityError describes an explicitly rejected send.
type CapacityError struct {
	Capacity int
	Attempt  uint64
}

// Error implements error.
func (err *CapacityError) Error() string {
	return fmt.Sprintf(
		"mail test sender capacity %d exceeded at attempt %d",
		err.Capacity,
		err.Attempt,
	)
}

// Unwrap supports errors.Is with ErrCapacityExceeded.
func (err *CapacityError) Unwrap() error {
	return ErrCapacityExceeded
}

// Attachment is one immutable delivered MIME attachment.
type Attachment struct {
	filename    string
	contentType string
	data        []byte
}

// Filename returns the delivered attachment filename.
func (attachment Attachment) Filename() string {
	return attachment.filename
}

// ContentType returns the normalized delivered media type.
func (attachment Attachment) ContentType() string {
	return attachment.contentType
}

// Bytes returns a defensive attachment-content copy.
func (attachment Attachment) Bytes() []byte {
	return slices.Clone(attachment.data)
}

// Snapshot is one immutable view of the exact delivered envelope and MIME.
type Snapshot struct {
	id           string
	envelopeFrom string
	recipients   []string
	subject      string
	textBody     string
	htmlBody     string
	attachments  []Attachment
	content      []byte
}

// ID returns the delivered Message-ID without angle brackets.
func (snapshot Snapshot) ID() string {
	return snapshot.id
}

// EnvelopeFrom returns the SMTP envelope sender.
func (snapshot Snapshot) EnvelopeFrom() string {
	return snapshot.envelopeFrom
}

// Recipients returns the stable de-duplicated SMTP recipient envelope.
func (snapshot Snapshot) Recipients() []string {
	return slices.Clone(snapshot.recipients)
}

// Subject returns the decoded Subject header.
func (snapshot Snapshot) Subject() string {
	return snapshot.subject
}

// TextBody returns the decoded, CRLF-normalized plain-text body.
func (snapshot Snapshot) TextBody() string {
	return snapshot.textBody
}

// HTMLBody returns the decoded, CRLF-normalized HTML body.
func (snapshot Snapshot) HTMLBody() string {
	return snapshot.htmlBody
}

// Attachments returns deep defensive attachment copies in MIME order.
func (snapshot Snapshot) Attachments() []Attachment {
	return cloneAttachments(snapshot.attachments)
}

// Bytes returns a defensive copy of the complete delivered MIME.
func (snapshot Snapshot) Bytes() []byte {
	return slices.Clone(snapshot.content)
}

// Attempt is one accepted delivery attempt.
type Attempt struct {
	number  uint64
	message Snapshot
	outcome Outcome
	sendErr error
}

// Number returns the one-based sender attempt number.
func (attempt Attempt) Number() uint64 {
	return attempt.number
}

// Message returns a defensive immutable delivered snapshot.
func (attempt Attempt) Message() Snapshot {
	return cloneSnapshot(attempt.message)
}

// Outcome returns the delivery result class.
func (attempt Attempt) Outcome() Outcome {
	return attempt.outcome
}

// Error returns the configured failure or cancellation result.
func (attempt Attempt) Error() error {
	return attempt.sendErr
}

// Sender records bounded immutable delivery attempts.
type Sender struct {
	mu           sync.RWMutex
	capacity     int
	failures     []error
	observer     Observer
	nextAttempt  uint64
	attempts     []Attempt
	observations []Observation
}

var _ spicemail.Sender = (*Sender)(nil)

// New validates and creates one isolated sender.
func New(config Config) (*Sender, error) {
	if config.Capacity < 1 || config.Capacity > MaxCapacity {
		return nil, fmt.Errorf(
			"mail test sender capacity must be between 1 and %d",
			MaxCapacity,
		)
	}
	if len(config.Failures) > config.Capacity {
		return nil, errors.New(
			"mail test sender failure plan must not exceed capacity",
		)
	}
	return &Sender{
		capacity: config.Capacity,
		failures: slices.Clone(config.Failures),
		observer: config.Observer,
		attempts: make([]Attempt, 0, config.Capacity),
		observations: make(
			[]Observation,
			0,
			config.Capacity,
		),
	}, nil
}

// Send records one immutable snapshot or returns an explicit failure. A
// context already canceled on entry is returned immediately and consumes no
// attempt capacity.
func (sender *Sender) Send(
	ctx context.Context,
	message spicemail.Message,
) error {
	if ctx == nil {
		return errors.New("mail test sender context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := snapshotMessage(message)
	if err != nil {
		return fmt.Errorf("%w: inspect MIME: %w", ErrInvalidMessage, err)
	}

	sender.mu.Lock()
	sender.nextAttempt++
	number := sender.nextAttempt
	if len(sender.attempts) == sender.capacity {
		observer := sender.observer
		notification := Observation{
			Attempt:   number,
			MessageID: snapshot.ID(),
			Outcome:   OutcomeRejected,
		}
		sender.mu.Unlock()
		observe(ctx, observer, notification)
		return &CapacityError{
			Capacity: sender.capacity,
			Attempt:  number,
		}
	}
	sendErr := ctx.Err()
	outcome := OutcomeDelivered
	if sendErr != nil {
		outcome = OutcomeCanceled
	} else if number <= uint64(len(sender.failures)) {
		sendErr = sender.failures[number-1]
		if sendErr != nil {
			outcome = OutcomeFailed
		}
	}
	attempt := Attempt{
		number:  number,
		message: snapshot,
		outcome: outcome,
		sendErr: sendErr,
	}
	notification := Observation{
		Attempt:   number,
		MessageID: snapshot.ID(),
		Outcome:   outcome,
	}
	sender.attempts = append(sender.attempts, attempt)
	sender.observations = append(sender.observations, notification)
	observer := sender.observer
	sender.mu.Unlock()

	observe(ctx, observer, notification)
	return sendErr
}

// AttemptCount returns all numbered valid-message attempts, including
// capacity rejections.
func (sender *Sender) AttemptCount() uint64 {
	sender.mu.RLock()
	defer sender.mu.RUnlock()
	return sender.nextAttempt
}

// Attempts returns deep defensive accepted-attempt copies in attempt order.
func (sender *Sender) Attempts() []Attempt {
	sender.mu.RLock()
	defer sender.mu.RUnlock()
	result := make([]Attempt, len(sender.attempts))
	for index, attempt := range sender.attempts {
		result[index] = cloneAttempt(attempt)
	}
	return result
}

// Messages returns only successfully delivered snapshots in attempt order.
func (sender *Sender) Messages() []Snapshot {
	sender.mu.RLock()
	defer sender.mu.RUnlock()
	result := make([]Snapshot, 0, len(sender.attempts))
	for _, attempt := range sender.attempts {
		if attempt.outcome == OutcomeDelivered {
			result = append(result, cloneSnapshot(attempt.message))
		}
	}
	return result
}

// Observations returns bounded payload-free accepted-attempt observations.
// Capacity rejections reach the configured Observer but are not retained after
// the history is full.
func (sender *Sender) Observations() []Observation {
	sender.mu.RLock()
	defer sender.mu.RUnlock()
	return slices.Clone(sender.observations)
}

func observe(
	ctx context.Context,
	observer Observer,
	observation Observation,
) {
	if observer != nil {
		observer(ctx, observation)
	}
}

func cloneAttempt(attempt Attempt) Attempt {
	attempt.message = cloneSnapshot(attempt.message)
	return attempt
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.recipients = slices.Clone(snapshot.recipients)
	snapshot.attachments = cloneAttachments(snapshot.attachments)
	snapshot.content = slices.Clone(snapshot.content)
	return snapshot
}

func cloneAttachments(attachments []Attachment) []Attachment {
	result := make([]Attachment, len(attachments))
	for index, attachment := range attachments {
		result[index] = attachment
		result[index].data = slices.Clone(attachment.data)
	}
	return result
}

type parsedContent struct {
	textBody    string
	htmlBody    string
	attachments []Attachment
}

func snapshotMessage(message spicemail.Message) (Snapshot, error) {
	return snapshotMIME(
		message.ID(),
		message.EnvelopeFrom(),
		message.Recipients(),
		message.Bytes(),
	)
}

func snapshotMIME(
	id string,
	envelopeFrom string,
	recipients []string,
	content []byte,
) (Snapshot, error) {
	if id == "" || envelopeFrom == "" || len(recipients) == 0 {
		return Snapshot{}, errors.New("mail envelope is incomplete")
	}
	if len(content) == 0 || len(content) > maxSnapshotMIMEBytes {
		return Snapshot{}, errors.New("mail MIME size is invalid")
	}
	message, err := netmail.ReadMessage(bytes.NewReader(content))
	if err != nil {
		return Snapshot{}, fmt.Errorf("parse MIME headers: %w", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(
		message.Header.Get("Subject"),
	)
	if err != nil || subject == "" {
		return Snapshot{}, errors.New("decode MIME subject")
	}
	parsed := parsedContent{}
	if err := readEntity(
		textproto.MIMEHeader(message.Header),
		message.Body,
		&parsed,
	); err != nil {
		return Snapshot{}, err
	}
	if parsed.textBody == "" && parsed.htmlBody == "" {
		return Snapshot{}, errors.New("MIME body is missing")
	}
	return Snapshot{
		id:           id,
		envelopeFrom: envelopeFrom,
		recipients:   slices.Clone(recipients),
		subject:      subject,
		textBody:     parsed.textBody,
		htmlBody:     parsed.htmlBody,
		attachments:  parsed.attachments,
		content:      slices.Clone(content),
	}, nil
}

func readEntity(
	header textproto.MIMEHeader,
	body io.Reader,
	parsed *parsedContent,
) error {
	mediaType, parameters, err := mime.ParseMediaType(
		header.Get("Content-Type"),
	)
	if err != nil {
		return fmt.Errorf("parse MIME content type: %w", err)
	}
	disposition, dispositionParameters, dispositionErr := mime.ParseMediaType(header.Get("Content-Disposition"))
	if dispositionErr == nil && disposition == "attachment" {
		return readAttachment(
			header,
			body,
			mime.FormatMediaType(mediaType, parameters),
			dispositionParameters["filename"],
			parsed,
		)
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := parameters["boundary"]
		if boundary == "" {
			return errors.New("parse MIME multipart: boundary is missing")
		}
		return readMultipart(body, boundary, parsed)
	}
	decoded, err := readTransferEncoded(header, body)
	if err != nil {
		return err
	}
	switch mediaType {
	case "text/plain":
		if parsed.textBody != "" {
			return errors.New("parse MIME: duplicate text body")
		}
		parsed.textBody = string(decoded)
	case "text/html":
		if parsed.htmlBody != "" {
			return errors.New("parse MIME: duplicate HTML body")
		}
		parsed.htmlBody = string(decoded)
	default:
		return fmt.Errorf("parse MIME: unsupported body type %q", mediaType)
	}
	return nil
}

func readMultipart(
	body io.Reader,
	boundary string,
	parsed *parsedContent,
) error {
	reader := multipart.NewReader(body, boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse MIME multipart: %w", err)
		}
		readErr := readEntity(part.Header, part, parsed)
		closeErr := part.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return fmt.Errorf("close MIME part: %w", closeErr)
		}
	}
}

func readAttachment(
	header textproto.MIMEHeader,
	body io.Reader,
	contentType string,
	filename string,
	parsed *parsedContent,
) error {
	if filename == "" {
		return errors.New("parse MIME attachment: filename is missing")
	}
	data, err := readTransferEncoded(header, body)
	if err != nil {
		return err
	}
	parsed.attachments = append(parsed.attachments, Attachment{
		filename:    filename,
		contentType: contentType,
		data:        data,
	})
	return nil
}

func readTransferEncoded(
	header textproto.MIMEHeader,
	body io.Reader,
) ([]byte, error) {
	reader := body
	switch strings.ToLower(strings.TrimSpace(
		header.Get("Content-Transfer-Encoding"),
	)) {
	case "", "7bit", "8bit", "binary":
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		reader = quotedprintable.NewReader(body)
	default:
		return nil, errors.New("parse MIME: unsupported transfer encoding")
	}
	content, err := io.ReadAll(io.LimitReader(
		reader,
		maxSnapshotMIMEBytes+1,
	))
	if err != nil {
		return nil, fmt.Errorf("read MIME content: %w", err)
	}
	if len(content) > maxSnapshotMIMEBytes {
		return nil, errors.New("parse MIME: decoded content exceeds limit")
	}
	return content, nil
}
