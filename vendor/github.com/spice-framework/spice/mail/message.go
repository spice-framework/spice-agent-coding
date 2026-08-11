// Package mail provides immutable, bounded MIME messages and a transport-
// neutral sender contract.
package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"net/textproto"
	"path"
	"slices"
	"strings"
	"time"
)

const (
	maxAddressBytes        = 512
	maxAddressHeaderBytes  = 900
	maxRecipients          = 100
	maxSubjectBytes        = 256
	maxMessageIDBytes      = 255
	maxBodyBytes           = 1 << 20
	maxAttachments         = 16
	maxAttachmentBytes     = 10 << 20
	maxAttachmentTotal     = 16 << 20
	maxFilenameBytes       = 255
	maxSerializedMailBytes = 25 << 20
	base64LineBytes        = 76
)

// AttachmentSpec describes one ordinary MIME attachment.
type AttachmentSpec struct {
	Filename    string
	ContentType string
	Data        []byte
}

// MessageSpec is the inspectable input to NewMessage. Date and ID are
// caller-owned so serialization has no hidden clock, hostname, or randomness.
type MessageSpec struct {
	ID          string
	Date        time.Time
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	ReplyTo     string
	Subject     string
	TextBody    string
	HTMLBody    string
	Attachments []AttachmentSpec
}

// Message is an immutable serialized MIME message plus its SMTP envelope.
type Message struct {
	id         string
	from       string
	recipients []string
	content    []byte
}

// Sender delivers one immutable message. Implementations own transport
// security, cancellation, retry, and observation policy.
type Sender interface {
	Send(context.Context, Message) error
}

type normalizedAddress struct {
	header   string
	envelope string
}

type attachment struct {
	filename    string
	contentType string
	data        []byte
}

type normalizedMessage struct {
	id          string
	date        time.Time
	from        normalizedAddress
	to          []normalizedAddress
	cc          []normalizedAddress
	bcc         []normalizedAddress
	replyTo     *normalizedAddress
	subject     string
	textBody    string
	htmlBody    string
	attachments []attachment
}

type normalizedEnvelope struct {
	from    normalizedAddress
	to      []normalizedAddress
	cc      []normalizedAddress
	bcc     []normalizedAddress
	replyTo *normalizedAddress
}

// NewMessage validates, copies, and deterministically serializes one message.
func NewMessage(spec MessageSpec) (Message, error) {
	normalized, err := normalizeMessage(spec)
	if err != nil {
		return Message{}, err
	}
	content, err := serializeMessage(normalized)
	if err != nil {
		return Message{}, err
	}
	if len(content) > maxSerializedMailBytes {
		return Message{}, fmt.Errorf(
			"construct mail message: serialized message exceeds %d bytes",
			maxSerializedMailBytes,
		)
	}
	recipients := make([]string, 0, len(normalized.to)+len(normalized.cc)+len(normalized.bcc))
	seen := make(map[string]struct{}, cap(recipients))
	for _, group := range [][]normalizedAddress{
		normalized.to,
		normalized.cc,
		normalized.bcc,
	} {
		for _, recipient := range group {
			if _, duplicate := seen[recipient.envelope]; duplicate {
				continue
			}
			seen[recipient.envelope] = struct{}{}
			recipients = append(recipients, recipient.envelope)
		}
	}
	return Message{
		id:         normalized.id,
		from:       normalized.from.envelope,
		recipients: recipients,
		content:    content,
	}, nil
}

// ID returns the caller-owned Message-ID without angle brackets.
func (message Message) ID() string {
	return message.id
}

// EnvelopeFrom returns the normalized SMTP envelope sender.
func (message Message) EnvelopeFrom() string {
	return message.from
}

// Recipients returns de-duplicated To, Cc, and Bcc envelope recipients in that
// stable order.
func (message Message) Recipients() []string {
	return slices.Clone(message.recipients)
}

// Bytes returns a defensive copy of the complete MIME message.
func (message Message) Bytes() []byte {
	return append([]byte(nil), message.content...)
}

func normalizeMessage(spec MessageSpec) (normalizedMessage, error) {
	if err := validateMessageID(spec.ID); err != nil {
		return normalizedMessage{}, err
	}
	if spec.Date.IsZero() {
		return normalizedMessage{}, errors.New("construct mail message: date is required")
	}
	envelope, err := normalizeEnvelope(spec)
	if err != nil {
		return normalizedMessage{}, err
	}
	if subjectErr := validateSubject(spec.Subject); subjectErr != nil {
		return normalizedMessage{}, subjectErr
	}
	if bodyErr := validateBodies(spec.TextBody, spec.HTMLBody); bodyErr != nil {
		return normalizedMessage{}, bodyErr
	}
	attachments, err := normalizeAttachments(spec.Attachments)
	if err != nil {
		return normalizedMessage{}, err
	}
	return normalizedMessage{
		id:          spec.ID,
		date:        spec.Date.UTC().Truncate(time.Second),
		from:        envelope.from,
		to:          envelope.to,
		cc:          envelope.cc,
		bcc:         envelope.bcc,
		replyTo:     envelope.replyTo,
		subject:     spec.Subject,
		textBody:    spec.TextBody,
		htmlBody:    spec.HTMLBody,
		attachments: attachments,
	}, nil
}

