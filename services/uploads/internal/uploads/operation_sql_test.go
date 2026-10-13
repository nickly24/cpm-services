package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Connection-owned TEMPORARY tables shadow only Uploads names on one pinned
// connection. This exercises real persistence, not shared permanent DDL, native
// Auth or Rabbit. MaxOpenConns=1 cannot prove cross-connection lock contention.
func isolatedOperationSQL(t *testing.T) *sql.DB {
	t.Helper()
	path := os.Getenv("CPM_UPLOADS_OPERATION_SQL_ENV")
	if path == "" {
		t.Skip("isolated local SQL fixture not enabled")
	}
	allowed, _ := filepath.Abs("../../../../dev/local/uploads.env")
	info, err := os.Lstat(path)
	if path != allowed || err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("only private regular local uploads.env accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("private local fixture configuration unavailable")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			values[k] = strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	if values["MYSQL_HOST"] != "127.0.0.1" || values["MYSQL_PORT"] != "13306" || values["MYSQL_DATABASE"] != "cpm_dev" || values["UPLOADS_DEV_MODE"] != "true" {
		t.Fatal("isolated local cpm_dev required")
	}
	c := mysql.NewConfig()
	c.User = values["MYSQL_USER"]
	c.Passwd = values["MYSQL_PASSWORD"]
	c.Net = "tcp"
	c.Addr = "127.0.0.1:13306"
	c.DBName = "cpm_dev"
	c.ParseTime = true
	c.Loc = time.UTC
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 5 * time.Second
	c.WriteTimeout = 5 * time.Second
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		t.Fatal("isolated connection unavailable")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, name := range []string{"001_uploads.sql", "002_delivery.sql", "003_previews.sql", "005_execution_plans.sql"} {
		ddl, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(ddl)
		if name == "002_delivery.sql" {
			source = source[strings.Index(source, "CREATE TABLE IF NOT EXISTS"):]
		}
		lines := []string{}
		for _, line := range strings.Split(source, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				lines = append(lines, line)
			}
		}
		for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if !strings.HasPrefix(strings.TrimSpace(stmt), "CREATE TABLE IF NOT EXISTS uploads_") {
				t.Fatal("unexpected temporary fixture statement")
			}
			stmt = strings.Replace(stmt, "CREATE TABLE IF NOT EXISTS", "CREATE TEMPORARY TABLE", 1)
			if _, err = db.ExecContext(ctx, stmt); err != nil {
				t.Fatal("temporary Uploads schema unavailable", name)
			}
		}
	}
	if _, err = db.ExecContext(ctx, "ALTER TABLE uploads_jobs_v1 ADD COLUMN delivery_generation BIGINT NOT NULL DEFAULT 0"); err != nil {
		t.Fatal("temporary delivery fixture unavailable")
	}
	return db
}

