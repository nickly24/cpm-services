package ratings

import (
	"context"
	"database/sql"
	"errors"
	amqp "github.com/rabbitmq/amqp091-go"
	"log/slog"
	"time"
)

const ratingRoute = "ratings.calculate.requested.v1"

type Publisher struct {
	Store *Store
	URL   string
	Log   *slog.Logger
}
type delivery struct {
	ID       string
	Body     []byte
	Token    string
	Attempts int
}

func (s *Store) claimDelivery(ctx context.Context) (delivery, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return delivery{}, e
	}
	defer tx.Rollback()
	var d delivery
	now := time.Now().UTC()
	e = tx.QueryRowContext(ctx, "SELECT o.message_id,o.body,o.attempts FROM ratings_outbox_v1 o JOIN ratings_jobs_v1 j ON j.id=o.job_id WHERE o.delivered_at IS NULL AND o.next_attempt_at<=? AND (o.lease_until IS NULL OR o.lease_until<?) AND j.status IN ('queued','running','validating') ORDER BY o.created_at,o.message_id LIMIT 1 FOR UPDATE SKIP LOCKED", now, now).Scan(&d.ID, &d.Body, &d.Attempts)
	if e != nil {
		return d, e
	}
	d.Token = uuid()
	_, e = tx.ExecContext(ctx, "UPDATE ratings_outbox_v1 SET lease_token=?,lease_until=?,attempts=attempts+1 WHERE message_id=?", d.Token, now.Add(30*time.Second), d.ID)
	if e != nil {
		return d, e
	}
	return d, tx.Commit()
}
func (p *Publisher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if e := p.session(ctx); e != nil && ctx.Err() == nil {
			p.Log.Warn("ratings RabbitMQ publisher disconnected")
		}
		t := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
func (p *Publisher) session(ctx context.Context) error {
	conn, e := amqp.DialConfig(p.URL, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if e != nil {
		return e
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
	ch, e := conn.Channel()
	if e != nil {
		return e
	}
	defer ch.Close()
	if e = declareTopology(ch); e != nil {
		return e
	}
	if e = ch.Confirm(false); e != nil {
		return e
	}
	returns := ch.NotifyReturn(make(chan amqp.Return, 1))
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		d, e := p.Store.claimDelivery(ctx)
		if errors.Is(e, sql.ErrNoRows) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-closed:
				if err == nil {
					return errors.New("broker closed")
				}
				return err
			case <-tick.C:
				continue
			}
		}
		if e != nil {
			return e
		}
		publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		confirmation, err := ch.PublishWithDeferredConfirmWithContext(publishCtx, "cpm.events", ratingRoute, true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: d.ID, Type: "ratings.calculate.requested", Timestamp: time.Now().UTC(), Body: d.Body})
		if err == nil {
			var ok bool
			ok, err = confirmation.WaitContext(publishCtx)
			if err == nil && !ok {
				err = errors.New("broker nack")
			}
		}
		if err == nil {
			select {
			case <-returns:
				err = errors.New("broker returned unroutable message")
			default:
			}
		}
		cancel()
		if err == nil {
			_, e = p.Store.DB.ExecContext(ctx, "UPDATE ratings_outbox_v1 SET delivered_at=?,next_attempt_at=?,lease_token=NULL,lease_until=NULL WHERE message_id=? AND lease_token=?", time.Now().UTC(), time.Now().UTC().Add(60*time.Second), d.ID, d.Token)
		} else {
			_, e = p.Store.DB.ExecContext(ctx, "UPDATE ratings_outbox_v1 SET next_attempt_at=?,lease_token=NULL,lease_until=NULL WHERE message_id=? AND lease_token=?", time.Now().UTC().Add(time.Duration(1<<min(d.Attempts, 5))*time.Second), d.ID, d.Token)
		}
		if e != nil {
			return e
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}
func declareTopology(ch *amqp.Channel) error {
	for _, name := range []string{"cpm.events", "cpm.events.dead"} {
		if e := ch.ExchangeDeclare(name, "topic", true, false, false, false, nil); e != nil {
			return e
		}
	}
	if _, e := ch.QueueDeclare("calculate.ratings.dead.v1", true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1)}); e != nil {
		return e
	}
	if e := ch.QueueBind("calculate.ratings.dead.v1", "calculate.ratings.invalid.v1", "cpm.events.dead", false, nil); e != nil {
		return e
	}
	if _, e := ch.QueueDeclare("calculate.ratings.v1", true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1), "x-dead-letter-exchange": "cpm.events.dead", "x-dead-letter-routing-key": "calculate.ratings.invalid.v1", "x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish"}); e != nil {
		return e
	}
	return ch.QueueBind("calculate.ratings.v1", ratingRoute, "cpm.events", false, nil)
}
