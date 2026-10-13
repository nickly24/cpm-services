package calculate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type receiptRepository struct {
	memoryRepository
	stateMu   sync.Mutex
	active    int
	maximum   int
	started   chan struct{}
	release   chan struct{}
	failureID string
}

func (s *receiptRepository) Receive(ctx context.Context, m Message) (bool, error) {
	s.stateMu.Lock()
	s.active++
	if s.active > s.maximum {
		s.maximum = s.active
	}
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		s.active--
		s.stateMu.Unlock()
	}()
	s.started <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if m.ID == s.failureID {
		return false, errors.New("storage unavailable")
	}
	return s.memoryRepository.Receive(ctx, m)
}

type receiptAcker struct {
	t      *testing.T
	store  *receiptRepository
	ids    map[uint64]string
	mu     sync.Mutex
	acked  map[uint64]bool
	nacked map[uint64]bool
	acks   chan uint64
}

func (a *receiptAcker) Ack(tag uint64, multiple bool) error {
	a.store.mu.Lock()
	_, persisted := a.store.items[a.ids[tag]]
	a.store.mu.Unlock()
	if !persisted || multiple {
		a.t.Error("receipt acknowledged without durable persistence or with multiple=true")
	}
	a.mu.Lock()
	if a.acked[tag] {
		a.t.Error("delivery acknowledged twice")
	}
	a.acked[tag] = true
	a.mu.Unlock()
	a.acks <- tag
	return nil
}

func (a *receiptAcker) Nack(tag uint64, multiple, requeue bool) error {
	if multiple || !requeue {
		a.t.Error("failed storage must requeue only its delivery")
	}
	a.mu.Lock()
	a.nacked[tag] = requeue
	a.mu.Unlock()
	return nil
}

func (a *receiptAcker) Reject(uint64, bool) error { return errors.New("unexpected reject") }

func receiptDeliveries(t *testing.T, store *receiptRepository, count int) (chan amqp.Delivery, *receiptAcker) {
	t.Helper()
	acker := &receiptAcker{t: t, store: store, ids: map[uint64]string{}, acked: map[uint64]bool{}, nacked: map[uint64]bool{}, acks: make(chan uint64, count)}
	deliveries := make(chan amqp.Delivery, count)
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("bd095746-6c38-4b77-bc40-%012x", i)
		acker.ids[uint64(i)] = id
		body := bytes.Replace(validBody(), []byte("bd095746-6c38-4b77-bc40-c6d0e62d1b2d"), []byte(id), 1)
		deliveries <- amqp.Delivery{Acknowledger: acker, DeliveryTag: uint64(i), Body: body, RoutingKey: "tests.grade.requested.v1"}
	}
	return deliveries, acker
}

