package homework

import (
	"context"
	"database/sql"
	"errors"
	amqp "github.com/rabbitmq/amqp091-go"
	"log/slog"
	"time"
)

const exchange = "cpm.events"
const resultQueue = "homework.pdf.results.v1"
const resultDeadQueue = "homework.pdf.results.dlq.v1"

type confirmedPublisher struct {
	ch      *amqp.Channel
	acks    <-chan amqp.Confirmation
	returns <-chan amqp.Return
}

func newPublisher(ch *amqp.Channel) (*confirmedPublisher, error) {
	if e := ch.Confirm(false); e != nil {
		return nil, e
	}
	return &confirmedPublisher{ch, ch.NotifyPublish(make(chan amqp.Confirmation, 1)), ch.NotifyReturn(make(chan amqp.Return, 1))}, nil
}
func (p *confirmedPublisher) publish(ctx context.Context, x, key string, msg amqp.Publishing) error {
	if e := p.ch.PublishWithContext(ctx, x, key, true, false, msg); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.returns:
		return errors.New("unroutable_event")
	case ack, ok := <-p.acks:
		if !ok || !ack.Ack {
			return errors.New("unconfirmed_event")
		}
		select {
		case <-p.returns:
			return errors.New("unroutable_event")
		default:
			return nil
		}
	}
}
func (s *Server) RunBroker(ctx context.Context) {
	for ctx.Err() == nil {
		e := s.brokerSession(ctx)
		s.BusReady.Store(false)
		if ctx.Err() != nil {
			return
		}
		if e != nil {
			slog.Warn("homework broker reconnecting", "error_code", "broker_session_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (s *Server) brokerSession(ctx context.Context) error {
	conn, e := amqp.DialConfig(s.Config.RabbitURL, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if e != nil {
		return e
	}
	defer conn.Close()
	consumer, e := conn.Channel()
	if e != nil {
		return e
	}
	defer consumer.Close()
	if e = consumer.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); e != nil {
		return e
	}
	for _, q := range []struct {
		name, route string
		args        amqp.Table
	}{{"filework.pdf.v1", commandRoute, amqp.Table{"x-single-active-consumer": true}}, {resultQueue, resultRoute, nil}} {
		if _, e = consumer.QueueDeclare(q.name, true, false, false, false, q.args); e != nil {
			return e
		}
		if e = consumer.QueueBind(q.name, q.route, exchange, false, nil); e != nil {
			return e
		}
	}
	if _, e = consumer.QueueDeclare(resultDeadQueue, true, false, false, false, nil); e != nil {
		return e
	}
	if e = consumer.Qos(1, 0, false); e != nil {
		return e
	}
	ch, e := conn.Channel()
	if e != nil {
		return e
	}
	defer ch.Close()
	pub, e := newPublisher(ch)
	if e != nil {
		return e
	}
	deliveries, e := consumer.Consume(resultQueue, "", false, false, false, false, nil)
	if e != nil {
		return e
	}
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	s.BusReady.Store(true)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return errors.New("broker_disconnected")
		case <-tick.C:
			if e = s.dispatchOutbox(ctx, pub); e != nil {
				return e
			}
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("consumer_closed")
			}
			p, e := decodeResult(delivery.Body)
			if e == nil {
				c, cancel := context.WithTimeout(ctx, 20*time.Second)
				e = s.applyResult(c, p)
				cancel()
			}
			if e != nil {
				if asError(e).Code >= 500 || asError(e).Code == 409 {
					delivery.Nack(false, true)
					return errors.New("result_dependency_failure")
				}
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				e = pub.publish(c, "", resultDeadQueue, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: delivery.Body, MessageId: delivery.MessageId, Headers: amqp.Table{"error_code": "invalid_result"}, Timestamp: time.Now().UTC()})
				cancel()
				if e != nil {
					return e
				}
			}
			if e = delivery.Ack(false); e != nil {
				return e
			}
		}
	}
}
func (s *Server) dispatchOutbox(ctx context.Context, pub *confirmedPublisher) error {
	for i := 0; i < 10; i++ {
		var row map[string]any
		e := s.transact(ctx, func(tx *sql.Tx) error {
			var e error
			row, e = one(ctx, tx, "SELECT * FROM hw_outbox WHERE published_at IS NULL AND available_at<=UTC_TIMESTAMP(6) AND (lease_until IS NULL OR lease_until<UTC_TIMESTAMP(6)) ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED")
			if e == missing {
				row = nil
				return nil
			}
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "UPDATE hw_outbox SET lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND),attempts=attempts+1 WHERE id=?", row["id"])
			return e
		})
		if e != nil {
			return e
		}
		if row == nil {
			return nil
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		e = pub.publish(c, exchange, str(row["event_type"]), amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: []byte(str(row["body_json"])), MessageId: str(row["id"]), CorrelationId: str(row["id"]), Timestamp: time.Now().UTC()})
		cancel()
		if e != nil {
			s.DB.ExecContext(ctx, "UPDATE hw_outbox SET lease_until=NULL,available_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 5 SECOND) WHERE id=? AND published_at IS NULL", row["id"])
			return e
		}
		if _, e = s.DB.ExecContext(ctx, "UPDATE hw_outbox SET published_at=UTC_TIMESTAMP(6),lease_until=NULL WHERE id=? AND published_at IS NULL", row["id"]); e != nil {
			return e
		}
	}
	return nil
}
