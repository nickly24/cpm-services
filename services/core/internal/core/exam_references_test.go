package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func examReferencesRequest(s *Server, body, key, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/internal/core/exam-references", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-Service-Key", key)
	r.Header.Set("Authorization", "Bearer ordinary-user-token")
	r.Header.Set("X-Organization-ID", "999")
	w := httptest.NewRecorder()
	s.examReferences(w, r)
	return w
}

func TestExamReferencesRequireOnlyServiceKey(t *testing.T) {
	for _, configured := range []string{"", "too-short", strings.Repeat("k", 32)} {
		for _, provided := range []string{"", "wrong", strings.Repeat("x", 32)} {
			s := &Server{InternalKey: configured}
			if w := examReferencesRequest(s, `{"organization_id":1}`, provided, "application/json"); w.Code != 401 {
				t.Fatalf("configured=%d provided=%d status=%d", len(configured), len(provided), w.Code)
			}
		}
	}
}

func TestExamReferencesStrictDTOBeforeDatabaseAccess(t *testing.T) {
	key := strings.Repeat("k", 32)
	s := &Server{InternalKey: key}
	for _, body := range []string{
		`{}`, `{"organization_id":0}`, `{"organization_id":1,"direction_id":0}`,
		`{"organization_id":1,"division_ids":[1,1]}`, `{"organization_id":1,"division_ids":[-1]}`,
		`{"organization_id":1,"account_ids":[1,1]}`, `{"organization_id":1,"account_ids":[0]}`,
		`{"organization_id":1,"logins":["12","12"]}`, `{"organization_id":1,"logins":[12]}`,
		`{"organization_id":1,"logins":[" a"]}`, `{"organization_id":1,"logins":[""]}`,
		`{"organization_id":1,"role":"admin"}`, `{"organization_id":1} {}`,
		`{"organization_id":1,"account_ids":[1.5]}`, `[]`, `null`,
	} {
		if w := examReferencesRequest(s, body, key, "application/json"); w.Code != 400 {
			t.Fatalf("accepted invalid DTO %s: %d", body, w.Code)
		}
	}
	if w := examReferencesRequest(s, `{"organization_id":1}`, key, "text/plain"); w.Code != 415 {
		t.Fatalf("accepted non-JSON body: %d", w.Code)
	}
	for _, in := range []examReferencesDTO{
		{OrganizationID: 1, DivisionIDs: make([]int64, 101)},
		{OrganizationID: 1, AccountIDs: make([]int64, 1001)},
		{OrganizationID: 1, Logins: []string{strings.Repeat("x", 51)}},
		{OrganizationID: 1, Logins: []string{"abc\x00def"}},
	} {
		if in.validate() == nil {
			t.Fatal("unbounded or invalid references accepted")
		}
	}
	if err := (examReferencesDTO{OrganizationID: 1, AccountIDs: []int64{12}, Logins: []string{"12", "0012"}}).validate(); err != nil {
		t.Fatalf("numeric logins were coerced into account IDs: %v", err)
	}
}

