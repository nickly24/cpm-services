package exams

import (
	"context"
	"database/sql"
	amqp "github.com/rabbitmq/amqp091-go"
	"net"
	"time"
)

// RunMaintenance delivers the transactional outbox at least once. A missing
// binding, nack or connection loss leaves the event pending for recovery.
// Consumers deduplicate by message_id and order by division source_revision.
func RunMaintenance(ctx context.Context, db *sql.DB, rabbitURL string) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var conn *amqp.Connection
	var channel *amqp.Channel
	var returns chan amqp.Return
	closeBroker := func() {
		if channel != nil {
			_ = channel.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
		channel = nil
		conn = nil
	}
	defer closeBroker()
	lastCleanup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if time.Since(lastCleanup) > time.Minute {
			cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = CleanupRetention(cleanup, db, time.Now().UTC(), 1000)
			cancel()
			lastCleanup = time.Now()
		}
		if rabbitURL == "" {
			continue
		}
		if channel == nil || channel.IsClosed() {
			closeBroker()
			var err error
			conn, err = amqp.DialConfig(rabbitURL, amqp.Config{Heartbeat: 10 * time.Second, Dial: func(network, address string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
			}})
			if err != nil {
				continue
			}
			channel, err = conn.Channel()
			if err != nil {
				closeBroker()
				continue
			}
			if channel.ExchangeDeclare("cpm.events", "topic", true, false, false, false, nil) != nil || channel.Confirm(false) != nil {
				closeBroker()
				continue
			}
			returns = channel.NotifyReturn(make(chan amqp.Return, 1))
		}
		// Bounded batches prevent expiry cleanup or shutdown starvation.
		for i := 0; i < 100; i++ {
			worked, err := deliverOutbox(ctx, db, channel, returns)
			if err != nil {
				closeBroker()
				break
			}
			if !worked {
				break
			}
		}
	}
}

func deliverOutbox(ctx context.Context, db *sql.DB, ch *amqp.Channel, returns <-chan amqp.Return) (bool, error) {
	return deliverOutboxTo(ctx, db, ch, returns, "cpm.events", "")
}

func deliverOutboxTo(ctx context.Context, db *sql.DB, ch *amqp.Channel, returns <-chan amqp.Return, exchange, targetID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id string
	var raw []byte
	if targetID == "" {
		err = tx.QueryRowContext(ctx, "SELECT id,envelope FROM exams_outbox_v1 WHERE published_at IS NULL ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED").Scan(&id, &raw)
	} else {
		err = tx.QueryRowContext(ctx, "SELECT id,envelope FROM exams_outbox_v1 WHERE id=? AND published_at IS NULL FOR UPDATE SKIP LOCKED", targetID).Scan(&id, &raw)
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	confirmed, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, "exams.source.changed", true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: id, Timestamp: time.Now().UTC(), Body: raw})
	if err != nil {
		return false, err
	}
	ack, err := confirmed.WaitContext(ctx)
	if err != nil {
		return false, err
	}
	select {
	case <-returns:
		return false, nil
	default:
	}
	if !ack {
		return false, context.DeadlineExceeded
	}
	_, err = tx.ExecContext(ctx, "UPDATE exams_outbox_v1 SET published_at=UTC_TIMESTAMP(6) WHERE id=? AND published_at IS NULL", id)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
