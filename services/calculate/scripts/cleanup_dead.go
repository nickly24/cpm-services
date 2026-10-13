// Run from services/calculate: go run ./scripts/cleanup_dead.go ../../dev
// Removes only rejected message IDs recorded by our local HTTP test report.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func run() error {
	if len(os.Args) != 2 {
		return errors.New("provide the local dev directory")
	}
	base := os.Args[1]
	data, err := os.ReadFile(filepath.Join(base, "local/calculate-http-report.json"))
	if err != nil {
		return errors.New("local HTTP report unavailable")
	}
	var report struct {
		IDs []string `json:"dead_letter_test_message_ids"`
	}
	if json.Unmarshal(data, &report) != nil || len(report.IDs) != 2 {
		return errors.New("unexpected local HTTP report")
	}
	owned := map[string]bool{}
	for _, id := range report.IDs {
		owned[id] = true
	}
	env, err := os.ReadFile(filepath.Join(base, "local/rabbitmq.env"))
	if err != nil {
		return errors.New("local RabbitMQ settings unavailable")
	}
	address := ""
	for _, line := range strings.Split(string(env), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "RABBITMQ_URL" {
			address = strings.Trim(strings.TrimSpace(value), "\"'")
		}
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "amqp" || u.Host != "127.0.0.1:5672" || u.Path != "/cpm_dev" {
		return errors.New("only fixed loopback cpm_dev RabbitMQ is supported")
	}
	conn, err := amqp.DialConfig(address, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return errors.New("local RabbitMQ unavailable")
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return errors.New("local RabbitMQ channel unavailable")
	}
	// Closing the channel automatically requeues every unrelated delivery. We
	// retain those unacknowledged while scanning so Get can reach our own IDs.
	defer ch.Close()
	removed, unrelated := 0, 0
	for i := 0; i < 1000; i++ {
		d, ok, err := ch.Get("calculate.events.dead.v1", false)
		if err != nil {
			return errors.New("could not inspect local dead-letter queue")
		}
		if !ok {
			break
		}
		var message struct {
			ID string `json:"message_id"`
		}
		if json.Unmarshal(d.Body, &message) == nil && owned[message.ID] {
			if err := d.Ack(false); err != nil {
				return errors.New("could not remove own rejected fixture")
			}
			removed++
		} else {
			unrelated++
		}
	}
	fmt.Printf("Removed %d own rejected fixtures; %d unrelated messages requeued.\n", removed, unrelated)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
