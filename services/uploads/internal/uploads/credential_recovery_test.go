package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture rejects cancelled SQL contexts, matching database/sql behavior.
// It also checks that post-owner persistence has a bounded independent deadline.
type credentialFixture struct {
	memory       *ownerRecoveryMemory
	afterOwner   atomic.Bool
	detachedSQL  atomic.Int64
	storedSource atomic.Bool
}
type credentialConnector struct{ fixture *credentialFixture }
type credentialConn struct {
	ownerRecoveryConn
	fixture *credentialFixture
}

func (c *credentialConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return &credentialConn{ownerRecoveryConn: ownerRecoveryConn{memory: c.fixture.memory}, fixture: c.fixture}, nil
}
func (*credentialConnector) Driver() driver.Driver { return ownerRecoveryDriver{} }
func (c *credentialConn) checkContext(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if c.fixture.afterOwner.Load() {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > creationOutcomeTimeout+100*time.Millisecond {
			return errors.New("post-owner SQL must have a bounded outcome context")
		}
		c.fixture.detachedSQL.Add(1)
	}
	return nil
}
func (c *credentialConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	if e := c.checkContext(ctx); e != nil {
		return nil, e
	}
	return c.ownerRecoveryConn.BeginTx(ctx, options)
}
func (c *credentialConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if e := c.checkContext(ctx); e != nil {
		return nil, e
	}
	return c.ownerRecoveryConn.QueryContext(ctx, q, args)
}
func (c *credentialConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if e := c.checkContext(ctx); e != nil {
		return nil, e
	}
	if strings.HasPrefix(q, "INSERT INTO uploads_sessions_v1") {
		if len(args) != 14 {
			return nil, errors.New("unexpected session insert")
		}
		c.memory.mu.Lock()
		defer c.memory.mu.Unlock()
		c.memory.ss = Session{ID: args[0].Value.(string), Scope: Scope{OrganizationID: args[1].Value.(int64), DivisionID: args[2].Value.(int64), ActorID: args[3].Value.(int64)},
			Type: args[4].Value.(string), Owner: args[5].Value.(string), OwnerSessionID: args[6].Value.(string), TargetID: args[7].Value.(string),
			Revision: args[8].Value.(int64), OwnerRevision: args[9].Value.(int64), State: args[10].Value.(string), FileID: args[11].Value.(string),
			SourceHash: args[12].Value.(string), Expires: args[13].Value.(time.Time), Created: time.Now().UTC()}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "INSERT INTO uploads_files_v1") {
		if len(args) != 11 {
			return nil, errors.New("unexpected file insert")
		}
		if b, ok := args[9].Value.([]byte); ok && len(b) > 0 {
			c.fixture.storedSource.Store(true)
		}
		return driver.RowsAffected(1), nil
	}
	return c.ownerRecoveryConn.ExecContext(ctx, q, args)
}

type credentialRoundTrip func(*http.Request) (*http.Response, error)

func (fn credentialRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func credentialResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{Request: r, StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func credentialActor(sc Scope, edit bool) Actor {
	d := sc.DivisionID
	return Actor{AccountID: sc.ActorID, OrganizationID: sc.OrganizationID, DivisionID: &d, Role: "staff_admin",
		Permissions: map[string]Permission{"upload": {View: true, Edit: edit}, "users": {View: true, Edit: edit}}}
}
func credentialMemory() *ownerRecoveryMemory {
	ss := ownerAdapterSession("users.credentials")
	ss.State, ss.OwnerSessionID, ss.TargetID = "staging", "", ""
	ss.Created = time.Now().UTC().Add(-10 * time.Minute)
	return &ownerRecoveryMemory{ss: ss, receipt: &ownerAdapterReceipt{key: "original-create-001", action: "session.create", id: ss.ID, state: "reserved"}}
}
func credentialFixtureDB(t *testing.T, memory *ownerRecoveryMemory) (*sql.DB, *credentialFixture) {
	t.Helper()
	f := &credentialFixture{memory: memory}
	db := sql.OpenDB(&credentialConnector{fixture: f})
	t.Cleanup(func() { db.Close() })
	return db, f
}
func credentialCreateRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if e := w.WriteField("type", "users.credentials"); e != nil {
		t.Fatal(e)
	}
	file, e := w.CreateFormFile("file", "synthetic.csv")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = io.WriteString(file, "full_name,login,password\nSynthetic Person,fixture-login,synthetic-private-password\n"); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "http://uploads.invalid/sessions", &body).WithContext(ctx)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Idempotency-Key", "original-create-001")
	r.Header.Set("Authorization", "Bearer fixture-live")
	return r
}

