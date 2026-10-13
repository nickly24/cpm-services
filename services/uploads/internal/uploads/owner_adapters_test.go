package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func ownerAdapterSession(kind string) Session {
	owner := "cards"
	if strings.HasPrefix(kind, "users.") {
		owner = "core"
	}
	if strings.HasPrefix(kind, "exams.") {
		owner = "exams"
	}
	return Session{ID: strings.Repeat("a", 32), Scope: Scope{OrganizationID: 42, DivisionID: 7, ActorID: 9},
		Type: kind, Owner: owner, OwnerSessionID: "owner-session-123", TargetID: "83", Revision: 3,
		OwnerRevision: 17, State: "preview", SourceHash: strings.Repeat("d", 64), Expires: time.Now().UTC().Add(time.Hour)}
}

func assertOwnerAPIError(t *testing.T, e error, status int) {
	t.Helper()
	var ae *APIError
	if !errors.As(e, &ae) || ae.Code != status {
		t.Fatalf("expected API status %d, got %v", status, e)
	}
}

func TestOwnerCommandUsesIndependentRFC4122GoldenIDs(t *testing.T) {
	// Golden UUIDs obtained with Python uuid.uuid5(uuid.NAMESPACE_URL, name).
	if got := ownerCommand("create", strings.Repeat("a", 32), ""); got != "b7db45cf-1737-5fc2-8d0a-aed6c6586da8" {
		t.Fatal("owner create command changed across implementations")
	}
	if got := ownerCommand("edit", strings.Repeat("a", 32), "ui-edit-001"); got != "cc15dc65-7937-5f51-b16e-7de67204eb03" {
		t.Fatal("owner edit command changed across implementations")
	}
	if ownerCommand("create", strings.Repeat("a", 32), "") == ownerCommand("edit", strings.Repeat("a", 32), "") {
		t.Fatal("different operations share an idempotency command")
	}
	if ownerCommand("edit", strings.Repeat("a", 32), "edit-001") == ownerCommand("edit", strings.Repeat("a", 32), "edit-002") {
		t.Fatal("different preview edits share a command")
	}
}

func TestNativeCreateAndCommitRoutesPreserveOwnerCommandAndAuthorization(t *testing.T) {
	for _, tc := range []struct{ kind, create, get string }{
		{"users.credentials", "/v1/core/divisions/7/imports", "/v1/core/divisions/7/imports/owner-session-123"},
		{"users.legacy", "/v1/core/divisions/7/legacy-imports", "/v1/core/divisions/7/legacy-imports/owner-session-123"},
		{"cards.xlsx", "/v1/cards/imports/parse", "/v1/cards/sessions/owner-session-123"},
		{"cards.transform", "/v1/cards/transforms", "/v1/cards/sessions/owner-session-123"},
		{"exams.questions", "/v1/exams/83/imports/questions", "/v1/exams/83/imports/questions/owner-session-123"},
		{"exams.assignments", "/v1/exams/83/imports/assignments", "/v1/exams/83/imports/assignments/owner-session-123"},
		{"exams.outside", "/v1/exams/83/imports/outside", "/v1/exams/83/imports/outside/owner-session-123"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			ss := ownerAdapterSession(tc.kind)
			command := ownerCommand("create", ss.ID, "")
			commitCommand := ownerCommand("commit", ss.ID, "")
			var creates, commits atomic.Int64
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-owner-token" {
					t.Error("owner request lost actor authorization")
					w.WriteHeader(403)
					return
				}
				if r.Header.Get("X-Service-Key") != "" {
					t.Error("unrelated native owner received a service key")
				}
				body, e := io.ReadAll(r.Body)
				if e != nil {
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case tc.create:
					creates.Add(1)
					if r.Header.Get("Idempotency-Key") != command || r.Header.Get("Content-Type") != "application/octet-stream" || !bytes.Equal(body, []byte("synthetic-source")) {
						t.Error("native create modified the byte/command contract")
					}
					json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"session_id": ss.OwnerSessionID, "revision": 17}})
				case tc.get + "/commit":
					commits.Add(1)
					var in map[string]any
					if json.Unmarshal(body, &in) != nil || r.Header.Get("Idempotency-Key") != commitCommand {
						t.Error("native commit lost its command")
					}
					if tc.kind == "users.credentials" {
						if len(in) != 0 {
							t.Error("credential owner commit requires the native empty command")
						}
					} else if in["expected_revision"] != float64(17) {
						t.Error("native commit did not pin owner revision")
					}
					json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"state": "completed"}})
				default:
					t.Error("wrong native owner route")
					w.WriteHeader(404)
				}
			}))
			defer owner.Close()
			s := &Server{HTTP: newHTTP(), Config: Config{CoreURL: owner.URL, CardsURL: owner.URL, ExamsURL: owner.URL}}
			for i := 0; i < 2; i++ {
				raw, e := s.ownerCreate(context.Background(), ss, "Bearer synthetic-owner-token", command, "application/octet-stream", []byte("synthetic-source"))
				if e != nil {
					t.Fatal(e)
				}
				id, rev, e := ref(raw)
				if e != nil || id != ss.OwnerSessionID || rev != 17 {
					t.Fatal("native create reference was lost")
				}
			}
			if _, e := s.ownerCommit(context.Background(), ss, "Bearer synthetic-owner-token", commitCommand); e != nil {
				t.Fatal(e)
			}
			if creates.Load() != 2 || commits.Load() != 1 {
				t.Fatal("native request count changed")
			}
		})
	}
}