func TestOperationSQLConfirmationRestartLostReplyAndReceiptOnly(t *testing.T) {
	db := isolatedOperationSQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ss, _, ownerPreview, _ := operationFixturePlan(t, "users.credentials")
	ss.State = "preview"
	seed := &Server{DB: db}
	if err := seed.tx(ctx, func(tx *sql.Tx) error { return insertSession(ctx, tx, ss) }); err != nil {
		t.Fatal("temporary fixture session failed", err)
	}
	var issueCalls, applyCalls, receiptCalls atomic.Int64
	var receiptReady atomic.Bool
	var unavailableReceipt atomic.Bool
	var queueRecoveryBeforeOldError atomic.Bool
	var originalIssueKey string
	var originalBinding []byte
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/auth/operation-grants" || r.Header.Get("X-Service-Key") != strings.Repeat("a", 32) || r.Header.Get("Authorization") != "Bearer synthetic-live" {
			t.Error("unexpected issuance authority")
		}
		raw, _ := io.ReadAll(r.Body)
		var b operationBinding
		if decodeOperationJSON(raw, &b) != nil {
			t.Error("invalid frozen binding")
			w.WriteHeader(400)
			return
		}
		p, err := loadExecutionPlan(r.Context(), db, ss.Scope, b.JobID, false)
		if err != nil || !bytes.Equal(p.Command.Body, []byte("{}")) || len(p.BindingBody) == 0 {
			t.Error("issuance before durable immutable command")
		}
		var outbox int
		if db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM uploads_outbox_v1").Scan(&outbox) != nil || outbox != 0 {
			t.Error("outbox published before grant binding")
		}
		if issueCalls.Add(1) == 1 {
			originalIssueKey = r.Header.Get("Idempotency-Key")
			originalBinding = append([]byte(nil), raw...)
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"synthetic lost issuance reply"}`))
			return
		}
		if r.Header.Get("Idempotency-Key") != originalIssueKey || !bytes.Equal(raw, originalBinding) {
			t.Error("issuance replay changed identity/bytes")
		}
		d := ss.DivisionID
		env := operationEnvelope{Status: true, ContractVersion: 1, GrantID: strings.Repeat("7", 32), Mode: "apply", ExpiresAt: ss.Expires.Add(-time.Minute), Binding: b, Actor: operationActor{Actor: credentialActor(ss.Scope, true), ID: 999, DivisionIDs: []int64{d}}}
		env.DivisionPermissions = make([]struct {
			DivisionID  int64                 `json:"division_id"`
			Permissions map[string]Permission `json:"permissions"`
		}, 0)
		json.NewEncoder(w).Encode(env)
	}))
	defer auth.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/imports/17") {
			w.Write([]byte(`{"status":true,"data":` + string(ownerPreview) + `}`))
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Key") != "" || r.Header.Get("X-CPM-Operation-Service-Key") != strings.Repeat("c", 32) {
			t.Error("worker retained bearer or mixed credentials")
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/commit") {
			applyCalls.Add(1)
			receiptReady.Store(true)
			w.WriteHeader(502)
			return
		}
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/receipts/") {
			receiptCalls.Add(1)
			if queueRecoveryBeforeOldError.Swap(false) {
				jobID := r.Header.Get("X-CPM-Operation-Job")
				current, e := loadExecutionPlan(r.Context(), db, ss.Scope, jobID, false)
				if e != nil {
					t.Error(e)
					w.WriteHeader(500)
					return
				}
				var binding operationBinding
				json.Unmarshal(current.BindingBody, &binding)
				key := "explicit-recovery-during-old-worker"
				recovery := &Server{DB: db}
				if _, _, e = recovery.reserve(r.Context(), ss.Scope, key, "job.recover:"+jobID, digest([]byte(`{"confirmed":true}`)), jobID); e != nil {
					t.Error(e)
				}
				if _, e = db.ExecContext(r.Context(), "UPDATE uploads_jobs_v1 SET status='outcome_unknown' WHERE id=?", jobID); e != nil {
					t.Error(e)
				}
				env := operationEnvelope{Status: true, ContractVersion: 1, GrantID: strings.Repeat("8", 32), Mode: "receipt", ExpiresAt: ss.Expires, Binding: binding}
				if e = recovery.bindGrantAndEnqueue(r.Context(), ss.Scope, jobID, env, key); e != nil {
					t.Error("explicit recovery failed", e)
				}
				w.WriteHeader(403) // Late failure from the OLD grant/worker.
				return
			}
			if !receiptReady.Load() || unavailableReceipt.Load() {
				w.WriteHeader(404)
				return
			}
			w.Write([]byte(`{"status":true,"data":{"created":[9007199254740993],"count":1}}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer core.Close()
	cfg := Config{OperationsEnabled: true, AuthURL: auth.URL, CoreURL: core.URL, OperationsIssuerKey: strings.Repeat("a", 32), OperationDeliveryKeys: map[string]string{"core": strings.Repeat("c", 32)}}
	s := New(db, cfg)
	request := func(key string) *http.Request {
		r := httptest.NewRequest("POST", "/commit", strings.NewReader(`{"expected_revision":3,"confirmed":true}`)).WithContext(ctx)
		r.SetPathValue("session", ss.ID)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic-live")
		r.Header.Set("Idempotency-Key", key)
		return r
	}
	value, status, err := s.confirmedCommit(request("original-confirm-001"), credentialActor(ss.Scope, true), ss.Scope)
	if err != nil || status != 202 {
		t.Fatal("confirmation failed", err, status)
	}
	j := value.(Job)
	if j.Status != "authorization_unknown" || j.Authorization == nil || j.Authorization.Bound || !j.Capabilities.RetryConfirmation {
		t.Fatal("lost issuance reply cleared durable confirmation")
	}
	if applyCalls.Load() != 0 {
		t.Fatal("applied without bound grant")
	}
	// A new process resumes the same journal, using the original command key.
	s = New(db, cfg)
	value, status, err = s.confirmedCommit(request("original-confirm-001"), credentialActor(ss.Scope, true), ss.Scope)
	if err != nil || status != 202 {
		t.Fatal("original confirmation could not resume", err, status)
	}
	resumed := value.(Job)
	if resumed.ID != j.ID || resumed.Status != "queued" || resumed.Authorization == nil || !resumed.Authorization.Bound {
		t.Fatal("confirmation identity/binding lost on restart")
	}
	if _, _, err = s.confirmedCommit(request("different-confirm-002"), credentialActor(ss.Scope, true), ss.Scope); err == nil {
		t.Fatal("different intent upgraded existing job")
	}
	var count int
	var raw []byte
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM uploads_outbox_v1").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate outbox after replay")
	}
	if err = db.QueryRowContext(ctx, "SELECT body FROM uploads_outbox_v1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Bearer")) || bytes.Contains(raw, []byte("apply_body")) || bytes.Contains(raw, []byte("grant_id")) {
		t.Fatal("queue exposes private bytes/authority")
	}
	ev, err := decodeImport(raw)
	if err != nil || ev.Payload.JobID != j.ID {
		t.Fatal("invalid Rabbit command", err)
	}
	claim, err := s.Broker.claim(ctx, ev)
	if err != nil || !claim.Run {
		t.Fatal("durable delivery claim failed", err)
	}
	if err = s.processAuthorized(ctx, j.ID, ss.Scope); err != nil {
		t.Fatal("worker receipt completion failed", err)
	}
	completed, err := s.jobWithCapabilities(ctx, credentialActor(ss.Scope, true), ss.Scope, j.ID)
	if err != nil || completed.Status != "completed" || applyCalls.Load() != 1 || receiptCalls.Load() != 1 {
		t.Fatal("lost apply reply not resolved from matching receipt", err)
	}
	p, err := loadExecutionPlan(ctx, db, ss.Scope, j.ID, false)
	if err != nil || !p.ApplyStartedAt.Valid || len(p.OwnerResult) == 0 {
		t.Fatal("possible apply/receipt not durable", err)
	}
	// Simulated restart/redelivery after the marker must never POST again.
	if _, err = db.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='queued' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	unavailableReceipt.Store(true)
	if err = s.processAuthorized(ctx, j.ID, ss.Scope); err != nil {
		t.Fatal(err)
	}
	unknown, err := s.jobWithCapabilities(ctx, credentialActor(ss.Scope, true), ss.Scope, j.ID)
	if err != nil || unknown.Status != "outcome_unknown" || unknown.Capabilities.RetryDelivery || !unknown.Capabilities.RecoverReceipt || applyCalls.Load() != 1 {
		t.Fatal("missing receipt became no-change/apply retry", err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='queued' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	unavailableReceipt.Store(false)
	if err = s.processAuthorized(ctx, j.ID, ss.Scope); err != nil {
		t.Fatal(err)
	}
	if applyCalls.Load() != 1 || receiptCalls.Load() != 3 {
		t.Fatal("redelivery caused second native apply")
	}
	if _, err = db.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='queued' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	queueRecoveryBeforeOldError.Store(true)
	if err = s.processAuthorized(ctx, j.ID, ss.Scope); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.jobWithCapabilities(ctx, credentialActor(ss.Scope, true), ss.Scope, j.ID)
	if err != nil || recovered.Status != "queued" || recovered.Authorization.Mode != "receipt" {
		t.Fatal("late old error erased fresh queued recovery", err)
	}
	p, err = loadExecutionPlan(ctx, db, ss.Scope, j.ID, false)
	if err != nil || p.GrantID.String != strings.Repeat("8", 32) || p.OriginalGrantID.String != strings.Repeat("7", 32) || p.Command.NativeKey != ownerCommand("commit", j.ID, "") {
		t.Fatal("recovery replaced original native command/grant", err)
	}
	if err = s.processAuthorized(ctx, j.ID, ss.Scope); err != nil {
		t.Fatal(err)
	}
	recovered, err = s.jobWithCapabilities(ctx, credentialActor(ss.Scope, true), ss.Scope, j.ID)
	if err != nil || recovered.Status != "completed" || applyCalls.Load() != 1 || receiptCalls.Load() != 5 {
		t.Fatal("fresh recovery did not complete by readonly receipt", err)
	}
	foreign := ss.Scope
	foreign.ActorID++
	if _, err = loadExecutionPlan(ctx, db, foreign, j.ID, false); err != sql.ErrNoRows {
		t.Fatal("foreign private plan exposed", err)
	}
	// Corrupting persisted exact bytes never reaches a native owner.
	if _, err = db.ExecContext(ctx, "UPDATE uploads_execution_plans_v1 SET apply_body=? WHERE job_id=?", []byte("{ }"), j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = loadExecutionPlan(ctx, db, ss.Scope, j.ID, false); err == nil {
		t.Fatal("persisted exact-byte corruption accepted")
	}
	if applyCalls.Load() != 1 {
		t.Fatal("corruption applied")
	}
	t.Logf("temporary SQL proof: issuance=%d native apply=%d readonly receipt=%d; profile=999 account=3; shared permanent DDL/runtime=0; Rabbit transport mocked by persisted envelope claim", issueCalls.Load(), applyCalls.Load(), receiptCalls.Load())
}

func TestOperationSQLRecoveryActionReceiptsResolveJobAfterResponseLoss(t *testing.T) {
	db := isolatedOperationSQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &Server{DB: db}
	sc := Scope{1001, 10235, 3}
	id := strings.Repeat("e", 32)
	if _, err := db.ExecContext(ctx, "INSERT INTO uploads_jobs_v1(id,organization_id,division_id,actor_account_id,session_id,type,owner,status) VALUES(?,?,?,?,?,'users.credentials','core','outcome_unknown')", id, sc.OrganizationID, sc.DivisionID, sc.ActorID, strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"session.commit:", "job.retry:", "job.recover:", "job.revoke:"} {
		key := strings.TrimSuffix(action, ":") + "-lostreply-001"
		if _, _, err := s.reserve(ctx, sc, key, action+id, digest([]byte("{}")), id); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"reserved", "completed"} {
			if err := s.tx(ctx, func(tx *sql.Tx) error { return receipt(ctx, tx, sc, key, state) }); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/receipts/"+key, nil).WithContext(ctx)
			r.SetPathValue("command", key)
			value, status, err := s.getReceipt(r, credentialActor(sc, true), sc)
			if err != nil || status != 200 {
				t.Fatal(action, state, "job action receipt treated as a session", err, status)
			}
			out := value.(map[string]any)
			if out["resource_id"] != id || out["state"] != state || out["action"] != action+id || out["owner"] != "core" {
				t.Fatal("action resource metadata changed")
			}
			foreign := sc
			foreign.ActorID++
			if _, _, err = s.getReceipt(r, credentialActor(foreign, true), foreign); err != sql.ErrNoRows {
				t.Fatal("foreign receipt exposed", err)
			}
		}
	}
}
