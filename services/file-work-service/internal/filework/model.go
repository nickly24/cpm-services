package filework

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	Exchange     = "cpm.events"
	CommandRoute = "files.pdf.process.requested.v1"
	CommandQueue = "filework.pdf.v1"
	ResultRoute  = "homework.pdf.completed.v1"
	ResultQueue  = "homework.pdf.results.v1"
	DeadQueue    = "filework.pdf.dlq.v1"
)

type Envelope[T any] struct {
	MessageID     string    `json:"message_id"`
	MessageType   string    `json:"message_type"`
	SchemaVersion int       `json:"schema_version"`
	OccurredAt    time.Time `json:"occurred_at"`
	Producer      string    `json:"producer"`
	CorrelationID string    `json:"correlation_id"`
	Payload       T         `json:"payload"`
}

type Limits struct {
	MaxSourceBytes int64 `json:"max_source_bytes"`
	MaxOutputBytes int64 `json:"max_output_bytes"`
	MaxPages       int   `json:"max_pages"`
	DPI            int   `json:"dpi"`
	JPEGQuality    int   `json:"jpeg_quality"`
}

type Command struct {
	JobID          string `json:"job_id"`
	Generation     int64  `json:"generation"`
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	SourceKey      string `json:"source_key"`
	SourceETag     string `json:"source_etag"`
	OutputKey      string `json:"output_key"`
	Limits         Limits `json:"limits"`
}

type Result struct {
	JobID          string `json:"job_id"`
	Generation     int64  `json:"generation"`
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	OutputKey      string `json:"output_key"`
	SizeBytes      int64  `json:"size_bytes"`
	PageCount      int    `json:"page_count"`
	SHA256         string `json:"sha256"`
	ErrorCode      string `json:"error_code"`
	ClaimToken     string `json:"claim_token"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func DecodeCommand(body []byte) (Envelope[Command], error) {
	var event Envelope[Command]
	if len(body) > 32*1024 {
		return event, errors.New("command_too_large")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&event); err != nil {
		return event, errors.New("invalid_command")
	}
	if d.Decode(new(any)) != io.EOF {
		return event, errors.New("invalid_command")
	}
	if event.SchemaVersion != 1 || event.MessageType != CommandRoute || event.MessageID == "" || len(event.MessageID) > 128 || event.OccurredAt.IsZero() || event.Producer != "homework" || len(event.CorrelationID) > 128 {
		return event, errors.New("invalid_envelope")
	}
	if err := event.Payload.Validate(); err != nil {
		return event, err
	}
	return event, nil
}

func (c Command) Validate() error {
	if !uuidPattern.MatchString(c.JobID) || c.Generation < 1 || c.OrganizationID < 1 || c.DivisionID < 1 {
		return errors.New("invalid_job_identity")
	}
	prefix := fmt.Sprintf("homework/%d/%d/", c.OrganizationID, c.DivisionID)
	if !strings.HasPrefix(c.SourceKey, prefix+"staging/") || strings.Contains(c.SourceKey, "..") || strings.ContainsAny(c.SourceKey, "\\\x00\r\n") || len(c.SourceKey) > 1024 || c.SourceETag == "" || len(c.SourceETag) > 128 || strings.ContainsAny(c.SourceETag, "\r\n") {
		return errors.New("invalid_source")
	}
	if c.OutputKey != fmt.Sprintf("%sprocessed/%s/%d.pdf", prefix, c.JobID, c.Generation) {
		return errors.New("invalid_output")
	}
	return c.Limits.Validate()
}

func (l Limits) Validate() error {
	// The persisted policy can be stricter. These ceilings protect worker resources
	// even if a malformed message or configuration bypasses the public API.
	if l.MaxSourceBytes < 1 || l.MaxSourceBytes > 100*1024*1024 || l.MaxOutputBytes < 1 || l.MaxOutputBytes > 100*1024*1024 || l.MaxPages < 1 || l.MaxPages > 200 || l.DPI < 72 || l.DPI > 300 || l.JPEGQuality < 35 || l.JPEGQuality > 95 {
		return errors.New("invalid_limits")
	}
	return nil
}

func (c Command) Result(code string) Result {
	return Result{JobID: c.JobID, Generation: c.Generation, OrganizationID: c.OrganizationID, DivisionID: c.DivisionID, OutputKey: c.OutputKey, ErrorCode: code}
}
