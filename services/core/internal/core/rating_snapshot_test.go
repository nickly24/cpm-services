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
	"testing"
)

func TestRatingSnapshotRequestBounds(t *testing.T) {
	base := RatingSnapshotRequest{OrganizationID: 1, DivisionID: 2, DateFrom: "2026-01-01", DateTo: "2027-01-01", AsOf: "2026-10-08T00:00:00Z"}
	if e := base.validate(); e != nil {
		t.Fatal("366 inclusive calendar days should be accepted:", e)
	}
	for _, mutate := range []func(*RatingSnapshotRequest){
		func(x *RatingSnapshotRequest) { x.OrganizationID = 0 },
		func(x *RatingSnapshotRequest) { x.DivisionID = -2 },
		func(x *RatingSnapshotRequest) { x.DateFrom = "2026-02-29" },
		func(x *RatingSnapshotRequest) { x.DateTo = "2027-01-02" },
		func(x *RatingSnapshotRequest) { x.DateTo = "2025-01-01" },
		func(x *RatingSnapshotRequest) { x.AsOf = "2026-10-08T03:00:00+03:00" },
		func(x *RatingSnapshotRequest) { x.AsOf = "2026-10-08" },
		func(x *RatingSnapshotRequest) { x.DateFrom = "0000-01-01"; x.DateTo = "0000-01-02" },
	} {
		in := base
		mutate(&in)
		if e := in.validate(); e == nil {
			t.Fatalf("invalid request accepted: %+v", in)
		}
	}
}

func TestRatingSnapshotNeverAcceptsPublicBearer(t *testing.T) {
	s := &Server{InternalKey: strings.Repeat("k", 32)}
	for _, key := range []string{"", "wrong", strings.Repeat("w", 32)} {
		r := httptest.NewRequest("POST", "/internal/core/ratings/snapshot", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer user-token")
		r.Header.Set("X-Service-Key", key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("public token accepted: %d", w.Code)
		}
	}
}

func TestRatingSnapshotStrictScopeDTO(t *testing.T) {
	s := &Server{InternalKey: strings.Repeat("k", 32)}
	for _, body := range []string{`{"organization_id":1,"division_id":2,"date_from":"2026-01-01","date_to":"2026-12-31","as_of":"2026-10-08T00:00:00Z","account_id":99}`, `null`, `{} {}`} {
		r := httptest.NewRequest("POST", "/internal/core/ratings/snapshot", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", s.InternalKey)
		w := httptest.NewRecorder()
		s.ratingSnapshot(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid DTO %s accepted: %d", body, w.Code)
		}
	}
}

// This driver verifies the boundary and read consistency without requiring or
// mutating a database. Integration checks against dev validate actual SQL.
type ratingSnapshotQuery struct {
	contains []string
	columns  []string
	rows     [][]driver.Value
	args     []int64
}
type ratingSnapshotConnector struct {
	queries   []ratingSnapshotQuery
	readOnly  bool
	isolation driver.IsolationLevel
	committed bool
}
type ratingSnapshotConn struct{ c *ratingSnapshotConnector }
type ratingSnapshotDriver struct{}
type ratingSnapshotTx struct{ c *ratingSnapshotConnector }
type ratingSnapshotRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (c *ratingSnapshotConnector) Connect(context.Context) (driver.Conn, error) {
	return &ratingSnapshotConn{c}, nil
}
func (*ratingSnapshotConnector) Driver() driver.Driver { return ratingSnapshotDriver{} }
func (ratingSnapshotDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("connector required")
}
func (*ratingSnapshotConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*ratingSnapshotConn) Close() error                        { return nil }
func (c *ratingSnapshotConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("explicit transaction options required")
}
func (c *ratingSnapshotConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	c.c.readOnly, c.c.isolation = o.ReadOnly, o.Isolation
	return &ratingSnapshotTx{c.c}, nil
}
func (c *ratingSnapshotConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if len(c.c.queries) == 0 {
		return nil, fmt.Errorf("unexpected query")
	}
	want := c.c.queries[0]
	c.c.queries = c.c.queries[1:]
	for _, fragment := range want.contains {
		if !strings.Contains(q, fragment) {
			return nil, fmt.Errorf("required query boundary absent: %s", fragment)
		}
	}
	if len(args) != len(want.args) {
		return nil, fmt.Errorf("wrong scope argument count")
	}
	for i, a := range args {
		if a.Value != want.args[i] {
			return nil, fmt.Errorf("wrong scope argument %d", i)
		}
	}
	return &ratingSnapshotRows{cols: want.columns, rows: want.rows}, nil
}
func (t *ratingSnapshotTx) Commit() error       { t.c.committed = true; return nil }
func (*ratingSnapshotTx) Rollback() error       { return nil }
func (r *ratingSnapshotRows) Columns() []string { return r.cols }
func (*ratingSnapshotRows) Close() error        { return nil }
func (r *ratingSnapshotRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func TestRatingSnapshotIncludesInactiveStudentsAndStableHistoricalNames(t *testing.T) {
	connector := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{
		{contains: []string{"d.organization_id=? AND d.id=?", "d.is_active=1 AND o.is_active=1"}, columns: []string{"id"}, rows: [][]driver.Value{{int64(2)}}, args: []int64{1, 2}},
		{contains: []string{"a.role='student'", "m.organization_id=? AND m.division_id=?", "ORDER BY a.id LIMIT 2001"}, columns: []string{"account", "profile", "name", "class", "group", "group_name", "school", "school_name", "school_short", "active"}, rows: [][]driver.Value{
			{int64(10), int64(21), "DEV Active", int64(7), int64(4), "DEV G", int64(5), "DEV School", "DEV S", int64(1)},
			{int64(11), int64(22), "DEV Blocked", int64(8), nil, nil, nil, nil, nil, int64(0)},
		}, args: []int64{1, 2}},
		{contains: []string{"x.organization_id=? ORDER BY d.id LIMIT 2001"}, columns: []string{"id", "name"}, rows: [][]driver.Value{{int64(8), "Historical direction"}}, args: []int64{1}},
	}}
	db := sql.OpenDB(connector)
	defer db.Close()
	s := &Server{DB: db, InternalKey: strings.Repeat("k", 32)}
	r := httptest.NewRequest("POST", "/internal/core/ratings/snapshot", strings.NewReader(`{"organization_id":1,"division_id":2,"date_from":"2026-01-01","date_to":"2026-12-31","as_of":"2026-10-08T00:00:00Z"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", s.InternalKey)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !connector.readOnly || connector.isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !connector.committed || len(connector.queries) != 0 {
		t.Fatal("snapshot did not use one committed repeatable read transaction")
	}
	var response struct {
		Data RatingCoreSnapshot `json:"data"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	out := response.Data
	if len(out.Accounts) != 2 || !out.Accounts[0].IsActive || out.Accounts[1].IsActive || out.Accounts[1].GroupID != nil || out.Accounts[0].AccountID == out.Accounts[0].ProfileID {
		t.Fatal("inactive history, null values or account/profile identities lost")
	}
	if len(out.InputHash) != 64 || len(out.Directions) != 1 {
		t.Fatal("incomplete snapshot")
	}
	repeated, _ := out.fingerprint()
	if repeated != out.InputHash {
		t.Fatal("hash is not stable")
	}
	out.Accounts[1].IsActive = true
	changed, _ := out.fingerprint()
	if changed == out.InputHash {
		t.Fatal("account changes were omitted from the fingerprint")
	}
}
