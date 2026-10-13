package homework

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRatingSnapshotRequestBounds(t *testing.T) {
	base := RatingSnapshotRequest{OrganizationID: 1, DivisionID: 2, DateFrom: "2026-01-01", DateTo: "2027-01-01", AsOf: "2026-10-08T00:00:00Z"}
	if _, _, _, e := base.validate(); e != nil {
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
	} {
		in := base
		mutate(&in)
		if _, _, _, e := in.validate(); e == nil {
			t.Fatalf("invalid request accepted: %+v", in)
		}
	}
}

func TestRatingSnapshotRejectsWorkerAndPublicCredentials(t *testing.T) {
	s := &Server{Config: Config{CoreKey: strings.Repeat("c", 32), Key: strings.Repeat("w", 32)}}
	for _, key := range []string{"", "wrong", s.Config.Key} {
		r := httptest.NewRequest("POST", "/internal/homework/ratings/snapshot", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer ordinary-user")
		r.Header.Set("X-Service-Key", key)
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("grade data exposed to worker/user credential: %d", w.Code)
		}
	}
}

func TestRatingSnapshotStrictDTO(t *testing.T) {
	s := &Server{Config: Config{CoreKey: strings.Repeat("c", 32)}}
	for _, body := range []string{`{"organization_id":1,"division_id":2,"date_from":"2026-01-01","date_to":"2026-12-31","as_of":"2026-10-08T00:00:00Z","account_id":99}`, `null`, `{} {}`} {
		r := httptest.NewRequest("POST", "/internal/homework/ratings/snapshot", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", s.Config.CoreKey)
		w := httptest.NewRecorder()
		s.ratingSnapshot(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid request accepted: %d", w.Code)
		}
	}
}

func TestRatingScopeRejectsForeignInactiveAndUnavailableCore(t *testing.T) {
	for _, c := range []struct {
		name, body string
		code, want int
	}{
		{"active", `{"status":true,"data":{"organization":{"id":1},"divisions":[{"id":2,"is_active":true}]}}`, 200, 0},
		{"foreign org", `{"status":true,"data":{"organization":{"id":9},"divisions":[{"id":2,"is_active":true}]}}`, 200, 503},
		{"foreign division", `{"status":true,"data":{"organization":{"id":1},"divisions":[{"id":9,"is_active":true}]}}`, 200, 503},
		{"inactive division", `{"status":true,"data":{"organization":{"id":1},"divisions":[{"id":2,"is_active":false}]}}`, 200, 404},
		{"missing", `{}`, 404, 404},
		{"down", `{}`, 503, 503},
		{"malformed", `{`, 200, 503},
	} {
		t.Run(c.name, func(t *testing.T) {
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/core/test-references" || r.Header.Get("X-Service-Key") != strings.Repeat("c", 32) || r.Header.Get("Authorization") != "" {
					t.Error("wrong internal reference provenance")
				}
				w.WriteHeader(c.code)
				w.Write([]byte(c.body))
			}))
			defer core.Close()
			d := &Dependencies{CoreURL: core.URL, Key: strings.Repeat("c", 32), HTTP: core.Client()}
			e := d.ratingScope(context.Background(), 1, 2)
			if c.want == 0 && e != nil || c.want != 0 && (e == nil || asError(e).Code != c.want) {
				t.Fatalf("expected %d, got %v", c.want, e)
			}
		})
	}
}

func TestRatingHomeworkMidnightTransition(t *testing.T) {
	assignments := []RatingHomeworkAssignment{
		{AssignmentID: 1, TypeName: "OTHER", Deadline: "2026-10-07"},
		{AssignmentID: 2, TypeName: "ОВ", Deadline: "2026-10-08"},
		{AssignmentID: 3, TypeName: "ОВ", Deadline: "2026-10-09"},
	}
	for _, c := range []struct{ at, want string }{
		{"2026-10-08T20:59:59Z", "2026-10-08T21:00:00Z"},
		{"2026-10-08T21:00:00Z", "2026-10-09T21:00:00Z"},
		{"2026-10-09T21:00:00Z", ""},
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		next := ratingHomeworkTransition(assignments, at)
		if c.want == "" && next != nil || c.want != "" && (next == nil || *next != c.want) {
			t.Fatalf("at %s: wrong deadline inclusion transition %v", c.at, next)
		}
	}
}

type ratingSnapshotQuery struct {
	contains []string
	columns  []string
	rows     [][]driver.Value
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
func (*ratingSnapshotConn) Begin() (driver.Tx, error) {
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
			return nil, fmt.Errorf("missing query boundary: %s", fragment)
		}
	}
	if strings.Contains(q, "published=") || len(args) != 4 || args[0].Value != int64(1) || args[1].Value != int64(2) {
		return nil, fmt.Errorf("wrong scope or publication changed legacy eligibility")
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

func TestRatingHomeworkSnapshotPreservesNullGradesAndHash(t *testing.T) {
	deadline := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	connector := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{
		{contains: []string{"h.organization_id=? AND h.division_id=?", "h.deleted_at IS NULL", "ORDER BY h.id LIMIT 2001"}, columns: []string{"id", "name", "type", "deadline"}, rows: [][]driver.Value{{int64(3), "DEV OV", "ОВ", deadline}, {int64(4), "DEV Other", "OTHER", deadline}}},
		{contains: []string{"h.organization_id=? AND h.division_id=?", "ORDER BY r.assignment_id,r.account_id LIMIT 100001"}, columns: []string{"assignment", "account", "status", "result", "date_pass"}, rows: [][]driver.Value{{int64(3), int64(10), int64(1), int64(90), deadline}, {int64(3), int64(11), int64(0), nil, nil}}},
	}}
	db := sql.OpenDB(connector)
	defer db.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":true,"data":{"organization":{"id":1},"divisions":[{"id":2,"is_active":true}]}}`))
	}))
	defer core.Close()
	s := &Server{DB: db, Config: Config{CoreKey: strings.Repeat("c", 32)}, Deps: &Dependencies{CoreURL: core.URL, Key: strings.Repeat("c", 32), HTTP: core.Client()}}
	r := httptest.NewRequest("POST", "/internal/homework/ratings/snapshot", strings.NewReader(`{"organization_id":1,"division_id":2,"date_from":"2026-01-01","date_to":"2026-12-31","as_of":"2026-10-08T00:00:00Z"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", s.Config.CoreKey)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !connector.readOnly || connector.isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !connector.committed || len(connector.queries) != 0 {
		t.Fatal("snapshot did not use one committed repeatable read transaction")
	}
	var response struct {
		Data RatingHomeworkSnapshot `json:"data"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	out := response.Data
	if len(out.Assignments) != 2 || len(out.Assignments[0].Results) != 2 || out.Assignments[0].Results[1].Result != nil || out.Assignments[0].Results[1].DatePass != nil || *out.Assignments[0].Results[0].Result != 90 {
		t.Fatal("snapshot changed eligibility or replaced a null grade with zero")
	}
	if out.NextTransitionAt == nil || *out.NextTransitionAt != "2026-10-08T21:00:00Z" || len(out.InputHash) != 64 {
		t.Fatal("missing calendar guard or fingerprint")
	}
	repeated, _ := out.fingerprint()
	if repeated != out.InputHash {
		t.Fatal("hash is not stable")
	}
	out.Assignments[0].Results[1].Status = 1
	changed, _ := out.fingerprint()
	if changed == out.InputHash {
		t.Fatal("grading status omitted from the fingerprint")
	}
}