func TestReceiptPoolBoundsWritesAndOnlyAcknowledgesPersistedDeliveries(t *testing.T) {
	const count, slots = 20, 4
	store := &receiptRepository{started: make(chan struct{}, count), release: make(chan struct{})}
	deliveries, acker := receiptDeliveries(t, store, count)
	consumer := &Consumer{Store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.consumeReceipts(ctx, deliveries, slots) }()
	for range slots {
		select {
		case <-store.started:
		case <-time.After(5 * time.Second):
			close(store.release)
			t.Fatal("receipts were not persisted concurrently")
		}
	}
	select {
	case <-store.started:
		close(store.release)
		t.Fatal("receipt pool exceeded its bound")
	case <-time.After(30 * time.Millisecond):
	}
	acker.mu.Lock()
	if len(acker.acked) != 0 {
		t.Error("blocked receipts were acknowledged before persistence")
	}
	acker.mu.Unlock()
	close(store.release)
	for range count {
		select {
		case <-acker.acks:
		case <-time.After(5 * time.Second):
			t.Fatal("durable receipt was not acknowledged")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	store.stateMu.Lock()
	defer store.stateMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.maximum != slots || store.active != 0 || len(store.items) != count {
		t.Fatalf("receipt pool left unfinished writes: max=%d active=%d stored=%d", store.maximum, store.active, len(store.items))
	}
}

func TestReceiptFailureStopsAndWaitsForOtherWritesBeforeReconnect(t *testing.T) {
	const count, slots = 20, 3
	store := &receiptRepository{started: make(chan struct{}, count), release: make(chan struct{}), failureID: "bd095746-6c38-4b77-bc40-000000000001"}
	deliveries, acker := receiptDeliveries(t, store, count)
	consumer := &Consumer{Store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.consumeReceipts(ctx, deliveries, slots) }()
	for range slots {
		select {
		case <-store.started:
		case <-time.After(5 * time.Second):
			close(store.release)
			t.Fatal("receipts never started")
		}
	}
	close(store.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("storage failure did not request session reconnect")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receipt failure left workers running")
	}
	store.stateMu.Lock()
	defer store.stateMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	acker.mu.Lock()
	defer acker.mu.Unlock()
	if store.active != 0 || store.maximum > slots || acker.acked[1] || !acker.nacked[1] {
		t.Fatal("failed write acknowledged or background writes still running")
	}
	if _, exists := store.items[store.failureID]; exists {
		t.Fatal("failed receipt created an inbox record")
	}
}

type blockedSocketAck struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (a *blockedSocketAck) Ack(uint64, bool) error {
	a.once.Do(func() { close(a.started) })
	<-a.closed
	return errors.New("socket closed before ACK response")
}

func (a *blockedSocketAck) Nack(uint64, bool, bool) error { return nil }
func (a *blockedSocketAck) Reject(uint64, bool) error     { return nil }

type failAfterBlockedAck struct {
	memoryRepository
	ackStarted <-chan struct{}
	failureID  string
}

func (s *failAfterBlockedAck) Receive(ctx context.Context, m Message) (bool, error) {
	if m.ID == s.failureID {
		select {
		case <-s.ackStarted:
			return false, errors.New("storage unavailable")
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.memoryRepository.Receive(ctx, m)
}

func TestReceiptShutdownClosesTransportBeforeJoiningBlockedAckAndReplayIsIdempotent(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("storage_failure_%t", failure), func(t *testing.T) {
			ack := &blockedSocketAck{started: make(chan struct{}), closed: make(chan struct{})}
			store := &failAfterBlockedAck{ackStarted: ack.started, failureID: "bd095746-6c38-4b77-bc40-000000000002"}
			consumer := &Consumer{Store: store}
			deliveries := make(chan amqp.Delivery, 2)
			deliveries <- amqp.Delivery{Acknowledger: ack, DeliveryTag: 1, Body: validBody(), RoutingKey: "tests.grade.requested.v1"}
			if failure {
				body := bytes.Replace(validBody(), []byte("bd095746-6c38-4b77-bc40-c6d0e62d1b2d"), []byte(store.failureID), 1)
				deliveries <- amqp.Delivery{Acknowledger: ack, DeliveryTag: 2, Body: body, RoutingKey: "tests.grade.requested.v1"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var closeSocket sync.Once
			stopTransport := func() { closeSocket.Do(func() { close(ack.closed) }) }
			defer stopTransport()
			done := make(chan error, 1)
			go func() { done <- consumer.consumeReceiptsWithStop(ctx, deliveries, 2, stopTransport) }()
			select {
			case <-ack.started:
			case <-time.After(5 * time.Second):
				t.Fatal("receipt never reached its blocked ACK")
			}
			if !failure {
				cancel()
			}
			select {
			case err := <-done:
				if failure && err == nil {
					t.Fatal("receipt storage failure did not trigger reconnect")
				}
			case <-time.After(time.Second):
				t.Fatal("transport stayed open while joining a blocked ACK")
			}
			// Socket loss leaves a durable receipt. Broker redelivery compares
			// the same hash, confirms it and does not add another stored job.
			confirm := &recordedAck{t: t, store: &store.memoryRepository}
			if err := consumer.Handle(context.Background(), validBody(), "tests.grade.requested.v1", confirm); err != nil || !confirm.acked || len(store.items) != 1 {
				t.Fatal("redelivery after lost ACK was not idempotent")
			}
		})
	}
}