func examAdapterPage(ss Session, offset, total int) map[string]any {
	end := offset + 200
	if end > total {
		end = total
	}
	rows := []any{}
	reports := []any{}
	for n := offset; n < end; n++ {
		errors := []string{}
		if n == 199 || n == 4500 {
			errors = append(errors, "synthetic_owner_validation_error")
			reports = append(reports, map[string]any{"row_id": fmt.Sprintf("row-%04d", n), "source_row": n + 2, "errors": errors})
		}
		rows = append(rows, map[string]any{"row_id": fmt.Sprintf("row-%04d", n), "source_row": n + 2, "errors": errors, "values": map[string]any{"score": n % 100}})
	}
	var next any
	if end < total {
		next = end
	}
	return map[string]any{"id": ss.OwnerSessionID, "revision": ss.OwnerRevision, "state": "editable", "kind": strings.TrimPrefix(ss.Type, "exams."),
		"exam_id": 83, "organization_id": ss.OrganizationID, "division_id": ss.DivisionID, "account_id": ss.ActorID,
		"source_hash": ss.SourceHash, "validation_hash": strings.Repeat("e", 64), "total": total, "limit": 200, "offset": offset,
		"rows": rows, "error_report": reports, "summary": map[string]any{"total": total, "errors": 2}, "has_more": end < total, "next_offset": next}
}

func TestExamsOwnerPreviewIncludesAll5000RowsAndLaterPageErrors(t *testing.T) {
	ss := ownerAdapterSession("exams.questions")
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/exams/83/imports/questions/owner-session-123" || r.URL.Query().Get("limit") != "200" || r.Header.Get("Authorization") != "Bearer synthetic" {
			w.WriteHeader(400)
			return
		}
		offset, e := strconv.Atoi(r.URL.Query().Get("offset"))
		if e != nil || offset < 0 || offset >= 5000 {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"status": true, "data": examAdapterPage(ss, offset, 5000)})
	}))
	defer owner.Close()
	s := &Server{HTTP: newHTTP(), Config: Config{ExamsURL: owner.URL}}
	raw, e := s.ownerGet(context.Background(), ss, "Bearer synthetic")
	if e != nil {
		t.Fatal(e)
	}
	var preview struct {
		Rows []struct {
			RowID  string   `json:"row_id"`
			Errors []string `json:"errors"`
		} `json:"rows"`
		ErrorReport []struct {
			SourceRow int `json:"source_row"`
		} `json:"error_report"`
		HasMore    bool `json:"has_more"`
		NextOffset *int `json:"next_offset"`
	}
	if json.Unmarshal(raw, &preview) != nil || len(preview.Rows) != 5000 || calls.Load() != 25 {
		t.Fatal("truncated or duplicated Exams preview")
	}
	for n, row := range preview.Rows {
		if row.RowID != fmt.Sprintf("row-%04d", n) {
			t.Fatal("page aggregation lost source order or repeated a page")
		}
	}
	if len(preview.Rows[4500].Errors) != 1 || len(preview.ErrorReport) != 2 || preview.ErrorReport[1].SourceRow != 4502 {
		t.Fatal("later-page owner errors are missing from the common report")
	}
	if preview.HasMore || preview.NextOffset != nil {
		t.Fatal("full coordinator preview still advertises incomplete pagination")
	}
}

func TestExamsOwnerPreviewRejectsChangedOrMalformedPagesWithoutPartialData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mutate func(map[string]any)
	}{
		{"revision_changed", 409, func(p map[string]any) { p["revision"] = int64(18) }},
		{"state_changed", 409, func(p map[string]any) { p["state"] = "committed" }},
		{"total_changed", 409, func(p map[string]any) { p["total"] = 402 }},
		{"validation_changed_same_revision", 409, func(p map[string]any) { p["validation_hash"] = strings.Repeat("f", 64) }},
		{"exam_revision_changed_same_preview", 409, func(p map[string]any) { p["exam_revision"] = int64(19) }},
		{"exam_connections_changed_same_preview", 409, func(p map[string]any) { p["connected_division_ids"] = []int64{1002} }},
		{"wrong_session", 503, func(p map[string]any) { p["id"] = "another-session" }},
		{"empty_nonfinal_page", 503, func(p map[string]any) { p["rows"] = []any{} }},
		{"wrong_offset", 503, func(p map[string]any) { p["offset"] = 0 }},
		{"too_many_rows", 503, func(p map[string]any) { p["rows"] = make([]any, 201) }},
		{"too_many_total", 503, func(p map[string]any) { p["total"] = 5001 }},
		{"owner_access_revoked", 403, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ss := ownerAdapterSession("exams.assignments")
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
				page := examAdapterPage(ss, offset, 401)
				if offset == 200 {
					if tc.mutate == nil {
						w.WriteHeader(403)
						w.Write([]byte(`{"error":"private owner message"}`))
						return
					}
					tc.mutate(page)
				}
				json.NewEncoder(w).Encode(map[string]any{"status": true, "data": page})
			}))
			defer owner.Close()
			s := &Server{HTTP: newHTTP(), Config: Config{ExamsURL: owner.URL}}
			raw, e := s.ownerGet(context.Background(), ss, "Bearer synthetic")
			assertOwnerAPIError(t, e, tc.status)
			if len(raw) != 0 {
				t.Fatal("failed preview exposed partial aggregate")
			}
		})
	}
}

func TestOwnerReferencesRejectFractionalNumbersAndPreserveLargeIDs(t *testing.T) {
	id, rev, e := ref(json.RawMessage(`{"id":9007199254740993,"revision":17}`))
	if e != nil || id != "9007199254740993" || rev != 17 {
		t.Fatal("owner reference lost integer precision")
	}
	for _, raw := range []string{`{"id":1.5}`, `{"id":1e30}`, `{"id":-1}`, `{"id":"../../another"}`, `{"id":"owner-session","revision":1.5}`, `{"id":"owner-session","revision":0}`, `{"id":"owner-session","revision":"17"}`} {
		if _, _, e := ref(json.RawMessage(raw)); e == nil {
			t.Fatal("malformed owner reference or revision was accepted")
		}
	}
}