func normalizeEnvelope(spec MessageSpec) (normalizedEnvelope, error) {
	from, err := parseAddress("from", spec.From)
	if err != nil {
		return normalizedEnvelope{}, err
	}
	to, err := parseAddresses("to", spec.To)
	if err != nil {
		return normalizedEnvelope{}, err
	}
	cc, err := parseAddresses("cc", spec.Cc)
	if err != nil {
		return normalizedEnvelope{}, err
	}
	bcc, err := parseAddresses("bcc", spec.Bcc)
	if err != nil {
		return normalizedEnvelope{}, err
	}
	count := len(to) + len(cc) + len(bcc)
	if count < 1 {
		return normalizedEnvelope{}, errors.New(
			"construct mail message: at least one recipient is required",
		)
	}
	if count > maxRecipients {
		return normalizedEnvelope{}, fmt.Errorf(
			"construct mail message: recipients exceed %d entries",
			maxRecipients,
		)
	}
	var replyTo *normalizedAddress
	if spec.ReplyTo != "" {
		parsed, parseErr := parseAddress("reply-to", spec.ReplyTo)
		if parseErr != nil {
			return normalizedEnvelope{}, parseErr
		}
		replyTo = &parsed
	}
	return normalizedEnvelope{
		from:    from,
		to:      to,
		cc:      cc,
		bcc:     bcc,
		replyTo: replyTo,
	}, nil
}

func validateBodies(textBody, htmlBody string) error {
	if textBody == "" && htmlBody == "" {
		return errors.New("construct mail message: text or HTML body is required")
	}
	if len(textBody) > maxBodyBytes || len(htmlBody) > maxBodyBytes {
		return fmt.Errorf(
			"construct mail message: each body must not exceed %d bytes",
			maxBodyBytes,
		)
	}
	return nil
}

func validateMessageID(value string) error {
	if len(value) < 3 ||
		len(value) > maxMessageIDBytes ||
		strings.TrimSpace(value) != value ||
		strings.Count(value, "@") != 1 ||
		strings.HasPrefix(value, "@") ||
		strings.HasSuffix(value, "@") ||
		!printableASCII(value) ||
		strings.ContainsAny(value, "<>()[]\\,;:\"") {
		return errors.New(
			"construct mail message: ID must be a bounded ASCII addr-spec without brackets",
		)
	}
	return nil
}

func parseAddresses(field string, values []string) ([]normalizedAddress, error) {
	result := make([]normalizedAddress, len(values))
	for index, value := range values {
		address, err := parseAddress(fmt.Sprintf("%s recipient %d", field, index), value)
		if err != nil {
			return nil, err
		}
		result[index] = address
	}
	return result, nil
}

func parseAddress(field, value string) (normalizedAddress, error) {
	if value == "" ||
		len(value) > maxAddressBytes ||
		strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n") {
		return normalizedAddress{}, fmt.Errorf(
			"construct mail message: %s address is invalid",
			field,
		)
	}
	parsed, err := netmail.ParseAddress(value)
	if err != nil ||
		parsed.Address == "" ||
		!printableASCII(parsed.Address) ||
		strings.Count(parsed.Address, "@") != 1 ||
		strings.HasPrefix(parsed.Address, "@") ||
		strings.HasSuffix(parsed.Address, "@") {
		return normalizedAddress{}, fmt.Errorf(
			"construct mail message: %s address is invalid",
			field,
		)
	}
	header := parsed.String()
	if len(header) > maxAddressHeaderBytes ||
		strings.ContainsAny(header, "\r\n") {
		return normalizedAddress{}, fmt.Errorf(
			"construct mail message: %s address is too long",
			field,
		)
	}
	return normalizedAddress{header: header, envelope: parsed.Address}, nil
}

func validateSubject(value string) error {
	if value == "" ||
		len(value) > maxSubjectBytes ||
		strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf(
			"construct mail message: subject must contain between 1 and %d bytes without surrounding space or line breaks",
			maxSubjectBytes,
		)
	}
	return nil
}

