package filework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

var retryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}

const leaseWaitQueue = "filework.pdf.lease-wait.v1"

// One serialized publisher uses confirms and mandatory returns. Rabbit sends
// basic.return before basic.ack; both notifications are buffered before ack is
// observed, so a confirmed-but-unroutable publish cannot acknowledge its input.
type Publisher struct {
	channel  *amqp.Channel
	confirms <-chan amqp.Confirmation
	returns  <-chan amqp.Return
}

func newPublisher(ch *amqp.Channel) (*Publisher, error) {
	if err := ch.Confirm(false); err != nil {
		return nil, err
	}
	return &Publisher{channel: ch, confirms: ch.NotifyPublish(make(chan amqp.Confirmation, 1)), returns: ch.NotifyReturn(make(chan amqp.Return, 1))}, nil
}
func (p *Publisher) Publish(ctx context.Context, exchange, key string, message amqp.Publishing) error {
	if err := p.channel.PublishWithContext(ctx, exchange, key, true, false, message); err != nil {
		return errors.New("publish_failed")
	}
	for {
		select {
		case _, ok := <-p.returns:
			if !ok {
				return errors.New("publisher_closed")
			}
			return errors.New("message_unroutable")
		case confirmation, ok := <-p.confirms:
			if !ok || !confirmation.Ack {
				return errors.New("publish_not_confirmed")
			}
			select {
			case <-p.returns:
				return errors.New("message_unroutable")
			default:
				return nil
			}
		case <-ctx.Done():
			return errors.New("publish_confirmation_timeout")
		}
	}
}

type BrokerWorker struct {
	RabbitURL  string
	Engine     Engine
	Ready      atomic.Bool
	Processing atomic.Bool
	Log        *slog.Logger
}

