package uploads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Opt-in transport tests use only synthetic uploads-owned records and random
// uploads test queues. The ordinary uploads daemon must be stopped: its outbox
// publisher shares this ledger and would race with the isolated test publisher.
func TestRealBrokerDeliveryRecoveryAndScope(t *testing.T) {
	if os.Getenv("UPLOADS_DELIVERY_INTEGRATION") != "true" {
		t.Skip("explicit isolated dev transport test opt-in required")
	}
	if live, e := net.DialTimeout("tcp", "127.0.0.1:8014", 250*time.Millisecond); e == nil {
		live.Close()
		t.Fatal("stop the ordinary dev Uploads daemon before isolated ledger tests")
	}
	c, e := LoadConfig()
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("mysql", c.DSN)
	if e != nil {
		t.Fatal("dev database open failed")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		t.Fatal("dev database unavailable")
	}
	var database string
	if e = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); e != nil || database != "cpm_dev" {
		t.Fatal("isolated dev guard failed")
	}
	var ownerCalls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ownerCalls.Add(1); w.WriteHeader(500) }))
	defer owner.Close()
	s := New(db, c)
	s.HTTP = owner.Client()
	s.Config.AuthURL = owner.URL
	s.Config.CardsURL = owner.URL
	suffix := uuid()
	b := NewDeliveryBroker(db, c.RabbitURL, nil)
	b.exchange = "cpm.uploads.test." + suffix
	b.executeQueue = "uploads.test.execute." + suffix
	b.retryQueue = "uploads.test.retry." + suffix
	b.deadQueue = "uploads.test.dead." + suffix
	conn, e := amqp.Dial(c.RabbitURL)
	if e != nil {
		t.Fatal("dev Rabbit unavailable")
	}
	defer conn.Close()
	ch, e := conn.Channel()
	if e != nil {
		t.Fatal("test channel unavailable")
	}
	defer ch.Close()
	if e = b.declare(ch); e != nil {
		t.Fatal("test topology unavailable")
	}
	defer func() {
		_, _ = ch.QueueDelete(b.executeQueue, false, false, false)
		_, _ = ch.QueueDelete(b.retryQueue, false, false, false)
		_, _ = ch.QueueDelete(b.deadQueue, false, false, false)
		_ = ch.ExchangeDelete(b.exchange, false, false)
	}()
	org := int64(97000101)
	sc := Scope{OrganizationID: org, DivisionID: 97000111, ActorID: 97000121}
	ids := []string{}
	sessions := []string{}
	defer func() {
		for _, id := range ids {
			_, _ = db.Exec("DELETE FROM uploads_inbox_v1 WHERE job_id=?", id)
			_, _ = db.Exec("DELETE FROM uploads_outbox_v1 WHERE job_id=?", id)
			_, _ = db.Exec("DELETE FROM uploads_events_v1 WHERE resource_id=? AND organization_id=?", id, org)
			_, _ = db.Exec("DELETE FROM uploads_jobs_v1 WHERE id=? AND organization_id=?", id, org)
		}
		for _, id := range sessions {
			_, _ = db.Exec("DELETE FROM uploads_sessions_v1 WHERE id=? AND organization_id=?", id, org)
		}
	}()
	fixture := func(scope Scope) (string, string) {
		t.Helper()
		jid, sid := uuid(), uuid()
		ids = append(ids, jid)
		sessions = append(sessions, sid)
		ss := Session{ID: sid, Scope: scope, Type: "cards.xlsx", Owner: "cards", OwnerSessionID: "synthetic-cards-" + sid,
			Revision: 1, OwnerRevision: 1, State: "queued", SourceHash: strings.Repeat("d", 64), Expires: time.Now().UTC().Add(time.Hour)}
		if e = s.tx(ctx, func(tx *sql.Tx) error {
			if e := insertSession(ctx, tx, ss); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "INSERT INTO uploads_jobs_v1(id,organization_id,division_id,actor_account_id,session_id,type,owner,status) VALUES(?,?,?,?,?,'cards.xlsx','cards','queued')", jid, scope.OrganizationID, scope.DivisionID, scope.ActorID, sid); e != nil {
				return e
			}
			return EnqueueImport(ctx, tx, jid, scope)
		}); e != nil {
			t.Fatal("transport fixture create failed", e)
		}
		var mid string
		if e = db.QueryRowContext(ctx, "SELECT id FROM uploads_outbox_v1 WHERE job_id=?", jid).Scan(&mid); e != nil {
			t.Fatal(e)
		}
		return jid, mid
	}
	completedID, completedMessage := fixture(sc)
	pendingID, pendingMessage := fixture(Scope{OrganizationID: org, DivisionID: 97000112, ActorID: sc.ActorID})
	unknownID, _ := fixture(sc)
	recoveredID, recoveredMessage := fixture(sc)
	var executions atomic.Int64
	b.Execute = func(c context.Context, id string, scope Scope) error {
		if id == pendingID {
			return s.process(c, id, scope)
		}
		if id != completedID && id != unknownID && id != recoveredID {
			return errors.New("fixture_scope_rejected")
		}
		if scope != sc {
			return errors.New("fixture_scope_rejected")
		}
		executions.Add(1)
		_, e := db.ExecContext(c, "UPDATE uploads_jobs_v1 SET status='completed',processed=1,total=1,lease_token=NULL,lease_until=NULL WHERE id=? AND organization_id=? AND division_id=? AND actor_account_id=?", id, scope.OrganizationID, scope.DivisionID, scope.ActorID)
		if e != nil {
			return e
		}
		if id == unknownID {
			return errors.New("simulated_response_loss_after_durable_outcome")
		}
		return nil
	}
	// Simulate a persisted running lease after a process crash, with a confirmed
	// outbox delivery no longer in Rabbit. Restart recovery must replay its ID.
	var recoveryRaw []byte
	if e = db.QueryRowContext(ctx, "SELECT body FROM uploads_outbox_v1 WHERE id=?", recoveredMessage).Scan(&recoveryRaw); e != nil {
		t.Fatal(e)
	}
	ev, e := decodeImport(recoveryRaw)
	if e != nil {
		t.Fatal(e)
	}
	claim, e := b.claim(ctx, ev)
	if e != nil || !claim.Run {
		t.Fatal("recovery fixture claim failed")
	}
	if _, e = db.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='running',lease_token=?,lease_until=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE id=?", uuid(), recoveredID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET lease_until=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE id=?", recoveredMessage); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, "UPDATE uploads_outbox_v1 SET published_at=UTC_TIMESTAMP(6) WHERE id=?", recoveredMessage); e != nil {
		t.Fatal(e)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); b.Run(runCtx) }()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("transport broker did not stop")
		}
	}()
	wait := func(id, want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var state string
			if db.QueryRowContext(ctx, "SELECT status FROM uploads_jobs_v1 WHERE id=?", id).Scan(&state) == nil && state == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("persisted transport outcome not reached")
	}
	wait(completedID, "completed")
	wait(pendingID, "awaiting_authorization")
	wait(unknownID, "completed")
	wait(recoveredID, "completed")
	var code, inboxState string
	if e = db.QueryRowContext(ctx, "SELECT error_code FROM uploads_jobs_v1 WHERE id=?", pendingID).Scan(&code); e != nil || code != "background_authorization_pending" {
		t.Fatal("authorization dependency was hidden")
	}
	if ownerCalls.Load() != 0 {
		t.Fatal("pending authorization invoked an owner")
	}
	if e = db.QueryRowContext(ctx, "SELECT state FROM uploads_inbox_v1 WHERE id=?", pendingMessage).Scan(&inboxState); e != nil || inboxState != "done" {
		t.Fatal("waiting outcome not durably acknowledged")
	}
	if executions.Load() != 3 {
		t.Fatal("durable outcome loss or lease recovery executed incorrectly")
	}
	// A same-ID delivery after an ACK loss must use the durable inbox receipt.
	var duplicateRaw []byte
	if e = db.QueryRowContext(ctx, "SELECT body FROM uploads_outbox_v1 WHERE id=?", completedMessage).Scan(&duplicateRaw); e != nil {
		t.Fatal(e)
	}
	if e = ch.PublishWithContext(ctx, b.exchange, importExecuteRoute, false, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: completedMessage, Body: duplicateRaw}); e != nil {
		t.Fatal("duplicate publish failed")
	}
	// Guessing the IDs with another organization/division must be rejected
	// before Execute, even though the transport envelope is structurally valid.
	for _, change := range []func(*ImportEnvelope){
		func(e *ImportEnvelope) { e.Payload.OrganizationID++ },
		func(e *ImportEnvelope) { e.Payload.DivisionID++ },
		func(e *ImportEnvelope) { e.Payload.ActorID++ },
		func(e *ImportEnvelope) { e.Payload.SourceHash = strings.Repeat("e", 64) },
	} {
		forged, _ := decodeImport(duplicateRaw)
		change(&forged)
		raw, _ := json.Marshal(forged)
		if e = ch.PublishWithContext(ctx, b.exchange, importExecuteRoute, false, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: forged.MessageID, Body: raw}); e != nil {
			t.Fatal("forged fixture publish failed")
		}
	}
	unsafe := []byte(`{"authorization":"Bearer synthetic-secret","full_name":"Synthetic Person"}`)
	if e = ch.PublishWithContext(ctx, b.exchange, importExecuteRoute, false, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: uuid(), Body: unsafe}); e != nil {
		t.Fatal("unsafe fixture publish failed")
	}
	deadline := time.Now().Add(10 * time.Second)
	rejected := 0
	for time.Now().Before(deadline) && rejected < 5 {
		d, ok, e := ch.Get(b.deadQueue, true)
		if e != nil {
			t.Fatal("test diagnostic queue read failed")
		}
		if !ok {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if strings.Contains(string(d.Body), "synthetic-secret") || strings.Contains(string(d.Body), "Synthetic Person") || strings.Contains(string(d.Body), "authorization") {
			t.Fatal("rejected raw input leaked into DLQ")
		}
		var diagnostic map[string]any
		if json.Unmarshal(d.Body, &diagnostic) != nil || diagnostic["reason"] != "invalid_import_event" {
			t.Fatal("invalid transport diagnostic")
		}
		rejected++
	}
	if rejected != 5 || executions.Load() != 3 {
		t.Fatal("scope mismatch or duplicate bypassed durable checks")
	}
	stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("transport stop failed")
	}
	// Verify a job enqueued while this broker is stopped survives a new worker
	// session without any in-memory bearer or delivery state.
	newID, _ := fixture(sc)
	b.Execute = func(c context.Context, id string, scope Scope) error {
		if id != newID || scope != sc {
			return errors.New("fixture_rejected")
		}
		return s.process(c, id, scope)
	}
	runCtx, stop = context.WithCancel(ctx)
	defer stop()
	done = make(chan struct{})
	go func() { defer close(done); b.Run(runCtx) }()
	wait(newID, "awaiting_authorization")
	if ownerCalls.Load() != 0 {
		t.Fatal("restart depended on owner authorization in memory")
	}
}