func TestCredentialCreatePersistsOutcomeAfterCancellation(t *testing.T) {
	for _, test := range []struct {
		name, body, state, receipt string
		status                     int
		transportError, cancel     bool
	}{
		{"cancelled_transport", "", "outcome_unknown", "outcome_unknown", 0, true, true},
		{"accepted_after_cancel", `{"data":{"id":23}}`, "preview", "completed", 201, false, true},
		{"rejected_after_cancel", `{"error":"denied"}`, "rejected", "rejected", 403, false, true},
		{"malformed_accepted", `{"data":{}}`, "outcome_unknown", "outcome_unknown", 200, false, false},
		{"nonnumeric_core_id", `{"data":{"id":"invalid-native-reference"}}`, "outcome_unknown", "outcome_unknown", 200, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory := credentialMemory()
			memory.receipt = nil
			db, fixture := credentialFixtureDB(t, memory)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var posts atomic.Int64
			client := &http.Client{Transport: credentialRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer fixture-live" {
					t.Error("owner lost the original live bearer")
				}
				if r.Method == "POST" {
					posts.Add(1)
					if r.Context() != ctx || r.Context().Err() != nil {
						t.Error("owner POST used a detached or expired context")
					}
					fixture.afterOwner.Store(true)
					if test.cancel {
						cancel()
					}
					if test.transportError {
						return nil, context.Canceled
					}
					return credentialResponse(r, test.status, test.body), nil
				}
				if r.Context() != ctx || !test.cancel || r.Context().Err() != context.Canceled {
					t.Error("post-create owner GET must retain the cancelled original context")
				}
				return nil, context.Canceled
			})}
			s := &Server{DB: db, HTTP: client, Config: Config{CoreURL: "http://owner.invalid"}}
			r := credentialCreateRequest(t, ctx)
			_, _, e := s.create(r, credentialActor(memory.ss.Scope, true), memory.ss.Scope)
			if e == nil {
				t.Fatal("cancelled or malformed call unexpectedly returned success")
			}
			if memory.ss.State != test.state || memory.receipt == nil || memory.receipt.state != test.receipt || fixture.detachedSQL.Load() < 4 {
				t.Fatalf("post-owner outcome was not durably recorded: state=%s receipt=%v SQL=%d error=%v", memory.ss.State, memory.receipt, fixture.detachedSQL.Load(), e)
			}
			if test.state == "preview" && memory.ss.OwnerSessionID != "23" {
				t.Fatal("accepted owner reference lost after cancellation")
			}
			if posts.Load() != 1 || fixture.storedSource.Load() || len(memory.events) != 2 || memory.events[1] != "session."+test.state {
				t.Fatal("outcome persistence resent input, stored credential bytes, or lost its event")
			}
		})
	}
}

