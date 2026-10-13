// Guarded live Rabbit/inbox check. Retains isolated fixtures, never stops services.
package main

import (
	"context"
	"cpm/ratings/internal/ratings"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
	"os"
	"time"
)

func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic("random unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func run() error {
	c, e := ratings.LoadConfig()
	if e != nil {
		return fmt.Errorf("config unavailable")
	}
	m, e := mysql.ParseDSN(c.DSN)
	if e != nil || m.DBName != "cpm_dev" || m.Addr != "127.0.0.1:13306" {
		return fmt.Errorf("dev SQL guard")
	}
	u, e := amqp.ParseURI(c.RabbitURL)
	if e != nil || u.Host != "127.0.0.1" || u.Port != 5672 || u.Vhost != "cpm_dev" {
		return fmt.Errorf("dev Rabbit guard")
	}
	db, e := sql.Open("mysql", c.DSN)
	if e != nil {
		return fmt.Errorf("SQL unavailable")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	conn, e := amqp.Dial(c.RabbitURL)
	if e != nil {
		return fmt.Errorf("Rabbit unavailable")
	}
	defer conn.Close()
	ch, e := conn.Channel()
	if e != nil {
		return fmt.Errorf("channel unavailable")
	}
	defer ch.Close()
	if e = ch.Confirm(false); e != nil {
		return fmt.Errorf("confirm unavailable")
	}
	returns := ch.NotifyReturn(make(chan amqp.Return, 8))
	before, e := ch.QueueInspect("cpm.ratings.exams-source.dead.v1")
	if e != nil {
		return fmt.Errorf("topology unavailable")
	}
	seed := time.Now().UnixNano()%1000000000 + 7000000000
	org, div := seed, seed+1
	event := func(o, d, rev int64) map[string]any {
		return map[string]any{"message_id": id(), "message_type": "exams.source.changed", "schema_version": 1, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "producer": "exams-service", "correlation_id": id(), "payload": map[string]any{"organization_id": o, "division_id": d, "exam_id": seed + 2, "exam_revision": int64(1), "source_revision": rev, "reason": "dev_invalidation_probe"}}
	}
	first := event(org, div, 9)
	older := event(org, div, 3)
	foreignOrg := event(org+3, div, 22)
	foreignDiv := event(org, div+4, 44)
	conflict := event(org+3, div, 55)
	conflict["message_id"] = first["message_id"]
	invalid := event(org, div, 99)
	invalid["schema_version"] = 2
	for _, ev := range []map[string]any{first, first, older, foreignOrg, foreignDiv, conflict, invalid} {
		raw, _ := json.Marshal(ev)
		confirmation, err := ch.PublishWithDeferredConfirmWithContext(ctx, "cpm.events", "exams.source.changed", true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: ev["message_id"].(string), Body: raw})
		if err != nil {
			return fmt.Errorf("publish failed")
		}
		ok, err := confirmation.WaitContext(ctx)
		if err != nil || !ok {
			return fmt.Errorf("confirm failed")
		}
		select {
		case <-returns:
			return fmt.Errorf("unroutable")
		default:
		}
	}
	until := time.Now().Add(20 * time.Second)
	for {
		var count int
		if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ratings_source_events_v1 WHERE organization_id IN (?,?) AND division_id IN (?,?)", org, org+3, div, div+4).Scan(&count); e != nil {
			return fmt.Errorf("inbox unavailable")
		}
		dead, err := ch.QueueInspect("cpm.ratings.exams-source.dead.v1")
		if err != nil {
			return fmt.Errorf("dead-letter unavailable")
		}
		if count == 4 && dead.Messages >= before.Messages+2 {
			break
		}
		if time.Now().After(until) {
			return fmt.Errorf("delivery timeout")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, s := range [][3]int64{{org, div, 9}, {org + 3, div, 22}, {org, div + 4, 44}} {
		var rev int64
		if e = db.QueryRowContext(ctx, "SELECT revision FROM ratings_source_revisions_v1 WHERE source='exams' AND organization_id=? AND division_id=?", s[0], s[1]).Scan(&rev); e != nil || rev != s[2] {
			return fmt.Errorf("scope revision mismatch")
		}
	}
	var invalidCount, jobs int
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ratings_source_events_v1 WHERE message_id=?", invalid["message_id"]).Scan(&invalidCount); e != nil || invalidCount != 0 {
		return fmt.Errorf("invalid schema accepted")
	}
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ratings_jobs_v1 WHERE organization_id IN (?,?) AND division_id IN (?,?)", org, org+3, div, div+4).Scan(&jobs); e != nil || jobs != 0 {
		return fmt.Errorf("automatic jobs created")
	}
	report := map[string]any{"status": "passed", "organization_ids": []int64{org, org + 3}, "division_ids": []int64{div, div + 4}, "published_messages": 7, "inbox_records": 4, "cases": []string{"persistent confirmed delivery", "duplicate dedup", "older revision monotonic", "foreign organization isolated", "foreign division isolated", "identity conflict DLQ", "unsupported schema DLQ", "no automatic jobs"}, "fixtures_retained": true}
	raw, _ := json.MarshalIndent(report, "", "  ")
	target := os.Getenv("RATINGS_EVENTS_REPORT")
	if target == "" {
		return fmt.Errorf("report path required")
	}
	if e = os.WriteFile(target, append(raw, '\n'), 0600); e != nil {
		return fmt.Errorf("report write failed")
	}
	fmt.Println("Ratings source events passed: 8 cases, 7 messages, 4 inbox records.")
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e.Error())
		os.Exit(1)
	}
}