func TestPreviewAndReportMaskOwnerCredentials(t *testing.T) {
	ss := ownerAdapterSession("cards.xlsx")
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"session_id":"owner-session-123","revision":17,"preview":{"cards":[{"row":2,"question":"Synthetic question","password":"fixture-password","nested":{"access_token":"fixture-token","private_key":"fixture-key"}}]},"authorization":"fixture-authorization","credentials":{"password":"fixture-other"}}}`))
	}))
	defer owner.Close()
	s := &Server{HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
	r := httptest.NewRequest("GET", "http://uploads.invalid/session", nil)
	r.Header.Set("Authorization", "Bearer synthetic")
	v, code, e := s.sessionView(r, ss)
	if e != nil || code != 200 {
		t.Fatal("owner preview unavailable")
	}
	encoded, _ := json.Marshal(v)
	for _, secret := range []string{"fixture-password", "fixture-token", "fixture-key", "fixture-authorization", "fixture-other"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("owner secret leaked into preview")
		}
	}
	report := makeReport(ss, json.RawMessage(`{"cards":[{"row":2}]}`), json.RawMessage(`{"result":{"password":"fixture-password","access_token":"fixture-token"},"changed":1}`))
	if bytes.Contains(report, []byte("fixture-password")) || bytes.Contains(report, []byte("fixture-token")) {
		t.Fatal("owner credentials leaked into job report")
	}
}

func TestOwnerErrorBodyAndRedirectDoNotExposeOrForwardCredentials(t *testing.T) {
	var redirected atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.Write([]byte(`{}`)) }))
	defer other.Close()
	for _, code := range []int{403, 503, 303} {
		owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if code == 303 {
				w.Header().Set("Location", other.URL)
			}
			w.WriteHeader(code)
			w.Write([]byte(`{"password":"fixture-private-password","error":"fixture-private-token"}`))
		}))
		s := &Server{HTTP: newHTTP()}
		_, e := s.call(context.Background(), "GET", owner.URL, "/preview", "Bearer fixture-private-token", "", "", nil)
		owner.Close()
		if e == nil || strings.Contains(e.Error(), "fixture-private") {
			t.Fatal("owner failure leaked response details")
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("forwarded authorization followed an owner redirect")
	}
}

// This small driver supplies a persisted coordinator state while HTTP supplies
// owner receipts. It exercises actual recovery code and CAS outcomes without
// touching the live daemon, SQL tables or queues.
type ownerRecoveryMemory struct {
	mu             sync.Mutex
	ss             Session
	events         []string
	receiptWrites  int
	receipt        *ownerAdapterReceipt
	job            *Job
	sessionReadErr error
}
type ownerAdapterReceipt struct{ key, action, hash, id, state string }
type ownerRecoveryConnector struct{ memory *ownerRecoveryMemory }
type ownerRecoveryConn struct{ memory *ownerRecoveryMemory }
type ownerRecoveryDriver struct{}
type ownerRecoveryTx struct{}
type ownerRecoveryRows struct {
	cols []string
	rows [][]driver.Value
}

func (c *ownerRecoveryConnector) Connect(context.Context) (driver.Conn, error) {
	return &ownerRecoveryConn{memory: c.memory}, nil
}
func (c *ownerRecoveryConnector) Driver() driver.Driver { return ownerRecoveryDriver{} }
func (ownerRecoveryDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector required")
}
func (c *ownerRecoveryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements unsupported in fixture")
}
func (c *ownerRecoveryConn) Close() error              { return nil }
func (c *ownerRecoveryConn) Begin() (driver.Tx, error) { return ownerRecoveryTx{}, nil }
func (c *ownerRecoveryConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return ownerRecoveryTx{}, nil
}
func (ownerRecoveryTx) Commit() error          { return nil }
func (ownerRecoveryTx) Rollback() error        { return nil }
func (r *ownerRecoveryRows) Columns() []string { return r.cols }
func (r *ownerRecoveryRows) Close() error      { return nil }
func (r *ownerRecoveryRows) Next(out []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(out, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}
func (c *ownerRecoveryConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.memory.mu.Lock()
	defer c.memory.mu.Unlock()
	ss := c.memory.ss
	if strings.Contains(q, "SELECT MIN(command_id),COUNT(*) FROM uploads_receipts_v1") {
		out := &ownerRecoveryRows{cols: []string{"command_id", "count"}, rows: [][]driver.Value{{nil, int64(0)}}}
		p := c.memory.receipt
		if p != nil && p.action == "session.create" && p.id == ss.ID && len(args) == 4 && args[0].Value == ss.OrganizationID && args[1].Value == ss.DivisionID && args[2].Value == ss.ActorID && args[3].Value == ss.ID {
			out.rows = [][]driver.Value{{p.key, int64(1)}}
		}
		return out, nil
	}
	if strings.Contains(q, "FROM uploads_receipts_v1") {
		out := &ownerRecoveryRows{cols: []string{"action", "request_hash", "resource_id", "state"}}
		p := c.memory.receipt
		if p != nil && len(args) >= 4 && args[0].Value == ss.OrganizationID && args[1].Value == ss.DivisionID && args[2].Value == ss.ActorID && args[3].Value == p.key {
			out.rows = [][]driver.Value{{p.action, p.hash, p.id, p.state}}
		}
		return out, nil
	}
	if strings.Contains(q, "FROM uploads_jobs_v1") {
		j := c.memory.job
		if strings.Contains(q, "SELECT id,type") {
			out := &ownerRecoveryRows{cols: []string{"id", "type"}}
			if j != nil && len(args) >= 3 && args[0].Value == j.OrganizationID && args[1].Value == j.DivisionID && args[2].Value == j.ActorID {
				out.rows = [][]driver.Value{{j.ID, j.Type}}
			}
			return out, nil
		}
		out := &ownerRecoveryRows{cols: strings.Split(jobColumns, ",")}
		if j != nil && len(args) >= 4 && args[0].Value == j.ID && args[1].Value == j.OrganizationID && args[2].Value == j.DivisionID && args[3].Value == j.ActorID {
			out.rows = [][]driver.Value{{j.ID, j.OrganizationID, j.DivisionID, j.ActorID, j.SessionID, j.Type, j.Owner, j.Status, int64(j.Attempt), int64(j.Processed), int64(j.Total), j.Error, []byte(j.Report), j.Created}}
		}
		return out, nil
	}
	if !strings.Contains(q, "FROM uploads_sessions_v1") {
		return nil, errors.New("unexpected recovery read")
	}
	if c.memory.sessionReadErr != nil {
		return nil, c.memory.sessionReadErr
	}
	rows := &ownerRecoveryRows{cols: strings.Split(sessionColumns, ",")}
	if len(args) < 4 || args[0].Value != ss.ID || args[1].Value != ss.OrganizationID || args[2].Value != ss.DivisionID || args[3].Value != ss.ActorID {
		return rows, nil
	}
	rows.rows = [][]driver.Value{{ss.ID, ss.OrganizationID, ss.DivisionID, ss.ActorID, ss.Type, ss.Owner, ss.OwnerSessionID, ss.TargetID, ss.Revision, ss.OwnerRevision, ss.State, ss.FileID, ss.SourceHash, ss.Expires, ss.Created}}
	return rows, nil
}
func (c *ownerRecoveryConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.memory.mu.Lock()
	defer c.memory.mu.Unlock()
	if strings.HasPrefix(q, "INSERT INTO uploads_receipts_v1") {
		if c.memory.receipt != nil {
			return nil, &mysql.MySQLError{Number: 1062}
		}
		if len(args) < 7 {
			return nil, errors.New("invalid fixture receipt")
		}
		c.memory.receipt = &ownerAdapterReceipt{key: args[3].Value.(string), action: args[4].Value.(string), hash: args[5].Value.(string), id: args[6].Value.(string), state: "reserved"}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE uploads_sessions_v1 SET state='preview'") {
		if c.memory.ss.State != "staging" && c.memory.ss.State != "outcome_unknown" {
			return driver.RowsAffected(0), nil
		}
		if len(args) < 3 || args[2].Value != c.memory.ss.ID {
			return driver.RowsAffected(0), nil
		}
		c.memory.ss.State = "preview"
		c.memory.ss.OwnerSessionID, _ = args[0].Value.(string)
		c.memory.ss.OwnerRevision, _ = args[1].Value.(int64)
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE uploads_receipts_v1") {
		c.memory.receiptWrites++
		if c.memory.receipt != nil && len(args) > 0 {
			if state, ok := args[0].Value.(string); ok {
				c.memory.receipt.state = state
			}
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE uploads_sessions_v1 SET state='editing'") {
		c.memory.ss.State = "editing"
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE uploads_sessions_v1 SET state=?,owner_revision=?,revision=revision+") {
		if c.memory.ss.State != "editing" {
			return driver.RowsAffected(0), nil
		}
		if strings.Contains(q, "AND revision=? AND owner_revision=?") {
			if len(args) != 6 {
				return nil, errors.New("invalid guarded edit arguments")
			}
			if args[4].Value != c.memory.ss.Revision || args[5].Value != c.memory.ss.OwnerRevision {
				return driver.RowsAffected(0), nil
			}
		}
		c.memory.ss.State = args[0].Value.(string)
		c.memory.ss.OwnerRevision = args[1].Value.(int64)
		c.memory.ss.Revision += args[2].Value.(int64)
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE uploads_sessions_v1 SET state=? WHERE") {
		if c.memory.ss.State != "staging" && c.memory.ss.State != "outcome_unknown" {
			return driver.RowsAffected(0), nil
		}
		c.memory.ss.State = args[0].Value.(string)
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "INSERT INTO uploads_events_v1") {
		if len(args) > 6 {
			action, _ := args[6].Value.(string)
			c.memory.events = append(c.memory.events, action)
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected recovery write")
}

func TestCredentialRecoveryMissingReceiptNeverResendsInput(t *testing.T) {
	ss := ownerAdapterSession("users.credentials")
	ss.State = "outcome_unknown"
	ss.OwnerSessionID = ""
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || !strings.Contains(r.URL.Path, "/imports/receipts/") {
			t.Error("uncertain credential preview replayed bytes")
		}
		w.WriteHeader(404)
	}))
	defer owner.Close()
	memory := &ownerRecoveryMemory{ss: ss, receipt: &ownerAdapterReceipt{key: "preview-command-123", action: "session.create", id: ss.ID, state: "reserved"}}
	db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
	defer db.Close()
	s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CoreURL: owner.URL}}
	r := httptest.NewRequest("POST", "http://uploads.invalid/preview", strings.NewReader("fixture-private-password"))
	r.Header.Set("Authorization", "Bearer synthetic")
	typeInfo, _ := findType(ss.Type)
	got, status, e := s.recoverCredentialPreview(r, ss, "preview-command-123", typeInfo)
	if e != nil || status != 200 || got.(Session).State != "outcome_unknown" || calls.Load() != 1 {
		t.Fatal("missing owner receipt silently retried a credential import")
	}
}

func TestCredentialRecoveryReturnsPersistedStateAndDoesNotReopenQueuedPreview(t *testing.T) {
	for _, state := range []string{"outcome_unknown", "queued", "awaiting_authorization", "completed"} {
		t.Run(state, func(t *testing.T) {
			stale := ownerAdapterSession("users.credentials")
			stale.State = "outcome_unknown"
			stale.OwnerSessionID = ""
			stale.TargetID = ""
			current := stale
			current.State = state
			if state != "outcome_unknown" {
				current.OwnerSessionID = "23"
			}
			memory := &ownerRecoveryMemory{ss: current, receipt: &ownerAdapterReceipt{key: "preview-command-123", action: "session.create", id: current.ID, state: "reserved"}}
			db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
			defer db.Close()
			var posts atomic.Int64
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					posts.Add(1)
					w.WriteHeader(400)
					return
				}
				if strings.Contains(r.URL.Path, "/imports/receipts/") {
					json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"session_id": 23}})
					return
				}
				if r.URL.Path != "/v1/core/divisions/7/imports/23" {
					w.WriteHeader(404)
					return
				}
				w.Write([]byte(`{"data":{"id":23,"status":"pending","users":[{"full_name":"Synthetic Person","password":"fixture-private-password"}]}}`))
			}))
			defer owner.Close()
			s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CoreURL: owner.URL}}
			r := httptest.NewRequest("POST", "http://uploads.invalid/preview", nil)
			r.Header.Set("Authorization", "Bearer synthetic")
			typeInfo, _ := findType(stale.Type)
			got, _, e := s.recoverCredentialPreview(r, stale, "preview-command-123", typeInfo)
			if e != nil {
				if state == "outcome_unknown" {
					t.Fatal(e)
				}
				assertOwnerAPIError(t, e, 409)
			} else {
				want := state
				if state == "outcome_unknown" {
					want = "preview"
				}
				ss, ok := got.(Session)
				if !ok || ss.State != want {
					t.Fatal("recovered owner response ignored the persisted coordinator lifecycle")
				}
				raw, _ := json.Marshal(got)
				if bytes.Contains(raw, []byte("fixture-private-password")) {
					t.Fatal("recovered preview leaked credentials")
				}
			}
			memory.mu.Lock()
			defer memory.mu.Unlock()
			if state != "outcome_unknown" && memory.ss.State != state {
				t.Fatal("recovery reopened a confirmed or queued preview")
			}
			if state != "outcome_unknown" && len(memory.events) > 0 {
				t.Fatal("lost coordinator CAS produced a false preview recovery event")
			}
			if posts.Load() != 0 {
				t.Fatal("credential recovery reapplied input")
			}
		})
	}
}

func TestScopedSessionLookupRejectsGuessedIDsAfterCabinetSwitch(t *testing.T) {
	ss := ownerAdapterSession("cards.xlsx")
	memory := &ownerRecoveryMemory{ss: ss}
	db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
	defer db.Close()
	if _, e := loadSession(context.Background(), db, ss.Scope, ss.ID, false); e != nil {
		t.Fatal(e)
	}
	for _, other := range []Scope{{OrganizationID: 43, DivisionID: 7, ActorID: 9}, {OrganizationID: 42, DivisionID: 8, ActorID: 9}, {OrganizationID: 42, DivisionID: 7, ActorID: 10}} {
		if _, e := loadSession(context.Background(), db, other, ss.ID, false); !errors.Is(e, sql.ErrNoRows) {
			t.Fatal("guessed session ID crossed actor/organization/division scope")
		}
	}
}

func TestUploadAndSubjectPermissionAreBothRequired(t *testing.T) {
	typeInfo, _ := findType("cards.xlsx")
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "cards": {View: true, Edit: true}}}
	if e := permission(a, typeInfo, true); e == nil {
		t.Fatal("noncanonical cards permission allowed a native import")
	}
	a.Permissions["train"] = Permission{View: true, Edit: true}
	if e := permission(a, typeInfo, true); e != nil {
		t.Fatal("canonical train permission did not allow import")
	}
	delete(a.Permissions, "upload")
	if e := permission(a, typeInfo, true); e == nil {
		t.Fatal("subject permission bypassed upload permission")
	}
	a.Permissions["upload"] = Permission{View: true, Edit: true}
	transform, _ := findType("cards.transform")
	if e := permission(a, transform, true); e == nil {
		t.Fatal("card-to-test transform bypassed tests permission")
	}
}

func TestNativeCardsEditPinsOwnerRevisionAndReplaysWithoutSecondMutation(t *testing.T) {
	ss := ownerAdapterSession("cards.xlsx")
	ss.TargetID = ""
	memory := &ownerRecoveryMemory{ss: ss}
	db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
	defer db.Close()
	var edits atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cards/sessions/owner-session-123" || r.Header.Get("Authorization") != "Bearer synthetic" {
			w.WriteHeader(403)
			return
		}
		if r.Method == "PUT" {
			edits.Add(1)
			var in map[string]any
			if json.NewDecoder(r.Body).Decode(&in) != nil || in["expected_revision"] != float64(17) || r.Header.Get("Idempotency-Key") != ownerCommand("edit", ss.ID, "ui-edit-001") {
				t.Error("edit did not pin the stored owner revision and command")
			}
		} else if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Write([]byte(`{"data":{"session_id":"owner-session-123","revision":18,"state":"preview","preview":{"cards":[]}}}`))
	}))
	defer owner.Close()
	s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "train": {View: true, Edit: true}}}
	input := `{"expected_revision":3,"owner_payload":{"cards":[{"row":2,"question":"Synthetic question","answer":"Synthetic answer","skip":false}]}}`
	for n := 0; n < 2; n++ {
		r := httptest.NewRequest("PUT", "http://uploads.invalid/preview", strings.NewReader(input))
		r.SetPathValue("session", ss.ID)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic")
		r.Header.Set("Idempotency-Key", "ui-edit-001")
		got, code, e := s.edit(r, a, ss.Scope)
		if e != nil || code != 200 {
			t.Fatal("native edit/replay failed", e)
		}
		current, ok := got.(Session)
		if !ok || current.Revision != 4 || current.OwnerRevision != 18 || current.State != "preview" {
			t.Fatal("coordinator lost the accepted edit version")
		}
	}
	if edits.Load() != 1 {
		t.Fatal("same edit command performed a second owner mutation")
	}
}

func TestLateNativeEditResponseCannotOverwriteNewerSessionOrReceipt(t *testing.T) {
	for _, tc := range []struct {
		name          string
		state         string
		revision      int64
		ownerRevision int64
		ownerStatus   int
	}{
		{"reconciled", "preview", 4, 18, 200},
		{"queued_after_reconcile", "queued", 4, 18, 200},
		{"coordinator_version_changed", "editing", 4, 17, 200},
		{"owner_version_changed", "editing", 3, 18, 200},
		{"late_failure_after_reconcile", "preview", 4, 18, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ss := ownerAdapterSession("cards.xlsx")
			memory := &ownerRecoveryMemory{ss: ss}
			db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
			defer db.Close()
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The owner response is delayed across another coordinator outcome.
				memory.mu.Lock()
				memory.ss.State = tc.state
				memory.ss.Revision = tc.revision
				memory.ss.OwnerRevision = tc.ownerRevision
				memory.receipt.state = "completed"
				memory.events = []string{"session.reconciled"}
				memory.mu.Unlock()
				w.WriteHeader(tc.ownerStatus)
				w.Write([]byte(`{"data":{"session_id":"owner-session-123","revision":18,"state":"preview"}}`))
			}))
			defer owner.Close()
			s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
			a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "train": {View: true, Edit: true}}}
			r := httptest.NewRequest("PUT", "http://uploads.invalid/preview", strings.NewReader(`{"expected_revision":3,"owner_payload":{"cards":[]}}`))
			r.SetPathValue("session", ss.ID)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "ui-edit-delayed")
			r.Header.Set("Authorization", "Bearer synthetic")
			got, _, e := s.edit(r, a, ss.Scope)
			assertOwnerAPIError(t, e, 409)
			if got != nil {
				t.Fatal("late edit response confirmed a replaced coordinator outcome")
			}
			memory.mu.Lock()
			defer memory.mu.Unlock()
			if memory.ss.State != tc.state || memory.ss.Revision != tc.revision || memory.ss.OwnerRevision != tc.ownerRevision {
				t.Fatal("late edit response overwrote the newer coordinator state")
			}
			if memory.receipt.state != "completed" || memory.receiptWrites != 0 || len(memory.events) != 1 || memory.events[0] != "session.reconciled" {
				t.Fatal("rejected edit finish changed a receipt or appended a contradictory audit event")
			}
		})
	}
}

func TestNativeEditReturnsPostDurableReloadFailureAndReplaysAcceptedOutcome(t *testing.T) {
	ss := ownerAdapterSession("cards.xlsx")
	memory := &ownerRecoveryMemory{ss: ss}
	db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
	defer db.Close()
	wantErr := errors.New("synthetic post-durable session read failure")
	var edits atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			edits.Add(1)
			memory.mu.Lock()
			memory.sessionReadErr = wantErr
			memory.mu.Unlock()
		}
		w.Write([]byte(`{"data":{"session_id":"owner-session-123","revision":18,"state":"preview","preview":{"cards":[]}}}`))
	}))
	defer owner.Close()
	s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "train": {View: true, Edit: true}}}
	request := func() *http.Request {
		r := httptest.NewRequest("PUT", "http://uploads.invalid/preview", strings.NewReader(`{"expected_revision":3,"owner_payload":{"cards":[]}}`))
		r.SetPathValue("session", ss.ID)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "ui-edit-reload-failure")
		r.Header.Set("Authorization", "Bearer synthetic")
		return r
	}
	got, code, e := s.edit(request(), a, ss.Scope)
	if !errors.Is(e, wantErr) || got != nil || code != 0 {
		t.Fatal("failed final reload looked like a successful empty session", e)
	}
	memory.mu.Lock()
	if memory.ss.State != "preview" || memory.ss.Revision != 4 || memory.ss.OwnerRevision != 18 || memory.receipt.state != "preview" || memory.receiptWrites != 1 || len(memory.events) != 1 {
		memory.mu.Unlock()
		t.Fatal("reload failure lost the accepted durable edit outcome")
	}
	memory.sessionReadErr = nil
	memory.mu.Unlock()
	got, code, e = s.edit(request(), a, ss.Scope)
	if e != nil || code != 200 {
		t.Fatal("same command could not recover its accepted preview", e)
	}
	current, ok := got.(Session)
	if !ok || current.Revision != 4 || current.OwnerRevision != 18 || edits.Load() != 1 {
		t.Fatal("reload retry repeated the owner mutation or lost the durable revision")
	}
}

func TestLateOwnerCreateFailureCannotDowngradeRecoveredReceiptOrJournal(t *testing.T) {
	ss := ownerAdapterSession("cards.transform")
	ss.TargetID = ""
	ss.OwnerSessionID = ""
	ss.State = "outcome_unknown"
	payload := `{"theme_id":11}`
	memory := &ownerRecoveryMemory{ss: ss, receipt: &ownerAdapterReceipt{key: "ui-create-001", action: "session.create", hash: digest([]byte("cards.transform:" + payload)), id: ss.ID, state: "reserved"}}
	db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
	defer db.Close()
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A concurrent successful recovery has already confirmed the same owner
		// command and queued the coordinator job before this older response fails.
		memory.mu.Lock()
		memory.ss.State = "queued"
		memory.ss.OwnerSessionID = "owner-session-123"
		memory.receipt.state = "completed"
		memory.mu.Unlock()
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"simulated late dependency failure"}`))
	}))
	defer owner.Close()
	s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "train": {View: true, Edit: true}, "tests": {View: true, Edit: true}}}
	r := httptest.NewRequest("POST", "http://uploads.invalid/preview", strings.NewReader(`{"type":"cards.transform","owner_payload":`+payload+`}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "ui-create-001")
	r.Header.Set("Authorization", "Bearer synthetic")
	_, _, e := s.create(r, a, ss.Scope)
	if e != nil {
		assertOwnerAPIError(t, e, 503)
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.ss.State != "queued" || memory.receipt.state != "completed" || len(memory.events) != 0 {
		t.Fatal("late owner failure contradicted a recovered coordinator outcome")
	}
}

func TestNativeEditRejectsInvalidOwnerResponseWithoutConfirmingNewVersion(t *testing.T) {
	for _, body := range []string{
		`{"data":{}}`,
		`{"data":{"session_id":"owner-session-123","state":"preview"}}`,
		`{"data":{"session_id":"owner-session-123","revision":18.5,"state":"preview"}}`,
		`{"data":{"session_id":"owner-session-123","revision":"18","state":"preview"}}`,
		`{"data":{"session_id":"owner-session-123","revision":null,"state":"preview"}}`,
		`{"data":{"session_id":"another-owner-session","revision":18,"state":"preview"}}`,
		`{"data":{"session_id":"owner-session-123","revision":1,"state":"preview"}}`,
		`{"data":{"session_id":"owner-session-123","revision":17,"state":"preview"}}`,
		`{"data":{"session_id":"owner-session-123","revision":19,"state":"preview"}}`,
		`{"data":{"session_id":"owner-session-123","revision":18,"state":"committed"}}`,
		`{"data":{"session_id":"owner-session-123","revision":18,"state":false}}`,
		`{"data":{"session_id":"owner-session-123","revision":18,"state":"editable"}}`,
	} {
		t.Run(digest([]byte(body))[:8], func(t *testing.T) {
			ss := ownerAdapterSession("cards.xlsx")
			ss.TargetID = ""
			memory := &ownerRecoveryMemory{ss: ss}
			db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
			defer db.Close()
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer owner.Close()
			s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
			a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "train": {View: true, Edit: true}}}
			r := httptest.NewRequest("PUT", "http://uploads.invalid/preview", strings.NewReader(`{"expected_revision":3,"owner_payload":{"cards":[]}}`))
			r.SetPathValue("session", ss.ID)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "ui-edit-invalid-response")
			r.Header.Set("Authorization", "Bearer synthetic")
			got, _, e := s.edit(r, a, ss.Scope)
			assertOwnerAPIError(t, e, 503)
			if got != nil {
				t.Fatal("malformed edit response looked confirmed")
			}
			memory.mu.Lock()
			defer memory.mu.Unlock()
			if memory.ss.State != "edit_unknown" || memory.ss.Revision != 3 || memory.ss.OwnerRevision != 17 {
				t.Fatal("invalid owner edit response confirmed or rewound a preview version")
			}
		})
	}
}

func TestNativeExamsEditAcceptsOwnerIDAndEditableState(t *testing.T) {
	ss := ownerAdapterSession("exams.questions")
	memory := &ownerRecoveryMemory{ss: ss}
	db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
	defer db.Close()
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/exams/83/imports/questions/owner-session-123" {
			t.Error("exams edit used a different native owner route")
		}
		if r.Method == "PUT" {
			var in struct {
				ExpectedRevision int64 `json:"expected_revision"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.ExpectedRevision != 17 {
				t.Error("exams edit did not pin the accepted owner version")
			}
		} else if r.Method != "GET" || r.URL.Query().Get("limit") != "200" || r.URL.Query().Get("offset") != "0" {
			t.Error("exams edit did not reload through the native page adapter")
		}
		w.Write([]byte(`{"data":{"id":"owner-session-123","revision":18,"state":"editable","rows":[],"total":0}}`))
	}))
	defer owner.Close()
	s := &Server{DB: db, HTTP: newHTTP(), Config: Config{ExamsURL: owner.URL}}
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "exams": {View: true, Edit: true}}}
	r := httptest.NewRequest("PUT", "http://uploads.invalid/preview", strings.NewReader(`{"expected_revision":3,"owner_payload":{"rows":[]}}`))
	r.SetPathValue("session", ss.ID)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "ui-exams-edit-001")
	r.Header.Set("Authorization", "Bearer synthetic")
	got, code, e := s.edit(r, a, ss.Scope)
	if e != nil || code != 200 {
		t.Fatal("native exams edit rejected its successful preview DTO", e)
	}
	current, ok := got.(Session)
	if !ok || current.Revision != 4 || current.OwnerRevision != 18 || current.State != "preview" {
		t.Fatal("successful exams edit lost the confirmed version")
	}
}

