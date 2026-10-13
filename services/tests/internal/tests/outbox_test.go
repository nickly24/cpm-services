package tests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestEnqueuePreservesPublisherLeaseAndAtomicBusinessUpdate(t *testing.T) {
	fields := bson.M{"status": "accepted", "revision": int64(4)}
	update := bson.M{"$set": fields}
	pending := storedEvent{ID: "event", Body: []byte(`{}`), RoutingKey: "tests.grade.requested.v1"}
	enqueueOutbox(update, pending)
	if fields["status"] != "accepted" || fields["outbox_pending"] != true || !equalValue(update["$push"].(bson.M)["outbox"], pending) {
		t.Fatal("acceptance, event and pending marker must be committed together")
	}
	for _, name := range []string{"outbox", "outbox_pending", "outbox_lease_until", "outbox_lease_token"} {
		fields, err := documentFields(bson.M{"_id": bson.NewObjectID(), "revision": int64(4), name: "worker owned"})
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := fields[name]; exists {
			t.Fatalf("business CAS overwrites publisher field %s", name)
		}
	}
	if _, exists := fields["outbox_lease_until"]; exists {
		t.Fatal("enqueue stole an existing publisher lease")
	}
}

type fakeOutboxChannel struct {
	sequence uint64
	count    int
	publish  func(uint64, int, bool, amqp.Publishing) error
}

func (f *fakeOutboxChannel) GetNextPublishSeqNo() uint64 { return f.sequence + 1 }
func (f *fakeOutboxChannel) PublishWithContext(_ context.Context, exchange, key string, mandatory, immediate bool, message amqp.Publishing) error {
	if exchange != "cpm.events" || key != "tests.grade.requested.v1" || immediate {
		return errors.New("unexpected event routing")
	}
	f.sequence++
	f.count++
	return f.publish(f.sequence, f.count, mandatory, message)
}

func outboxPublishFixture(count int) []leasedOutbox {
	batch := make([]leasedOutbox, count)
	for i := range batch {
		batch[i].Outbox = []storedEvent{{ID: uuid(), Body: []byte(`{}`), RoutingKey: "tests.grade.requested.v1"}}
	}
	return batch
}

func TestPublishPipelinesWholeBatchBeforeWaitingForConfirms(t *testing.T) {
	confirms := make(chan amqp.Confirmation, outboxBatchSize)
	batch := outboxPublishFixture(outboxBatchSize)
	ch := &fakeOutboxChannel{publish: func(tag uint64, count int, mandatory bool, message amqp.Publishing) error {
		if !mandatory || message.DeliveryMode != amqp.Persistent || message.ContentType != "application/json" || message.MessageId == "" {
			t.Fatal("delivery guarantees or immutable identity removed")
		}
		// A serial publisher would wait forever after the first message.
		if count == outboxBatchSize {
			for i := tag; i > 0; i-- {
				confirms <- amqp.Confirmation{DeliveryTag: i, Ack: true}
			}
		}
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := publishOutboxBatch(ctx, ch, batch, confirms, make(chan amqp.Return, outboxBatchSize), make(chan *amqp.Error, 1)); err != nil {
		t.Fatal(err)
	}
	if ch.count != outboxBatchSize {
		t.Fatal("bounded batch was not completely sent")
	}
}

func TestPublishUncertainOutcomesNeverSucceed(t *testing.T) {
	for _, scenario := range []string{"nack", "return-and-ack", "closed", "confirm-channel-closed", "unknown-tag", "duplicate-tag", "publish-failure", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			confirms := make(chan amqp.Confirmation, outboxBatchSize)
			returns := make(chan amqp.Return, outboxBatchSize)
			closed := make(chan *amqp.Error, 1)
			ch := &fakeOutboxChannel{publish: func(tag uint64, count int, _ bool, _ amqp.Publishing) error {
				if count != 2 {
					return nil
				}
				switch scenario {
				case "nack":
					confirms <- amqp.Confirmation{DeliveryTag: tag, Ack: false}
				case "return-and-ack":
					returns <- amqp.Return{ReplyCode: 312}
					confirms <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
					confirms <- amqp.Confirmation{DeliveryTag: 2, Ack: true}
				case "closed":
					close(closed)
				case "confirm-channel-closed":
					close(confirms)
				case "unknown-tag":
					confirms <- amqp.Confirmation{DeliveryTag: 99, Ack: true}
				case "duplicate-tag":
					confirms <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
					confirms <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
				case "publish-failure":
					return errors.New("broken socket")
				}
				return nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := publishOutboxBatch(ctx, ch, outboxPublishFixture(2), confirms, returns, closed); err == nil {
				t.Fatal("failed/uncertain delivery would have deleted durable events")
			}
		})
	}
}

func TestPublishConfirmedNotificationRaceWithMandatoryReturn(t *testing.T) {
	// Exercise scheduling order repeatedly: even if the select consumes both
	// confirms first, the buffered mandatory return must still reject the batch.
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			confirms := make(chan amqp.Confirmation, 2)
			returns := make(chan amqp.Return, 2)
			ch := &fakeOutboxChannel{publish: func(tag uint64, count int, _ bool, _ amqp.Publishing) error {
				if count == 2 {
					returns <- amqp.Return{ReplyCode: 312}
					confirms <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
					confirms <- amqp.Confirmation{DeliveryTag: tag, Ack: true}
				}
				return nil
			}}
			if err := publishOutboxBatch(context.Background(), ch, outboxPublishFixture(2), confirms, returns, make(chan *amqp.Error)); err == nil {
				t.Error("mandatory return raced with confirms and was ignored")
			}
		})
	}
	wg.Wait()
}