func TestCredentialSessionReadAndReconcileMissingNativeReceipt(t *testing.T) {
	for _, method := range []string{"get", "reconcile"} {
		for _, state := range []string{"old_staging", "young_staging", "already_unknown"} {
			t.Run(method+"/"+state, func(t *testing.T) {
				memory := credentialMemory()
				want := "outcome_unknown"
				changed := state == "old_staging"
				if state == "young_staging" {
					memory.ss.Created = time.Now().UTC()
					want = "staging"
				} else if state == "already_unknown" {
					memory.ss.State = "outcome_unknown"
				}
				db, fixture := credentialFixtureDB(t, memory)
				var calls atomic.Int64
				client := &http.Client{Transport: credentialRoundTrip(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					if r.Method != "GET" || r.URL.Path != "/v1/core/divisions/7/imports/receipts/"+ownerCommand("create", memory.ss.ID, "") || r.Header.Get("Authorization") != "Bearer fixture-live" {
						t.Error("recovery did not read the original scoped native creation receipt")
					}
					fixture.afterOwner.Store(true)
					return credentialResponse(r, 404, `{"error":"missing"}`), nil
				})}
				s := &Server{DB: db, HTTP: client, Config: Config{CoreURL: "http://owner.invalid"}}
				r := httptest.NewRequest("GET", "http://uploads.invalid/sessions/"+memory.ss.ID, nil)
				r.SetPathValue("session", memory.ss.ID)
				r.Header.Set("Authorization", "Bearer fixture-live")
				r.Header.Set("Idempotency-Key", "unrelated-read-command")
				var got any
				var code int
				var e error
				if method == "get" {
					got, code, e = s.sessionGet(r, credentialActor(memory.ss.Scope, false), memory.ss.Scope)
				} else {
					got, code, e = s.reconcile(r, credentialActor(memory.ss.Scope, true), memory.ss.Scope)
				}
				if e != nil || code != 200 || got.(Session).State != want || memory.ss.State != want || calls.Load() != 1 {
					t.Fatalf("missing native receipt confused pending outcome: %#v %d %v", got, code, e)
				}
				if changed {
					if memory.receipt.state != "outcome_unknown" || memory.receiptWrites != 1 || len(memory.events) != 1 || memory.events[0] != "session.outcome_unknown" {
						t.Fatal("old orphan was not guardedly classified as unknown")
					}
				} else if memory.receiptWrites != 0 || len(memory.events) != 0 {
					t.Fatal("active or already-unknown session was changed by a missing native receipt")
				}
			})
		}
	}
}

func TestCredentialSessionRecoveryFoundReceiptAndLateLifecycle(t *testing.T) {
	for _, state := range []string{"staging", "outcome_unknown", "queued", "awaiting_authorization", "completed"} {
		t.Run(state, func(t *testing.T) {
			memory := credentialMemory()
			if state == "outcome_unknown" {
				memory.ss.State = state
			}
			db, fixture := credentialFixtureDB(t, memory)
			var calls atomic.Int64
			client := &http.Client{Transport: credentialRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-live" {
					t.Error("credential recovery mutated owner data or changed bearer")
				}
				if strings.Contains(r.URL.Path, "/imports/receipts/") {
					if r.URL.Path != "/v1/core/divisions/7/imports/receipts/"+ownerCommand("create", memory.ss.ID, "") {
						t.Error("native creation receipt key changed")
					}
					if state != "staging" && state != "outcome_unknown" {
						memory.mu.Lock()
						memory.ss.State, memory.ss.OwnerSessionID, memory.receipt.state = state, "23", "completed"
						memory.mu.Unlock()
					}
					return credentialResponse(r, 200, `{"data":{"session_id":23}}`), nil
				}
				if r.URL.Path != "/v1/core/divisions/7/imports/23" {
					t.Error("native preview reference changed")
				}
				fixture.afterOwner.Store(true)
				return credentialResponse(r, 200, `{"data":{"id":23,"status":"pending","users":[{"full_name":"Synthetic First","password":"synthetic-private-password"},{"full_name":"Synthetic Last","nested":{"token":"synthetic-private-token"}}]}}`), nil
			})}
			s := &Server{DB: db, HTTP: client, Config: Config{CoreURL: "http://owner.invalid"}}
			r := httptest.NewRequest("GET", "http://uploads.invalid/sessions/"+memory.ss.ID, nil)
			r.SetPathValue("session", memory.ss.ID)
			r.Header.Set("Authorization", "Bearer fixture-live")
			got, code, e := s.sessionGet(r, credentialActor(memory.ss.Scope, false), memory.ss.Scope)
			want := state
			if state == "staging" || state == "outcome_unknown" {
				want = "preview"
			}
			if e != nil || code != 200 || got.(Session).State != want || memory.ss.State != want || got.(Session).OwnerSessionID != "23" || calls.Load() != 2 {
				t.Fatalf("recovery lost preview or reopened a queued lifecycle: %#v %v", got, e)
			}
			raw, _ := json.Marshal(got)
			if !bytes.Contains(raw, []byte("Synthetic Last")) || bytes.Contains(raw, []byte("synthetic-private-")) || fixture.storedSource.Load() {
				t.Fatal("recovery returned a partial preview or private credential material")
			}
			if want == "preview" {
				if memory.receipt.state != "completed" || memory.receiptWrites != 1 || len(memory.events) != 1 || memory.events[0] != "session.recovered" {
					t.Fatal("recovery did not complete the original creation receipt")
				}
			} else if memory.receiptWrites != 0 || len(memory.events) != 0 {
				t.Fatal("late recovery wrote over a confirmed lifecycle or receipt")
			}
		})
	}
}

