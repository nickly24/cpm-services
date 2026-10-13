package uploads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const uploadsExchange = "cpm.uploads.v1"
const uploadsExecuteQueue = "uploads.import.execute.v1"
const uploadsRetryQueue = "uploads.import.retry.v1"
const uploadsDeadQueue = "uploads.import.dead.v1"

// ExecuteImport must check live Auth permissions for the pinned scope and use a
// stable owner idempotency key. nil means an outcome (including domain failure,
// authorization wait, or retry) has been durably saved in uploads_jobs_v1.
type ExecuteImport func(context.Context, string, Scope) error

type DeliveryBroker struct {
	DB                                            *sql.DB
	URL                                           string
	Execute                                       ExecuteImport
	AuthorizationEnabled                          bool
	ready                                         atomic.Bool
	exchange, executeQueue, retryQueue, deadQueue string
}

func NewDeliveryBroker(db *sql.DB, rabbitURL string, execute ExecuteImport) *DeliveryBroker {
	return &DeliveryBroker{DB: db, URL: rabbitURL, Execute: execute, exchange: uploadsExchange,
		executeQueue: uploadsExecuteQueue, retryQueue: uploadsRetryQueue, deadQueue: uploadsDeadQueue}
}

func (b *DeliveryBroker) Ready() bool { return b.ready.Load() }

func (b *DeliveryBroker) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, e := b.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}

type importPublisher struct {
	mu      sync.Mutex
	ch      *amqp.Channel
	acks    <-chan amqp.Confirmation
	returns <-chan amqp.Return
}

func newImportPublisher(ch *amqp.Channel) (*importPublisher, error) {
	if e := ch.Confirm(false); e != nil {
		return nil, e
	}
	return &importPublisher{ch: ch, acks: ch.NotifyPublish(make(chan amqp.Confirmation, 1)), returns: ch.NotifyReturn(make(chan amqp.Return, 1))}, nil
}

func (p *importPublisher) publish(ctx context.Context, exchange, route string, msg amqp.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq := p.ch.GetNextPublishSeqNo()
	if e := p.ch.PublishWithContext(ctx, exchange, route, true, false, msg); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.returns:
		return errors.New("uploads_unroutable_delivery")
	case ack, ok := <-p.acks:
		if !ok || !ack.Ack || ack.DeliveryTag != seq {
			return errors.New("uploads_unconfirmed_delivery")
		}
		// Rabbit sends mandatory returns before their publish confirmation.
		select {
		case <-p.returns:
			return errors.New("uploads_unroutable_delivery")
		default:
			return nil
		}
	}
}

func (b *DeliveryBroker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		e := b.session(ctx)
		b.ready.Store(false)
		if ctx.Err() != nil {
			return
		}
		if e != nil {
			slog.Warn("uploads broker reconnecting", "error_code", "broker_session_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (b *DeliveryBroker) declare(ch *amqp.Channel) error {
	if e := ch.ExchangeDeclare(b.exchange, "direct", true, false, false, false, nil); e != nil {
		return e
	}
	for _, q := range []struct {
		name, route string
		args        amqp.Table
	}{
		{b.executeQueue, importExecuteRoute, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1), "x-single-active-consumer": true, "x-dead-letter-exchange": b.exchange, "x-dead-letter-routing-key": importDeadRoute, "x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish"}},
		{b.retryQueue, importRetryRoute, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1), "x-message-ttl": int32(5000), "x-dead-letter-exchange": b.exchange, "x-dead-letter-routing-key": importExecuteRoute, "x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish"}},
		{b.deadQueue, importDeadRoute, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1)}},
	} {
		if _, e := ch.QueueDeclare(q.name, true, false, false, false, q.args); e != nil {
			return e
		}
		if e := ch.QueueBind(q.name, q.route, b.exchange, false, nil); e != nil {
			return e
		}
	}
	return nil
}

func (b *DeliveryBroker) session(ctx context.Context) error {
	if b.Execute == nil {
		return errors.New("uploads_executor_required")
	}
	conn, e := amqp.DialConfig(b.URL, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if e != nil {
		return e
	}
	defer conn.Close()
	consumer, e := conn.Channel()
	if e != nil {
		return e
	}
	defer consumer.Close()
	if e = b.declare(consumer); e != nil {
		return e
	}
	if e = consumer.Qos(1, 0, false); e != nil {
		return e
	}
	pc, e := conn.Channel()
	if e != nil {
		return e
	}
	defer pc.Close()
	pub, e := newImportPublisher(pc)
	if e != nil {
		return e
	}
	deliveries, e := consumer.Consume(b.executeQueue, "", false, false, false, false, nil)
	if e != nil {
		return e
	}
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsCh := make(chan error, 1)
	// The publisher keeps draining the outbox even during a long owner commit.
	// Only this goroutine uses its channel, except sanitized rejection publishes
	// which serialize through importPublisher.mu.
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		n := 0
		for sessionCtx.Err() == nil {
			c, end := context.WithTimeout(sessionCtx, 20*time.Second)
			e := b.dispatch(c, pub)
			if e == nil && n%10 == 0 {
				e = b.RecoverDelivery(c)
			}
			end()
			if e != nil {
				select {
				case errorsCh <- e:
				default:
				}
				return
			}
			n++
			select {
			case <-sessionCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	b.ready.Store(true)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return errors.New("uploads_broker_disconnected")
		case e := <-errorsCh:
			return e
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("uploads_consumer_closed")
			}
			if e = b.handle(sessionCtx, pub, d); e != nil {
				return e
			}
		}
	}
}

