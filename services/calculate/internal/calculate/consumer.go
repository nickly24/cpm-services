package calculate

import (
	"context"
	"errors"
	"log/slog"
	random "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	ExchangeName = "cpm.events"
	QueueName    = "calculate.events.v1"
	DeadExchange = "cpm.events.dead"
	DeadQueue    = "calculate.events.dead.v1"
	DeadRoute    = "calculate.invalid.v1"
)

type Acknowledger interface {
	Ack(bool) error
	Nack(bool, bool) error
}

type Consumer struct {
	URL            string
	Store          Repository
	Connected      atomic.Bool
	ReceiptWorkers int
	// Defaults preserve the tests consumer. Ratings has its own parser,
	// topology, connection and inbox, and cannot occupy the grading worker.
	Queue    string
	Parser   func([]byte, string, time.Time) (Message, error)
	Topology func(*amqp.Channel) error
}

func (c *Consumer) Handle(ctx context.Context, body []byte, routingKey string, ack Acknowledger) error {
	parse := c.Parser
	if parse == nil {
		parse = ParseMessage
	}
	m, err := parse(body, routingKey, time.Now())
	if err != nil {
		slog.Warn("calculate rejected invalid envelope")
		return ack.Nack(false, false)
	}
	_, err = c.Store.Receive(ctx, m)
	if errors.Is(err, ErrMessageConflict) {
		slog.Warn("calculate rejected conflicting message ID", "message_id", m.ID)
		return ack.Nack(false, false)
	}
	if err != nil {
		_ = ack.Nack(false, true)
		return err
	}
	return ack.Ack(false)
}

// The connection is recreated after either broker or persistence failure. The
// close returns unacknowledged deliveries to the quorum queue before retrying.
func (c *Consumer) Run(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		active, err := c.session(ctx)
		c.Connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		if active && time.Since(started) >= 30*time.Second {
			delay = time.Second
		}
		if err != nil {
			slog.Warn("calculate consumer disconnected; reconnect scheduled")
		}
		timer := time.NewTimer(delay/2 + time.Duration(random.Int64N(int64(delay/2)+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func DeclareTopology(ch *amqp.Channel) error {
	for _, exchange := range []string{ExchangeName, DeadExchange} {
		if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare(DeadQueue, true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1)}); err != nil {
		return err
	}
	if err := ch.QueueBind(DeadQueue, DeadRoute, DeadExchange, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(QueueName, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum", "x-delivery-limit": int32(-1),
		"x-dead-letter-exchange": DeadExchange, "x-dead-letter-routing-key": DeadRoute,
		"x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish",
	}); err != nil {
		return err
	}
	for _, key := range []string{"tests.grade.requested.v1", "tests.content.changed.v1"} {
		if err := ch.QueueBind(QueueName, key, ExchangeName, false, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *Consumer) session(ctx context.Context) (bool, error) {
	conn, err := amqp.DialConfig(c.URL, amqp.Config{Heartbeat: 10 * time.Second, Locale: "en_US", Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return false, err
	}
	// ACK writes do not accept a context. Closing with a socket deadline on
	// cancellation unblocks an ACK held in the channel/connection write mutex.
	// Plain ch.Close/conn.Close could themselves wait on that same blocked write.
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	connectionDone := make(chan struct{})
	go func() {
		defer close(connectionDone)
		<-connectionCtx.Done()
		_ = conn.CloseDeadline(time.Now().Add(2 * time.Second))
	}()
	defer func() {
		cancelConnection()
		<-connectionDone
	}()
	ch, err := conn.Channel()
	if err != nil {
		return false, err
	}
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	topology := c.Topology
	if topology == nil {
		topology = DeclareTopology
	}
	if err := topology(ch); err != nil {
		return false, err
	}
	workers := c.ReceiptWorkers
	if workers == 0 {
		workers = 1
	}
	if workers < 1 || workers > 8 {
		return false, errors.New("invalid receipt worker count")
	}
	if err := ch.Qos(workers*2, 0, false); err != nil {
		return false, err
	}
	queue := c.Queue
	if queue == "" {
		queue = QueueName
	}
	receiptCtx, cancelReceipts := context.WithCancel(ctx)
	defer cancelReceipts()
	deliveries, err := ch.ConsumeWithContext(receiptCtx, queue, "", false, false, false, false, nil)
	if err != nil {
		return false, err
	}
	c.Connected.Store(true)
	slog.Info("calculate consumer connected", "queue", queue, "receipt_workers", workers)
	receiptsDone := make(chan error, 1)
	go func() { receiptsDone <- c.consumeReceiptsWithStop(receiptCtx, deliveries, workers, cancelConnection) }()
	select {
	case <-ctx.Done():
		cancelReceipts()
		<-receiptsDone
		return true, nil
	case err := <-closed:
		cancelReceipts()
		<-receiptsDone
		if err == nil {
			return true, errors.New("consumer channel closed")
		}
		return true, err
	case err := <-receiptsDone:
		return true, err
	}
}

// A fixed pool bounds both goroutines and uncommitted deliveries. Every worker
// retains persist-before-ACK; ACK/NACK always targets one delivery. On failure
// transport cancellation unblocks ACK writes, and all workers finish before
// reconnect. Unacknowledged deliveries, including a lost ACK, are redelivered.
func (c *Consumer) consumeReceipts(parent context.Context, deliveries <-chan amqp.Delivery, count int) error {
	return c.consumeReceiptsWithStop(parent, deliveries, count, nil)
}

func (c *Consumer) consumeReceiptsWithStop(parent context.Context, deliveries <-chan amqp.Delivery, count int, stopTransport func()) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	failures := make(chan error, 1)
	var workers sync.WaitGroup
	for range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				select {
				case <-ctx.Done():
					return
				case d, ok := <-deliveries:
					if !ok {
						select {
						case failures <- errors.New("consumer stopped"):
						default:
						}
						cancel()
						return
					}
					writeCtx, stopWrite := context.WithTimeout(ctx, 10*time.Second)
					stopDeadlineWatch := context.AfterFunc(writeCtx, func() {
						if stopTransport != nil {
							stopTransport()
						}
					})
					err := c.Handle(writeCtx, d.Body, d.RoutingKey, d)
					stopDeadlineWatch()
					stopWrite()
					if err != nil {
						select {
						case failures <- err:
						default:
						}
						cancel()
						return
					}
				}
			}
		}()
	}
	var err error
	select {
	case <-parent.Done():
	case err = <-failures:
	}
	cancel()
	if stopTransport != nil {
		stopTransport()
	}
	workers.Wait()
	return err
}