func normalizeAttachments(specs []AttachmentSpec) ([]attachment, error) {
	if len(specs) > maxAttachments {
		return nil, fmt.Errorf(
			"construct mail message: attachments exceed %d entries",
			maxAttachments,
		)
	}
	result := make([]attachment, len(specs))
	total := 0
	for index, spec := range specs {
		normalized, err := normalizeAttachment(index, spec)
		if err != nil {
			return nil, err
		}
		total += len(spec.Data)
		if total > maxAttachmentTotal {
			return nil, fmt.Errorf(
				"construct mail message: attachment data exceeds %d bytes",
				maxAttachmentTotal,
			)
		}
		result[index] = normalized
	}
	return result, nil
}

func normalizeAttachment(index int, spec AttachmentSpec) (attachment, error) {
	if spec.Filename == "" ||
		len(spec.Filename) > maxFilenameBytes ||
		path.Base(spec.Filename) != spec.Filename ||
		strings.Contains(spec.Filename, "\\") ||
		strings.ContainsAny(spec.Filename, "\r\n") {
		return attachment{}, fmt.Errorf(
			"construct mail message: attachment %d filename is invalid",
			index,
		)
	}
	if len(spec.Data) < 1 || len(spec.Data) > maxAttachmentBytes {
		return attachment{}, fmt.Errorf(
			"construct mail message: attachment %d must contain between 1 and %d bytes",
			index,
			maxAttachmentBytes,
		)
	}
	contentType, err := normalizeAttachmentContentType(index, spec.ContentType)
	if err != nil {
		return attachment{}, err
	}
	return attachment{
		filename:    spec.Filename,
		contentType: contentType,
		data:        append([]byte(nil), spec.Data...),
	}, nil
}

func normalizeAttachmentContentType(index int, value string) (string, error) {
	if value == "" {
		return "application/octet-stream", nil
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	typeName, subtype, separated := strings.Cut(mediaType, "/")
	if err != nil ||
		!separated ||
		typeName == "" ||
		subtype == "" ||
		strings.HasPrefix(mediaType, "multipart/") {
		return "", fmt.Errorf(
			"construct mail message: attachment %d content type is invalid",
			index,
		)
	}
	return mime.FormatMediaType(mediaType, parameters), nil
}

func serializeMessage(message normalizedMessage) ([]byte, error) {
	var output bytes.Buffer
	writeHeader(&output, "Date", message.date.Format(time.RFC1123Z))
	writeHeader(&output, "Message-ID", "<"+message.id+">")
	writeHeader(&output, "From", message.from.header)
	writeAddressHeader(&output, "To", message.to)
	writeAddressHeader(&output, "Cc", message.cc)
	if message.replyTo != nil {
		writeHeader(&output, "Reply-To", message.replyTo.header)
	}
	writeHeader(&output, "Subject", encodeHeader(message.subject))
	writeHeader(&output, "MIME-Version", "1.0")

	switch {
	case len(message.attachments) > 0:
		boundary := messageBoundary(message, "mixed")
		writeHeader(
			&output,
			"Content-Type",
			mime.FormatMediaType("multipart/mixed", map[string]string{
				"boundary": boundary,
			}),
		)
		output.WriteString("\r\n")
		if err := writeMixed(&output, boundary, message); err != nil {
			return nil, err
		}
	case message.textBody != "" && message.htmlBody != "":
		boundary := messageBoundary(message, "alternative")
		writeHeader(
			&output,
			"Content-Type",
			mime.FormatMediaType("multipart/alternative", map[string]string{
				"boundary": boundary,
			}),
		)
		output.WriteString("\r\n")
		if err := writeAlternative(
			&output,
			boundary,
			message.textBody,
			message.htmlBody,
		); err != nil {
			return nil, err
		}
	default:
		mediaType, body := singleBody(message)
		writeHeader(&output, "Content-Type", textContentType(mediaType))
		writeHeader(&output, "Content-Transfer-Encoding", "quoted-printable")
		output.WriteString("\r\n")
		if err := writeQuotedPrintable(&output, body); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func writeMixed(
	destination io.Writer,
	boundary string,
	message normalizedMessage,
) error {
	writer := multipart.NewWriter(destination)
	if err := writer.SetBoundary(boundary); err != nil {
		return fmt.Errorf("construct mail message: set mixed boundary: %w", err)
	}
	if err := writeBodyPart(writer, message); err != nil {
		return err
	}
	for index, attachment := range message.attachments {
		if err := writeAttachment(writer, index, attachment); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("construct mail message: close mixed body: %w", err)
	}
	return nil
}

func writeBodyPart(writer *multipart.Writer, message normalizedMessage) error {
	if message.textBody != "" && message.htmlBody != "" {
		boundary := messageBoundary(message, "alternative")
		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type": {mime.FormatMediaType(
				"multipart/alternative",
				map[string]string{"boundary": boundary},
			)},
		})
		if err != nil {
			return fmt.Errorf("construct mail message: create alternative part: %w", err)
		}
		return writeAlternative(part, boundary, message.textBody, message.htmlBody)
	}
	mediaType, body := singleBody(message)
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {textContentType(mediaType)},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return fmt.Errorf("construct mail message: create body part: %w", err)
	}
	return writeQuotedPrintable(part, body)
}