func (w *BrokerWorker) logger() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
func (w *BrokerWorker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := w.session(ctx)
		w.Ready.Store(false)
		if ctx.Err() != nil {
			return
		}
		w.logger().Warn("worker broker session interrupted", "error_code", safeBrokerError(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func safeBrokerError(err error) string {
	if err == nil {
		return "broker_disconnected"
	}
	return "broker_session_failed"
}

func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(Exchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(CommandQueue, true, false, false, false, amqp.Table{"x-single-active-consumer": true}); err != nil {
		return err
	}
	if err := ch.QueueBind(CommandQueue, CommandRoute, Exchange, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(ResultQueue, true, false, false, false, nil); err != nil {
		return err
	}
	if err := ch.QueueBind(ResultQueue, ResultRoute, Exchange, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(DeadQueue, true, false, false, false, nil); err != nil {
		return err
	}
	for i, delay := range retryDelays {
		args := amqp.Table{"x-queue-type": "quorum", "x-message-ttl": int32(delay.Milliseconds()), "x-dead-letter-exchange": Exchange, "x-dead-letter-routing-key": CommandRoute, "x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish"}
		if _, err := ch.QueueDeclare(retryQueue(i), true, false, false, false, args); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare(leaseWaitQueue, true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-message-ttl": int32(310000), "x-dead-letter-exchange": Exchange, "x-dead-letter-routing-key": CommandRoute, "x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish"}); err != nil {
		return err
	}
	return nil
}
func retryQueue(index int) string { return fmt.Sprintf("filework.pdf.retry.%d.v1", index+1) }
func (w *BrokerWorker) session(ctx context.Context) error {
	connection, err := amqp.DialConfig(w.RabbitURL, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return errors.New("broker_connect_failed")
	}
	defer connection.Close()
	consumer, err := connection.Channel()
	if err != nil {
		return err
	}
	defer consumer.Close()
	if err = DeclareTopology(consumer); err != nil {
		return err
	}
	if err = consumer.Qos(1, 0, false); err != nil {
		return err
	}
	pubChannel, err := connection.Channel()
	if err != nil {
		return err
	}
	defer pubChannel.Close()
	publisher, err := newPublisher(pubChannel)
	if err != nil {
		return err
	}
	deliveries, err := consumer.Consume(CommandQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	closed := connection.NotifyClose(make(chan *amqp.Error, 1))
	w.Ready.Store(true)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return errors.New("broker_disconnected")
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("consumer_closed")
			}
			if err = w.deliver(ctx, publisher, delivery); err != nil {
				return err
			}
		}
	}
}

func commandPublishing(d amqp.Delivery, attempt int, code string) amqp.Publishing {
	headers := amqp.Table{"filework-attempt": int32(attempt)}
	if code != "" {
		headers["filework-error-code"] = code
	}
	return amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: d.MessageId, CorrelationId: d.CorrelationId, Timestamp: time.Now().UTC(), Headers: headers, Body: d.Body}
}
func deliveryAttempt(d amqp.Delivery) int {
	switch n := d.Headers["filework-attempt"].(type) {
	case int32:
		if n >= 0 && n <= 3 {
			return int(n)
		}
	case int64:
		if n >= 0 && n <= 3 {
			return int(n)
		}
	case int:
		if n >= 0 && n <= 3 {
			return n
		}
	}
	return 0
}

type MessagePublisher interface {
	Publish(context.Context, string, string, amqp.Publishing) error
}

func (w *BrokerWorker) deliver(ctx context.Context, p MessagePublisher, d amqp.Delivery) error {
	event, err := DecodeCommand(d.Body)
	if err != nil {
		publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err = p.Publish(publishCtx, "", DeadQueue, commandPublishing(d, deliveryAttempt(d), "invalid_command")); err != nil {
			return err
		}
		return d.Ack(false)
	}
	result, run, err := w.process(ctx, event.Payload)
	if err == nil && !run {
		return d.Ack(false)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code, permanent := permanentCode(err)
	attempt := deliveryAttempt(d)
	publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err != nil && err.Error() == "job_busy" {
		if err = p.Publish(publishCtx, "", leaseWaitQueue, commandPublishing(d, attempt, "job_busy")); err != nil {
			return err
		}
		return d.Ack(false)
	}
	if err != nil && !permanent && attempt < len(retryDelays) {
		route := retryQueue(attempt)
		nextAttempt := attempt + 1
		if result.ClaimToken != "" {
			if releaseErr := w.Engine.Claim.Release(publishCtx, event.Payload, result.ClaimToken); releaseErr != nil {
				route = leaseWaitQueue
				nextAttempt = attempt
			}
		}
		if err = p.Publish(publishCtx, "", route, commandPublishing(d, nextAttempt, "retryable_failure")); err != nil {
			return err
		}
		return d.Ack(false)
	}
	if err != nil {
		if !permanent {
			code = "processing_failed"
		}
		result.ErrorCode = code
	}
	processingFailed := err != nil && !permanent
	if result.ClaimToken == "" {
		if err = p.Publish(publishCtx, "", DeadQueue, commandPublishing(d, attempt, "claim_unavailable")); err != nil {
			return err
		}
		return d.Ack(false)
	}
	body, marshalErr := json.Marshal(ResultEnvelope(event, result))
	if marshalErr != nil {
		return marshalErr
	}
	if err = p.Publish(publishCtx, Exchange, ResultRoute, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: resultMessageID(result), CorrelationId: event.CorrelationID, Timestamp: time.Now().UTC(), Body: body}); err != nil {
		return err
	}
	if processingFailed {
		if err = p.Publish(publishCtx, "", DeadQueue, commandPublishing(d, attempt, code)); err != nil {
			return err
		}
	}
	w.logger().Info("PDF processing finished", "job_id", event.Payload.JobID, "generation", event.Payload.Generation, "error_code", result.ErrorCode, "size_bytes", result.SizeBytes, "page_count", result.PageCount)
	return d.Ack(false)
}

func (w *BrokerWorker) process(ctx context.Context, command Command) (Result, bool, error) {
	w.Processing.Store(true)
	defer w.Processing.Store(false)
	return w.Engine.Process(ctx, command)
}