func TestCredentialRecoveryFailsClosedBeforeNativeLookup(t *testing.T) {
	for _, scenario := range []string{"missing_key", "invalid_key", "wrong_action", "wrong_receipt_resource", "wrong_org", "wrong_division", "wrong_actor", "permission", "reconcile_permission"} {
		t.Run(scenario, func(t *testing.T) {
			memory := credentialMemory()
			db, _ := credentialFixtureDB(t, memory)
			sc := memory.ss.Scope
			a := credentialActor(sc, true)
			switch scenario {
			case "missing_key":
				memory.receipt = nil
			case "invalid_key":
				memory.receipt.key = "invalid key"
			case "wrong_action":
				memory.receipt.action = "session.commit"
			case "wrong_receipt_resource":
				memory.receipt.id = strings.Repeat("b", 32)
			case "wrong_org":
				sc.OrganizationID++
			case "wrong_division":
				sc.DivisionID++
			case "wrong_actor":
				sc.ActorID++
			case "permission":
				a.Permissions["users"] = Permission{}
			case "reconcile_permission":
				a = credentialActor(sc, false)
			}
			var calls atomic.Int64
			s := &Server{DB: db, Config: Config{CoreURL: "http://owner.invalid"}, HTTP: &http.Client{Transport: credentialRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return credentialResponse(r, 404, `{}`), nil
			})}}
			r := httptest.NewRequest("GET", "http://uploads.invalid/session", nil)
			r.SetPathValue("session", memory.ss.ID)
			r.Header.Set("Authorization", "Bearer fixture-live")
			var e error
			if scenario == "reconcile_permission" {
				_, _, e = s.reconcile(r, a, sc)
			} else {
				_, _, e = s.sessionGet(r, a, sc)
			}
			if e == nil || calls.Load() != 0 || memory.ss.State != "staging" || memory.receiptWrites != 0 || len(memory.events) != 0 {
				t.Fatalf("unbound recovery reached the native owner: %v", e)
			}
		})
	}
}

func TestCredentialRecoveryMalformedNativeReferenceStaysUnknown(t *testing.T) {
	for _, body := range []string{`{"data":{}}`, `{"data":{"session_id":0}}`, `{"data":{"session_id":"arbitrary-native-key"}}`} {
		t.Run(body, func(t *testing.T) {
			memory := credentialMemory()
			db, fixture := credentialFixtureDB(t, memory)
			var calls atomic.Int64
			s := &Server{DB: db, Config: Config{CoreURL: "http://owner.invalid"}, HTTP: &http.Client{Transport: credentialRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				fixture.afterOwner.Store(true)
				return credentialResponse(r, 200, body), nil
			})}}
			r := httptest.NewRequest("GET", "http://uploads.invalid/session", nil)
			r.SetPathValue("session", memory.ss.ID)
			r.Header.Set("Authorization", "Bearer fixture-live")
			_, _, e := s.sessionGet(r, credentialActor(memory.ss.Scope, false), memory.ss.Scope)
			assertOwnerAPIError(t, e, 503)
			if memory.ss.State != "outcome_unknown" || memory.ss.OwnerSessionID != "" || memory.receipt.state != "outcome_unknown" || calls.Load() != 1 || memory.receiptWrites != 1 {
				t.Fatal("malformed native reference became a usable preview")
			}
		})
	}
}