func writeAlternative(
	destination io.Writer,
	boundary string,
	textBody string,
	htmlBody string,
) error {
	writer := multipart.NewWriter(destination)
	if err := writer.SetBoundary(boundary); err != nil {
		return fmt.Errorf("construct mail message: set alternative boundary: %w", err)
	}
	for _, body := range []struct {
		mediaType string
		value     string
	}{
		{mediaType: "text/plain", value: textBody},
		{mediaType: "text/html", value: htmlBody},
	} {
		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {textContentType(body.mediaType)},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return fmt.Errorf(
				"construct mail message: create %s body: %w",
				body.mediaType,
				err,
			)
		}
		if err := writeQuotedPrintable(part, body.value); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("construct mail message: close alternative body: %w", err)
	}
	return nil
}

func writeAttachment(
	writer *multipart.Writer,
	index int,
	value attachment,
) error {
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {mime.FormatMediaType(
			"attachment",
			map[string]string{"filename": value.filename},
		)},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Type":              {value.contentType},
	})
	if err != nil {
		return fmt.Errorf(
			"construct mail message: create attachment %d: %w",
			index,
			err,
		)
	}
	lines := &base64Lines{destination: part}
	encoder := base64.NewEncoder(base64.StdEncoding, lines)
	if _, err := encoder.Write(value.data); err != nil {
		return fmt.Errorf("construct mail message: encode attachment %d: %w", index, err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("construct mail message: close attachment %d encoder: %w", index, err)
	}
	if err := lines.Close(); err != nil {
		return fmt.Errorf("construct mail message: finish attachment %d: %w", index, err)
	}
	return nil
}

type base64Lines struct {
	destination io.Writer
	column      int
}

func (writer *base64Lines) Write(value []byte) (int, error) {
	consumed := 0
	for len(value) > 0 {
		remaining := base64LineBytes - writer.column
		count := min(len(value), remaining)
		written, err := writer.destination.Write(value[:count])
		consumed += written
		writer.column += written
		if err != nil {
			return consumed, err
		}
		if written != count {
			return consumed, io.ErrShortWrite
		}
		value = value[count:]
		if writer.column == base64LineBytes {
			if _, err := io.WriteString(writer.destination, "\r\n"); err != nil {
				return consumed, err
			}
			writer.column = 0
		}
	}
	return consumed, nil
}

func (writer *base64Lines) Close() error {
	if writer.column == 0 {
		return nil
	}
	_, err := io.WriteString(writer.destination, "\r\n")
	return err
}

func writeQuotedPrintable(destination io.Writer, value string) error {
	writer := quotedprintable.NewWriter(destination)
	if _, err := io.WriteString(writer, canonicalBody(value)); err != nil {
		return fmt.Errorf("construct mail message: encode body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("construct mail message: close body encoder: %w", err)
	}
	return nil
}

func canonicalBody(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

func singleBody(message normalizedMessage) (string, string) {
	if message.textBody != "" {
		return "text/plain", message.textBody
	}
	return "text/html", message.htmlBody
}

func textContentType(mediaType string) string {
	return mime.FormatMediaType(mediaType, map[string]string{"charset": "utf-8"})
}

func messageBoundary(message normalizedMessage, kind string) string {
	for counter := 0; ; counter++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf(
			"%s\x00%s\x00%d",
			message.id,
			kind,
			counter,
		)))
		boundary := fmt.Sprintf("spice_%s_%x", kind, sum[:16])
		if !messageContains(message, boundary) {
			return boundary
		}
	}
}

func messageContains(message normalizedMessage, value string) bool {
	if strings.Contains(message.textBody, value) ||
		strings.Contains(message.htmlBody, value) {
		return true
	}
	for _, attachment := range message.attachments {
		if bytes.Contains(attachment.data, []byte(value)) {
			return true
		}
	}
	return false
}

func writeAddressHeader(
	output *bytes.Buffer,
	name string,
	addresses []normalizedAddress,
) {
	if len(addresses) == 0 {
		return
	}
	output.WriteString(name)
	output.WriteString(": ")
	for index, address := range addresses {
		if index > 0 {
			output.WriteString(",\r\n ")
		}
		output.WriteString(address.header)
	}
	output.WriteString("\r\n")
}

func writeHeader(output *bytes.Buffer, name, value string) {
	output.WriteString(name)
	output.WriteString(": ")
	output.WriteString(value)
	output.WriteString("\r\n")
}

func encodeHeader(value string) string {
	if printableASCII(value) {
		return value
	}
	return mime.QEncoding.Encode("utf-8", value)
}

func printableASCII(value string) bool {
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}