func TestNativeExamsEditReturnsCompleteAcceptedPreview(t *testing.T) {
	for _, tc := range []struct {
		name              string
		changedAllPages   bool
		changedSecondPage bool
	}{
		{name: "all_237_accepted_rows"},
		{name: "version_changed_on_last_page", changedSecondPage: true},
		{name: "version_advanced_after_accepted_mutation", changedAllPages: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ss := ownerAdapterSession("exams.questions")
			accepted := ss
			accepted.OwnerRevision++
			memory := &ownerRecoveryMemory{ss: ss}
			db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
			defer db.Close()
			page := func(offset int) map[string]any {
				p := examAdapterPage(accepted, offset, 237)
				p["summary"].(map[string]any)["excluded_rows"] = 1
				if offset == 200 {
					rows := p["rows"].([]any)
					last := rows[len(rows)-1].(map[string]any)
					last["excluded"] = true
					last["errors"] = []string{"synthetic_last_page_error"}
					p["error_report"] = append(p["error_report"].([]any), map[string]any{"row_id": "row-0236", "source_row": 238, "errors": []string{"synthetic_last_page_error"}})
				}
				return p
			}
			var puts, gets atomic.Int64
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/exams/83/imports/questions/owner-session-123" || r.Header.Get("Authorization") != "Bearer synthetic" {
					w.WriteHeader(403)
					return
				}
				if r.Method == "PUT" {
					puts.Add(1)
					var in struct {
						ExpectedRevision int64 `json:"expected_revision"`
						Rows             []struct {
							RowID    string `json:"row_id"`
							Excluded bool   `json:"excluded"`
						} `json:"rows"`
					}
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.ExpectedRevision != 17 || len(in.Rows) != 237 || in.Rows[236].RowID != "row-0236" || !in.Rows[236].Excluded {
						t.Error("edit did not send the complete input and pinned owner version")
					}
					// The native mutation confirms revision 18 but returns its first page.
					json.NewEncoder(w).Encode(map[string]any{"data": page(0)})
					return
				}
				if r.Method != "GET" || r.URL.Query().Get("limit") != "200" {
					w.WriteHeader(405)
					return
				}
				offset, e := strconv.Atoi(r.URL.Query().Get("offset"))
				if e != nil || offset != 0 && offset != 200 {
					w.WriteHeader(400)
					return
				}
				gets.Add(1)
				p := page(offset)
				if tc.changedAllPages || tc.changedSecondPage && offset == 200 {
					p["revision"] = int64(19)
				}
				json.NewEncoder(w).Encode(map[string]any{"data": p})
			}))
			defer owner.Close()
			s := &Server{DB: db, HTTP: newHTTP(), Config: Config{ExamsURL: owner.URL}}
			a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true, Edit: true}, "exams": {View: true, Edit: true}}}
			rows := make([]map[string]any, 237)
			for n := range rows {
				rows[n] = map[string]any{"row_id": fmt.Sprintf("row-%04d", n), "excluded": n == 236}
			}
			ownerPayload, _ := json.Marshal(map[string]any{"rows": rows})
			input, _ := json.Marshal(EditDTO{ExpectedRevision: 3, OwnerPayload: ownerPayload})
			r := httptest.NewRequest("PUT", "http://uploads.invalid/preview", bytes.NewReader(input))
			r.SetPathValue("session", ss.ID)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "ui-edit-paged-exams")
			r.Header.Set("Authorization", "Bearer synthetic")
			got, code, e := s.edit(r, a, ss.Scope)
			if puts.Load() != 1 || gets.Load() != 2 {
				t.Fatal("edit response did not reload every native page exactly once")
			}
			if tc.changedSecondPage || tc.changedAllPages {
				assertOwnerAPIError(t, e, 409)
				if got != nil {
					t.Fatal("changed owner revision exposed a partial edit preview")
				}
				memory.mu.Lock()
				defer memory.mu.Unlock()
				if memory.ss.State != "preview" || memory.ss.Revision != 4 || memory.ss.OwnerRevision != 18 || memory.receipt.state != "preview" || memory.receiptWrites != 1 || len(memory.events) != 1 {
					t.Fatal("post-edit read conflict downgraded the accepted durable outcome")
				}
				return
			}
			if e != nil || code != 200 {
				t.Fatal("complete accepted edit preview unavailable", e)
			}
			current, ok := got.(Session)
			if !ok || current.Revision != 4 || current.OwnerRevision != 18 {
				t.Fatal("full edit preview lost the durable accepted versions")
			}
			var preview struct {
				Revision int64 `json:"revision"`
				Total    int   `json:"total"`
				Rows     []struct {
					RowID    string   `json:"row_id"`
					Excluded bool     `json:"excluded"`
					Errors   []string `json:"errors"`
				} `json:"rows"`
				ErrorReport []struct {
					SourceRow int `json:"source_row"`
				} `json:"error_report"`
				HasMore    bool `json:"has_more"`
				NextOffset *int `json:"next_offset"`
			}
			if json.Unmarshal(current.Preview, &preview) != nil || preview.Revision != current.OwnerRevision || preview.Total != 237 || len(preview.Rows) != 237 {
				t.Fatal("edit returned the native first page or mismatched its accepted revision")
			}
			for n, row := range preview.Rows {
				if row.RowID != fmt.Sprintf("row-%04d", n) {
					t.Fatal("full edit preview lost source order or duplicated rows")
				}
			}
			if !preview.Rows[236].Excluded || len(preview.Rows[236].Errors) != 1 || preview.Rows[236].Errors[0] != "synthetic_last_page_error" || len(preview.ErrorReport) != 2 || preview.ErrorReport[1].SourceRow != 238 {
				t.Fatal("last-page exclusion or owner validation errors were dropped after edit")
			}
			if preview.HasMore || preview.NextOffset != nil {
				t.Fatal("complete edit preview still advertises an unread page")
			}
		})
	}
}