func TestCredentialLateUncertainOutcomeCannotDowngradeQueuedSession(t *testing.T) {
	for _, scenario := range []string{"cancelled_create_failure", "missing_native_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			memory := credentialMemory()
			if scenario == "cancelled_create_failure" {
				memory.receipt = nil
			}
			db, fixture := credentialFixtureDB(t, memory)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int64
			s := &Server{DB: db, Config: Config{CoreURL: "http://owner.invalid"}, HTTP: &http.Client{Transport: credentialRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				memory.mu.Lock()
				memory.ss.State, memory.ss.OwnerSessionID, memory.receipt.state = "queued", "23", "completed"
				memory.mu.Unlock()
				fixture.afterOwner.Store(true)
				if scenario == "cancelled_create_failure" {
					if r.Method != "POST" || r.Context() != ctx {
						t.Error("create changed the owner request context")
					}
					cancel()
					return nil, context.Canceled
				}
				if r.Method != "GET" || !strings.Contains(r.URL.Path, "/imports/receipts/") {
					t.Error("missing receipt recovery resent input")
				}
				return credentialResponse(r, 404, `{}`), nil
			})}}
			if scenario == "cancelled_create_failure" {
				r := credentialCreateRequest(t, ctx)
				_, _, e := s.create(r, credentialActor(memory.ss.Scope, true), memory.ss.Scope)
				if e == nil {
					t.Fatal("cancelled owner failure unexpectedly succeeded")
				}
			} else {
				r := httptest.NewRequest("GET", "http://uploads.invalid/session", nil).WithContext(ctx)
				r.SetPathValue("session", memory.ss.ID)
				r.Header.Set("Authorization", "Bearer fixture-live")
				got, code, e := s.sessionGet(r, credentialActor(memory.ss.Scope, false), memory.ss.Scope)
				if e != nil || code != 200 || got.(Session).State != "queued" {
					t.Fatalf("late missing receipt lost current lifecycle: %#v %v", got, e)
				}
			}
			wantEvents := 0
			if scenario == "cancelled_create_failure" {
				wantEvents = 1 // Only the pre-owner staging event.
			}
			if calls.Load() != 1 || memory.ss.State != "queued" || memory.receipt.state != "completed" || memory.receiptWrites != 0 || len(memory.events) != wantEvents {
				t.Fatal("late uncertain outcome downgraded a recovered receipt or lifecycle")
			}
		})
	}
}

func TestCredentialReconcileChecksFullPreviewReference(t *testing.T) {
	for _, id := range []string{"23", "24"} {
		t.Run(id, func(t *testing.T) {
			memory := credentialMemory()
			db, fixture := credentialFixtureDB(t, memory)
			var calls atomic.Int64
			s := &Server{DB: db, Config: Config{CoreURL: "http://owner.invalid"}, HTTP: &http.Client{Transport: credentialRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-live" {
					t.Error("reconcile did not use a protected native read")
				}
				if strings.Contains(r.URL.Path, "/imports/receipts/") {
					return credentialResponse(r, 200, `{"data":{"session_id":23}}`), nil
				}
				fixture.afterOwner.Store(true)
				return credentialResponse(r, 200, `{"data":{"id":`+id+`,"users":[{"full_name":"Synthetic Person","password":"synthetic-private-password"}]}}`), nil
			})}}
			r := httptest.NewRequest("POST", "http://uploads.invalid/session/reconcile", nil)
			r.SetPathValue("session", memory.ss.ID)
			r.Header.Set("Authorization", "Bearer fixture-live")
			got, code, e := s.reconcile(r, credentialActor(memory.ss.Scope, true), memory.ss.Scope)
			if calls.Load() != 2 {
				t.Fatal("reconcile changed the read-only native recovery sequence")
			}
			if id == "24" {
				assertOwnerAPIError(t, e, 503)
				if memory.ss.State != "outcome_unknown" || memory.ss.OwnerSessionID != "" || memory.receipt.state != "outcome_unknown" {
					t.Fatal("mismatched full preview became the original import session")
				}
				return
			}
			if e != nil || code != 200 || got.(Session).State != "preview" || memory.receipt.state != "completed" {
				t.Fatalf("reconcile did not recover the original accepted preview: %#v %v", got, e)
			}
			raw, _ := json.Marshal(got)
			if !bytes.Contains(raw, []byte("Synthetic Person")) || bytes.Contains(raw, []byte("synthetic-private-password")) {
				t.Fatal("reconcile lost full preview or exposed credentials")
			}
		})
	}
}