// This driver exercises SQL boundary semantics without new dependencies or a
// database fixture. All queries must use the same read-only repeatable-read tx.
type examReferenceStep struct {
	contains string
	columns  []string
	rows     [][]driver.Value
}
type examReferenceDriver struct{ conn *examReferenceConn }
type examReferenceConn struct {
	t          *testing.T
	steps      []examReferenceStep
	next       int
	active     bool
	committed  bool
	rolledBack bool
}
type examReferenceTx struct{ conn *examReferenceConn }
type examReferenceRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (d examReferenceDriver) Open(string) (driver.Conn, error) { return d.conn, nil }
func (c *examReferenceConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statement not supported by test driver")
}
func (c *examReferenceConn) Close() error { return nil }
func (c *examReferenceConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transaction options required")
}
func (c *examReferenceConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if !opts.ReadOnly || opts.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
		c.t.Errorf("wrong reference transaction options: %+v", opts)
	}
	c.active = true
	return examReferenceTx{c}, nil
}
func (c *examReferenceConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !c.active {
		c.t.Error("query ran outside reference transaction")
	}
	if c.next >= len(c.steps) {
		return nil, fmt.Errorf("unexpected reference query")
	}
	step := c.steps[c.next]
	c.next++
	if !strings.Contains(query, step.contains) {
		c.t.Errorf("reference query did not include %q", step.contains)
	}
	return &examReferenceRows{columns: step.columns, rows: step.rows}, nil
}
func (tx examReferenceTx) Commit() error {
	tx.conn.active, tx.conn.committed = false, true
	return nil
}
func (tx examReferenceTx) Rollback() error {
	tx.conn.active, tx.conn.rolledBack = false, true
	return nil
}
func (r *examReferenceRows) Columns() []string { return r.columns }
func (r *examReferenceRows) Close() error      { return nil }
func (r *examReferenceRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

var examReferenceDriverID atomic.Uint64

func examReferencesDB(t *testing.T, steps []examReferenceStep) (*sql.DB, *examReferenceConn) {
	t.Helper()
	conn := &examReferenceConn{t: t, steps: steps}
	name := fmt.Sprintf("core-exam-reference-%d", examReferenceDriverID.Add(1))
	sql.Register(name, examReferenceDriver{conn})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, conn
}

func TestExamReferencesUnionIdentityAndDivisionMembership(t *testing.T) {
	steps := []examReferenceStep{
		{"organizations WHERE id=? AND is_active=1", []string{"id"}, [][]driver.Value{{int64(1)}}},
		{"organization_id=? AND is_active=1", []string{"id", "name", "is_active"}, [][]driver.Value{{int64(10), "A", int64(1)}}},
		{"organization_id=? AND is_active=1", []string{"id", "name", "is_active"}, [][]driver.Value{{int64(11), "B", int64(1)}}},
		{"x.organization_id=? AND x.is_active=1", []string{"id", "name", "is_active"}, [][]driver.Value{{int64(50), "Math", int64(1)}}},
		{"BINARY a.username IN", []string{"account_id", "profile_id", "role", "login", "full_name", "is_active"}, [][]driver.Value{
			{int64(12), int64(99), "student", "0012", "DEV Student", int64(1)},
			{int64(13), int64(100), "examinator", "12", "DEV Examiner", int64(1)},
		}},
		{"d.is_active=1", []string{"account_id", "division_id"}, [][]driver.Value{{int64(12), int64(10)}, {int64(13), int64(11)}}},
	}
	db, conn := examReferencesDB(t, steps)
	key := strings.Repeat("k", 32)
	w := examReferencesRequest(&Server{DB: db, InternalKey: key}, `{"organization_id":1,"division_ids":[10,11],"direction_id":50,"account_ids":[12],"logins":["0012","12"]}`, key, "application/json")
	if w.Code != 200 || !conn.committed || conn.next != len(steps) {
		t.Fatalf("reference projection failed status=%d committed=%v queries=%d: %s", w.Code, conn.committed, conn.next, w.Body.String())
	}
	var result struct {
		Data struct {
			OrganizationID int64 `json:"organization_id"`
			Members        []struct {
				AccountID   int64   `json:"account_id"`
				ProfileID   int64   `json:"profile_id"`
				Login       string  `json:"login"`
				Active      bool    `json:"is_active"`
				DivisionIDs []int64 `json:"division_ids"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.OrganizationID != 1 || len(result.Data.Members) != 2 || result.Data.Members[0].AccountID != 12 || result.Data.Members[0].ProfileID != 99 || result.Data.Members[0].Login != "0012" || !result.Data.Members[0].Active || len(result.Data.Members[0].DivisionIDs) != 1 || result.Data.Members[0].DivisionIDs[0] != 10 {
		t.Fatalf("lost identity or scope distinctions: %+v", result.Data)
	}
}

func TestExamReferencesFailClosedOnMissingIdentityOrScope(t *testing.T) {
	key := strings.Repeat("k", 32)
	for _, test := range []struct {
		name  string
		body  string
		steps []examReferenceStep
	}{
		{"organization", `{"organization_id":2}`, []examReferenceStep{{"organizations", []string{"id"}, nil}}},
		{"division", `{"organization_id":1,"division_ids":[99]}`, []examReferenceStep{{"organizations", []string{"id"}, [][]driver.Value{{int64(1)}}}, {"organization_id=? AND is_active=1", []string{"id"}, nil}}},
		{"inactive_or_foreign_account", `{"organization_id":1,"account_ids":[99]}`, []examReferenceStep{{"organizations", []string{"id"}, [][]driver.Value{{int64(1)}}}, {"core_direction_scope", []string{"id"}, nil}, {"ast.is_active=1", []string{"account_id", "login"}, nil}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, conn := examReferencesDB(t, test.steps)
			w := examReferencesRequest(&Server{DB: db, InternalKey: key}, test.body, key, "application/json")
			if w.Code != 404 || conn.committed || !conn.rolledBack {
				t.Fatalf("missing scope/identity accepted status=%d committed=%v rollback=%v", w.Code, conn.committed, conn.rolledBack)
			}
		})
	}
}