func (b *DeliveryBroker) dispatch(ctx context.Context, pub *importPublisher) error {
	for n := 0; n < 20; n++ {
		var id, route string
		var body []byte
		token := uuid()
		e := b.transact(ctx, func(tx *sql.Tx) error {
			e := tx.QueryRowContext(ctx, `SELECT id,route,body FROM uploads_outbox_v1 WHERE published_at IS NULL
			 AND available_at<=UTC_TIMESTAMP(6) AND (lease_until IS NULL OR lease_until<UTC_TIMESTAMP(6))
			 ORDER BY available_at,created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &route, &body)
			if errors.Is(e, sql.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "UPDATE uploads_outbox_v1 SET lease_token=?,lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND),attempts=attempts+1 WHERE id=?", token, id)
			return e
		})
		if e != nil {
			return e
		}
		if id == "" {
			return nil
		}
		if route != importExecuteRoute && route != importRetryRoute && route != importDeadRoute {
			return errInvalidDelivery
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		e = pub.publish(c, b.exchange, route, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent,
			Body: body, MessageId: id, Timestamp: time.Now().UTC()})
		cancel()
		if e != nil {
			// A timed-out confirm has an unknown outcome: leave the original ID,
			// reconnect the channel, and publish the same envelope again.
			_, _ = b.DB.ExecContext(ctx, "UPDATE uploads_outbox_v1 SET lease_token=NULL,lease_until=NULL,available_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 5 SECOND) WHERE id=? AND lease_token=? AND published_at IS NULL", id, token)
			return e
		}
		_, e = b.DB.ExecContext(ctx, "UPDATE uploads_outbox_v1 SET published_at=UTC_TIMESTAMP(6),lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=? AND published_at IS NULL", id, token)
		if e != nil {
			return e
		}
	}
	return nil
}

func (b *DeliveryBroker) handle(ctx context.Context, pub *importPublisher, d amqp.Delivery) error {
	ev, e := decodeImport(d.Body)
	if e == nil && (d.MessageId != ev.MessageID || d.ContentType != "application/json") {
		e = errInvalidDelivery
	}
	if e != nil {
		return b.reject(ctx, pub, d)
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	claim, e := b.claim(c, ev)
	cancel()
	if errors.Is(e, errInvalidDelivery) {
		return b.reject(ctx, pub, d)
	}
	if errors.Is(e, errDeliveryBusy) {
		if e = b.deferDelivery(ctx, ev, "delivery_busy"); e != nil {
			return e
		}
		return d.Ack(false)
	}
	if e != nil {
		return e
	}
	if !claim.Run {
		return d.Ack(false)
	}
	c, cancel = context.WithTimeout(ctx, 120*time.Second)
	e = b.Execute(c, ev.Payload.JobID, ev.Payload.Scope)
	cancel()
	// Even during graceful shutdown, try to record durable retry/outcome before
	// ACK. On failure no ACK is sent and closing the channel returns the delivery.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer finishCancel()
	if e == nil {
		e = b.finish(finishCtx, ev, claim.Token)
	}
	if e != nil {
		if e = b.deferDelivery(finishCtx, ev, "executor_outcome_unknown"); e != nil {
			return e
		}
	}
	return d.Ack(false)
}

func (b *DeliveryBroker) reject(ctx context.Context, pub *importPublisher, d amqp.Delivery) error {
	// Invalid input is never copied into logs or DLQ. The digest identifies the
	// poisoned transport message without persisting personal data or credentials.
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "message_type": "uploads.import.rejected.v1",
		"producer": "uploads", "reason": "invalid_import_event", "body_hash": digest(d.Body), "occurred_at": time.Now().UTC()})
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if e := pub.publish(c, b.exchange, importDeadRoute, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: raw, MessageId: uuid(), Timestamp: time.Now().UTC()}); e != nil {
		return e
	}
	return d.Ack(false)
}
