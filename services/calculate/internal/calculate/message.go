package calculate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

const MaxMessageBytes = 4 << 20

// Content change events carry both full snapshots; grading events carry IDs.
const MaxContentMessageBytes = 16 << 20

var ErrInvalidMessage = errors.New("invalid message")
var ErrMessageConflict = errors.New("message ID already contains another body")
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type Envelope struct {
	MessageID     string          `json:"message_id"`
	MessageType   string          `json:"message_type"`
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Producer      string          `json:"producer"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type Message struct {
	ID                 string          `json:"message_id" bson:"_id"`
	Type               string          `json:"message_type" bson:"message_type"`
	SchemaVersion      int             `json:"schema_version" bson:"schema_version"`
	OccurredAt         time.Time       `json:"occurred_at" bson:"occurred_at"`
	Producer           string          `json:"producer" bson:"producer"`
	CorrelationID      string          `json:"correlation_id" bson:"correlation_id"`
	CausationID        string          `json:"causation_id,omitempty" bson:"causation_id,omitempty"`
	SubmissionID       string          `json:"submission_id,omitempty" bson:"submission_id,omitempty"`
	TestID             string          `json:"test_id,omitempty" bson:"test_id,omitempty"`
	OrganizationID     int64           `json:"organization_id,omitempty" bson:"organization_id,omitempty"`
	DivisionID         int64           `json:"division_id,omitempty" bson:"division_id,omitempty"`
	AccountID          int64           `json:"account_id,omitempty" bson:"account_id,omitempty"`
	JobID              string          `json:"job_id,omitempty" bson:"job_id,omitempty"`
	Revision           int64           `json:"revision,omitempty" bson:"revision,omitempty"`
	InputHash          string          `json:"input_hash,omitempty" bson:"input_hash,omitempty"`
	BodyHash           string          `json:"body_hash" bson:"body_hash"`
	PayloadHash        string          `json:"payload_hash" bson:"payload_hash"`
	Body               []byte          `json:"-" bson:"body"`
	ReceivedAt         time.Time       `json:"received_at" bson:"received_at"`
	Status             string          `json:"status" bson:"status"`
	ProcessingKind     string          `json:"processing_kind,omitempty" bson:"processing_kind,omitempty"`
	AvailableAt        *time.Time      `json:"-" bson:"available_at,omitempty"`
	ProcessingAttempts int64           `json:"processing_attempts" bson:"processing_attempts,omitempty"`
	ProcessedAt        *time.Time      `json:"processed_at,omitempty" bson:"processed_at,omitempty"`
	LastError          string          `json:"last_error,omitempty" bson:"last_error,omitempty"`
	NextAttemptAt      *time.Time      `json:"next_attempt_at,omitempty" bson:"next_attempt_at,omitempty"`
	LeaseToken         string          `json:"-" bson:"lease_token,omitempty"`
	LeaseUntil         *time.Time      `json:"-" bson:"lease_until,omitempty"`
	ProcessCursor      string          `json:"process_cursor,omitempty" bson:"process_cursor,omitempty"`
	Output             json.RawMessage `json:"output,omitempty" bson:"output,omitempty"`
}

type MessageView struct {
	Message
	Payload json.RawMessage `json:"payload"`
}

func (m Message) View() MessageView {
	var e Envelope
	_ = json.Unmarshal(m.Body, &e)
	return MessageView{Message: m, Payload: e.Payload}
}

func ParseMessage(body []byte, routingKey string, now time.Time) (Message, error) {
	invalid := func(reason string) (Message, error) {
		return Message{}, fmt.Errorf("%w: %s", ErrInvalidMessage, reason)
	}
	limit := MaxMessageBytes
	if routingKey == "tests.content.changed.v1" {
		limit = MaxContentMessageBytes
	}
	if len(body) == 0 || len(body) > limit {
		return invalid("size")
	}
	if err := validateJSON(body); err != nil {
		return invalid("JSON")
	}
	var e Envelope
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return invalid("envelope")
	}
	if !uuidPattern.MatchString(e.MessageID) || e.SchemaVersion != 1 || e.Producer != "tests-service" || e.OccurredAt.IsZero() || len(e.CorrelationID) == 0 || len(e.CorrelationID) > 128 || len(e.CausationID) > 128 {
		return invalid("required fields")
	}
	_, offset := e.OccurredAt.Zone()
	if offset != 0 {
		return invalid("occurred_at must be UTC")
	}
	switch e.MessageType {
	case "tests.grade.requested", "tests.content.changed":
		if routingKey != e.MessageType+".v1" {
			return invalid("routing key")
		}
	default:
		return invalid("message type")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &payload); err != nil || payload == nil || len(payload) == 0 {
		return invalid("payload must be a nonempty object")
	}
	var submissionID, testID string
	if raw := payload["submission_id"]; raw != nil {
		if json.Unmarshal(raw, &submissionID) != nil {
			return invalid("submission_id")
		}
	}
	if raw := payload["test_id"]; raw != nil {
		if json.Unmarshal(raw, &testID) != nil {
			return invalid("test_id")
		}
	}
	if testID == "" || e.MessageType == "tests.grade.requested" && submissionID == "" {
		return invalid("payload identity")
	}
	var organizationID, divisionID, accountID int64
	for _, scope := range []struct {
		name  string
		value *int64
	}{{"organization_id", &organizationID}, {"division_id", &divisionID}, {"account_id", &accountID}} {
		if raw := payload[scope.name]; raw != nil {
			if json.Unmarshal(raw, scope.value) != nil || *scope.value <= 0 {
				return invalid("scope identity")
			}
		}
	}
	canonical, err := canonicalPayload(e.Payload)
	if err != nil {
		return invalid("payload")
	}
	bodyHash, payloadHash := sha256.Sum256(body), sha256.Sum256(canonical)
	return Message{ID: e.MessageID, Type: e.MessageType, SchemaVersion: e.SchemaVersion, OccurredAt: e.OccurredAt.UTC(), Producer: e.Producer, CorrelationID: e.CorrelationID, CausationID: e.CausationID, SubmissionID: submissionID, TestID: testID, OrganizationID: organizationID, DivisionID: divisionID, AccountID: accountID, BodyHash: hex.EncodeToString(bodyHash[:]), PayloadHash: hex.EncodeToString(payloadHash[:]), Body: append([]byte(nil), body...), ReceivedAt: now.UTC(), Status: "received"}, nil
}

func canonicalPayload(raw []byte) ([]byte, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// Reject duplicate object keys so a body cannot have two conflicting identities.
func validateJSON(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := readJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalidMessage
	}
	return nil
}

func readJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrInvalidMessage
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrInvalidMessage
			}
			seen[s] = true
			if err := readJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := readJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidMessage
	}
	_, err = d.Token()
	return err
}
