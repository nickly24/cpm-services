package filework

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func realBroker(t *testing.T) *amqp.Connection {
	t.Helper()
	value := os.Getenv("RABBITMQ_TEST_URL")
	if value == "" {
		t.Skip("isolated RabbitMQ fixture not configured")
	}
	connection, err := amqp.Dial(value)
	if err != nil {
		t.Fatal("cannot connect isolated RabbitMQ fixture")
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}

func TestRealBrokerConfirmsMandatoryReturnsAndPersistence(t *testing.T) {
	connection := realBroker(t)
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	queue, err := channel.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := newPublisher(channel)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = publisher.Publish(ctx, "", queue.Name, amqp.Publishing{DeliveryMode: amqp.Persistent, ContentType: "application/json", Body: []byte(`{"durable":"test"}`)}); err != nil {
		t.Fatal(err)
	}
	delivery, ok, err := channel.Get(queue.Name, true)
	if err != nil || !ok || delivery.DeliveryMode != amqp.Persistent || string(delivery.Body) != `{"durable":"test"}` {
		t.Fatal("confirmed body/persistence missing")
	}
	if err = publisher.Publish(ctx, "", "filework-test-no-such-queue", amqp.Publishing{DeliveryMode: amqp.Persistent, Body: []byte(`{}`)}); err == nil || !strings.Contains(err.Error(), "unroutable") {
		t.Fatal("mandatory unroutable message falsely confirmed", err)
	}
}

func TestRealBrokerWorkerTopologySingleActiveConsumerAndRetry(t *testing.T) {
	connection := realBroker(t)
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err = DeclareTopology(channel); err != nil {
		t.Fatal(err)
	}
	if err = DeclareTopology(channel); err != nil {
		t.Fatal("topology is not idempotent", err)
	}
	if err = channel.Qos(1, 0, false); err != nil {
		t.Fatal(err)
	}
	second, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.Qos(1, 0, false)
	firstDeliveries, err := channel.Consume(CommandQueue, "filework-test-first", false, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondDeliveries, err := second.Consume(CommandQueue, "filework-test-second", false, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	pubChannel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer pubChannel.Close()
	publisher, err := newPublisher(pubChannel)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = publisher.Publish(ctx, Exchange, CommandRoute, amqp.Publishing{DeliveryMode: amqp.Persistent, Body: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	var first amqp.Delivery
	select {
	case first = <-firstDeliveries:
	case <-ctx.Done():
		t.Fatal("first active consumer missing")
	}
	if err = publisher.Publish(ctx, Exchange, CommandRoute, amqp.Publishing{DeliveryMode: amqp.Persistent, Body: []byte("two")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondDeliveries:
		t.Fatal("standby consumer received a concurrent PDF")
	case <-firstDeliveries:
		t.Fatal("prefetch 1 exceeded")
	case <-time.After(100 * time.Millisecond):
	}
	first.Ack(false)
	select {
	case delivery := <-firstDeliveries:
		if string(delivery.Body) != "two" {
			t.Fatal("message order changed")
		}
		delivery.Ack(false)
	case <-ctx.Done():
		t.Fatal("next command missing")
	}
	if err = publisher.Publish(ctx, "", retryQueue(0), amqp.Publishing{DeliveryMode: amqp.Persistent, Headers: amqp.Table{"filework-attempt": int32(1)}, Body: []byte("retry")}); err != nil {
		t.Fatal(err)
	}
	select {
	case delivery := <-firstDeliveries:
		if string(delivery.Body) != "retry" || deliveryAttempt(delivery) != 1 {
			t.Fatal("retry identity lost")
		}
		delivery.Ack(false)
	case <-ctx.Done():
		t.Fatal("quorum delayed retry did not return")
	}
	if err = channel.Cancel("filework-test-first", false); err != nil {
		t.Fatal(err)
	}
	if err = publisher.Publish(ctx, Exchange, CommandRoute, amqp.Publishing{DeliveryMode: amqp.Persistent, Body: []byte("takeover")}); err != nil {
		t.Fatal(err)
	}
	select {
	case delivery := <-secondDeliveries:
		if string(delivery.Body) != "takeover" {
			t.Fatal("wrong takeover message")
		}
		delivery.Ack(false)
	case <-ctx.Done():
		t.Fatal("standby consumer did not take over")
	}
}
