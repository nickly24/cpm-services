package tests

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.mongodb.org/mongo-driver/v2/bson"
	"log/slog"
	"net"
	"time"
)

type Event struct {
	MessageID     string    `json:"message_id"`
	MessageType   string    `json:"message_type"`
	SchemaVersion int       `json:"schema_version"`
	OccurredAt    time.Time `json:"occurred_at"`
	Producer      string    `json:"producer"`
	CorrelationID string    `json:"correlation_id"`
	Payload       any       `json:"payload"`
}
type storedEvent struct {
	ID         string `bson:"id"`
	Body       []byte `bson:"body"`
	RoutingKey string `bson:"routing_key"`
}

func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func NewEvent(kind string, payload any) Event {
	id := uuid()
	return Event{id, kind, 1, time.Now().UTC(), "tests-service", id, payload}
}
func storeEvent(e Event) (storedEvent, error) {
	if e.MessageID == "" {
		e.MessageID = uuid()
	}
	if e.SchemaVersion == 0 {
		e.SchemaVersion = 1
	}
	if e.Producer == "" {
		e.Producer = "tests-service"
	}
	if e.CorrelationID == "" {
		e.CorrelationID = e.MessageID
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if e.MessageType != "tests.grade.requested" && e.MessageType != "tests.content.changed" {
		return storedEvent{}, errors.New("invalid event type")
	}
	body, err := json.Marshal(e)
	return storedEvent{e.MessageID, body, e.MessageType + ".v1"}, err
}
func documentFields(d any) (bson.M, error) {
	data, e := bson.Marshal(d)
	if e != nil {
		return nil, e
	}
	var fields bson.M
	e = bson.Unmarshal(data, &fields)
	delete(fields, "_id")
	delete(fields, "outbox")
	delete(fields, "outbox_pending")
	delete(fields, "outbox_lease_until")
	delete(fields, "outbox_lease_token")
	return fields, e
}

// Pending events and accepted answers share a durable document. Publisher
// availability is deliberately independent from HTTP acceptance readiness.
func (s *Store) StartPublisher(ctx context.Context, rabbitURL string) {
	go func() {
		for ctx.Err() == nil {
			if err := s.publishConnection(ctx, rabbitURL); err != nil && ctx.Err() == nil {
				slog.Warn("tests outbox reconnecting")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}
func declareBus(ch *amqp.Channel) error {
	if e := ch.ExchangeDeclare("cpm.events", "topic", true, false, false, false, nil); e != nil {
		return e
	}
	if e := ch.ExchangeDeclare("cpm.events.dead", "topic", true, false, false, false, nil); e != nil {
		return e
	}
	if _, e := ch.QueueDeclare("calculate.events.dead.v1", true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1)}); e != nil {
		return e
	}
	if e := ch.QueueBind("calculate.events.dead.v1", "calculate.invalid.v1", "cpm.events.dead", false, nil); e != nil {
		return e
	}
	args := amqp.Table{"x-queue-type": "quorum", "x-dead-letter-exchange": "cpm.events.dead", "x-dead-letter-routing-key": "calculate.invalid.v1", "x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish", "x-delivery-limit": int32(-1)}
	if _, e := ch.QueueDeclare("calculate.events.v1", true, false, false, false, args); e != nil {
		return e
	}
	for _, key := range []string{"tests.grade.requested.v1", "tests.content.changed.v1"} {
		if e := ch.QueueBind("calculate.events.v1", key, "cpm.events", false, nil); e != nil {
			return e
		}
	}
	return ch.Confirm(false)
}

type outboxPublishChannel interface {
	GetNextPublishSeqNo() uint64
	PublishWithContext(context.Context, string, string, bool, bool, amqp.Publishing) error
}

func publishOutboxBatch(ctx context.Context, ch outboxPublishChannel, batch []leasedOutbox,
	confirms <-chan amqp.Confirmation, returns <-chan amqp.Return, closed <-chan *amqp.Error) error {
	inflight := map[uint64]bool{}
	for _, record := range batch {
		for _, pending := range record.Outbox {
			if err := ctx.Err(); err != nil {
				return err
			}
			tag := ch.GetNextPublishSeqNo()
			if err := ch.PublishWithContext(ctx, "cpm.events", pending.RoutingKey, true, false, amqp.Publishing{
				ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: pending.ID,
				Timestamp: time.Now().UTC(), Body: pending.Body,
			}); err != nil {
				return err
			}
			inflight[tag] = true
		}
	}
	// All messages are sent before waiting, so broker confirms overlap. No event
	// is removed until every message is routed and positively confirmed.
	for len(inflight) > 0 {
		select {
		case confirmation, ok := <-confirms:
			if !ok {
				return errors.New("publisher confirms closed")
			}
			if !confirmation.Ack {
				return errors.New("publish nacked")
			}
			if !inflight[confirmation.DeliveryTag] {
				return errors.New("unexpected publisher confirmation")
			}
			delete(inflight, confirmation.DeliveryTag)
		case <-returns:
			return errors.New("publish unroutable")
		case <-closed:
			return errors.New("broker disconnected")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Rabbit sends a mandatory return before its confirm; amqp091-go delivers
	// both notifications in wire order. Keep the return channel batch-buffered.
	select {
	case <-returns:
		return errors.New("publish unroutable")
	default:
		return nil
	}
}

func (s *Store) publishConnection(ctx context.Context, address string) error {
	var transport net.Conn
	dial := func(network, address string) (net.Conn, error) {
		conn, err := amqp.DefaultDial(5*time.Second)(network, address)
		if err == nil {
			transport = conn
		}
		return conn, err
	}
	conn, e := amqp.DialConfig(address, amqp.Config{Heartbeat: 10 * time.Second, Dial: dial})
	if e != nil {
		return e
	}
	defer conn.Close()
	ch, e := conn.Channel()
	if e != nil {
		return e
	}
	defer ch.Close()
	if e = declareBus(ch); e != nil {
		return e
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, outboxBatchSize))
	returns := ch.NotifyReturn(make(chan amqp.Return, outboxBatchSize))
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	for ctx.Err() == nil {
		batch, err := s.claimOutboxBatch(ctx)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-closed:
				return errors.New("broker disconnected")
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		publishContext, cancelPublish := context.WithTimeout(ctx, outboxConfirmTimeout)
		deadline, _ := publishContext.Deadline()
		// amqp091-go's PublishWithContext does not interrupt socket writes. A
		// shared write deadline bounds the entire batch, not each message.
		if err = transport.SetWriteDeadline(deadline); err == nil {
			err = publishOutboxBatch(publishContext, ch, batch, confirms, returns, closed)
		}
		cancelPublish()
		if err != nil {
			// The lease expires without removing anything: an uncertain broker
			// outcome can only produce a retry, never lose an accepted event.
			return err
		}
		if err = transport.SetWriteDeadline(time.Time{}); err != nil {
			return err
		}
		dbContext, cancelDB := context.WithTimeout(ctx, outboxDBTimeout)
		for _, record := range batch {
			ids := make([]string, len(record.Outbox))
			for i, pending := range record.Outbox {
				ids[i] = pending.ID
			}
			if err = finishOutbox(dbContext, record, ids); err != nil {
				break
			}
		}
		cancelDB()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}
