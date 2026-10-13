package ratings

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const examSourceRoute = "exams.source.changed"
const examSourceQueue = "cpm.ratings.exams-source.v1"

type SourceChange struct {
	MessageID     string `json:"message_id"`
	MessageType   string `json:"message_type"`
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	Producer      string `json:"producer"`
	CorrelationID string `json:"correlation_id"`
	Payload       struct {
		OrganizationID int64  `json:"organization_id"`
		DivisionID     int64  `json:"division_id"`
		ExamID         int64  `json:"exam_id"`
		ExamRevision   int64  `json:"exam_revision"`
		SourceRevision int64  `json:"source_revision"`
		Reason         string `json:"reason"`
	} `json:"payload"`
}

func decodeSourceChange(messageID string, raw []byte) (SourceChange, string, error) {
	var event SourceChange
	if len(raw) > 64<<10 || strict(raw, &event) != nil {
		return event, "", bad("invalid_source_event")
	}
	p := event.Payload
	_, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil || !validUUID(event.MessageID) || messageID != event.MessageID || !validUUID(event.CorrelationID) || event.MessageType != examSourceRoute || event.Producer != "exams-service" || event.SchemaVersion != 1 || p.OrganizationID < 1 || p.DivisionID < 1 || p.ExamID < 1 || p.ExamRevision < 1 || p.SourceRevision < 1 || p.Reason == "" || len(p.Reason) > 128 {
		return event, "", bad("invalid_source_event")
	}
	h, err := canonicalHash(raw, "")
	return event, h, err
}

// Commit both the inbox identity and the scoped high watermark before ACK.
// Events never create calculation jobs or change published rating rows.
func (s *Store) ApplySourceChange(ctx context.Context, event SourceChange, payloadHash string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p := event.Payload
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO ratings_source_events_v1(message_id,payload_hash,source,organization_id,division_id,source_revision,received_at) VALUES(?,?,'exams',?,?,?,?)", event.MessageID, payloadHash, p.OrganizationID, p.DivisionID, p.SourceRevision, now); err != nil {
		return err
	}
	var storedHash string
	if err = tx.QueryRowContext(ctx, "SELECT payload_hash FROM ratings_source_events_v1 WHERE message_id=? FOR UPDATE", event.MessageID).Scan(&storedHash); err != nil {
		return err
	}
	if storedHash != payloadHash {
		return conflict("source_event_identity_conflict")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO ratings_source_revisions_v1(source,organization_id,division_id,revision,updated_at) VALUES('exams',?,?,?,?) ON DUPLICATE KEY UPDATE updated_at=IF(VALUES(revision)>revision,VALUES(updated_at),updated_at),revision=GREATEST(revision,VALUES(revision))", p.OrganizationID, p.DivisionID, p.SourceRevision, now); err != nil {
		return err
	}
	return tx.Commit()
}

func sourceRevision(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, j Job, lock bool) (int64, error) {
	query := "SELECT revision FROM ratings_source_revisions_v1 WHERE source='exams' AND organization_id=? AND division_id=?"
	if lock {
		query += " FOR UPDATE"
	}
	var revision int64
	err := db.QueryRowContext(ctx, query, j.OrganizationID, j.DivisionID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

type SourceConsumer struct {
	Store *Store
	URL   string
	Log   *slog.Logger
}

func (c *SourceConsumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.session(ctx); err != nil && ctx.Err() == nil {
			c.Log.Warn("ratings source consumer disconnected")
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func declareSourceTopology(ch *amqp.Channel) error {
	for _, name := range []string{"cpm.events", "cpm.events.dead"} {
		if err := ch.ExchangeDeclare(name, "topic", true, false, false, false, nil); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare("cpm.ratings.exams-source.dead.v1", true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1)}); err != nil {
		return err
	}
	if err := ch.QueueBind("cpm.ratings.exams-source.dead.v1", "ratings.exams-source.invalid.v1", "cpm.events.dead", false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(examSourceQueue, true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1), "x-dead-letter-exchange": "cpm.events.dead", "x-dead-letter-routing-key": "ratings.exams-source.invalid.v1", "x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish"}); err != nil {
		return err
	}
	return ch.QueueBind(examSourceQueue, examSourceRoute, "cpm.events", false, nil)
}

func (c *SourceConsumer) session(ctx context.Context) error {
	conn, err := amqp.DialConfig(c.URL, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return err
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err = declareSourceTopology(ch); err != nil {
		return err
	}
	if err = ch.Qos(32, 0, false); err != nil {
		return err
	}
	deliveries, err := ch.Consume(examSourceQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("source consumer closed")
			}
			event, payloadHash, err := decodeSourceChange(delivery.MessageId, delivery.Body)
			if delivery.ContentType != "application/json" || delivery.RoutingKey != examSourceRoute || err != nil {
				if err = delivery.Nack(false, false); err != nil {
					return err
				}
				continue
			}
			work, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = c.Store.ApplySourceChange(work, event, payloadHash)
			cancel()
			if errorName(err) == "source_event_identity_conflict" {
				if err = delivery.Nack(false, false); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			} // Channel close requeues uncommitted delivery.
			if err = delivery.Ack(false); err != nil {
				return err
			}
		}
	}
}