func TestJobReportRechecksOwnerAccessAndJournalContainsMetadataOnly(t *testing.T) {
	ss := ownerAdapterSession("cards.xlsx")
	j := Job{ID: strings.Repeat("b", 32), Scope: ss.Scope, SessionID: ss.ID, Type: ss.Type, Owner: ss.Owner, Status: "completed", Report: json.RawMessage(`{"rows":[{"source_row":2,"private_owner_detail":"synthetic-private-report"}]}`)}
	memory := &ownerRecoveryMemory{ss: ss, job: &j}
	db := sql.OpenDB(&ownerRecoveryConnector{memory: memory})
	defer db.Close()
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"direction access revoked"}`))
	}))
	defer owner.Close()
	s := &Server{DB: db, HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"upload": {View: true}, "train": {View: true}}}
	r := httptest.NewRequest("GET", "http://uploads.invalid/report", nil)
	r.SetPathValue("job", j.ID)
	r.SetPathValue("report", "report")
	r.Header.Set("Authorization", "Bearer synthetic")
	got, _, e := s.jobGet(r, a, ss.Scope)
	assertOwnerAPIError(t, e, 403)
	if got != nil || calls.Load() != 1 {
		t.Fatal("revoked owner rights exposed the stored report")
	}
	r = httptest.NewRequest("GET", "http://uploads.invalid/jobs?limit=10", nil)
	out, code, e := s.listJobs(r, a, ss.Scope)
	if e != nil || code != 200 {
		t.Fatal("metadata journal unavailable")
	}
	raw, _ := json.Marshal(out)
	if bytes.Contains(raw, []byte("synthetic-private-report")) || bytes.Contains(raw, []byte("private_owner_detail")) {
		t.Fatal("journal embedded a report without its live owner check")
	}
	if calls.Load() != 1 {
		t.Fatal("metadata journal unexpectedly invoked an owner process")
	}
}
