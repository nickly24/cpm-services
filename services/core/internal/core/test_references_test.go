package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func testReferencesRequest(s *Server, body, key, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/internal/core/test-references", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-Service-Key", key)
	r.Header.Set("Authorization", "Bearer browser-token")
	r.Header.Set("X-Organization-ID", "999")
	w := httptest.NewRecorder()
	s.testReferences(w, r)
	return w
}

// Extend the existing reference driver with parameter checks: a projection
// must be scoped by the authenticated service DTO, not a browser header.
type testReferenceConn struct {
	*examReferenceConn
	args [][]driver.Value
}
type testReferenceDriver struct{ conn *testReferenceConn }

func (d testReferenceDriver) Open(string) (driver.Conn, error) { return d.conn, nil }
func (c *testReferenceConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	i := c.next
	actual := make([]driver.Value, len(args))
	for n, arg := range args {
		actual[n] = arg.Value
	}
	if i >= len(c.args) || !reflect.DeepEqual(actual, c.args[i]) {
		c.t.Errorf("reference query %d lost scope parameters: %v", i, actual)
	}
	if strings.Contains(query, "FROM auth_division_access") {
		for _, private := range []string{"username", "password", "tg_name", "core_profile_extra"} {
			if strings.Contains(query, private) {
				c.t.Errorf("student projection includes private column %s", private)
			}
		}
		if strings.Contains(query, "st.is_active=1") {
			c.t.Error("blocked participants were silently excluded from the complete projection")
		}
	}
	return c.examReferenceConn.QueryContext(ctx, query, args)
}

func testReferencesDB(t *testing.T, steps []examReferenceStep, args [][]driver.Value) (*sql.DB, *examReferenceConn) {
	t.Helper()
	base := &examReferenceConn{t: t, steps: steps}
	name := fmt.Sprintf("core-test-reference-%d", examReferenceDriverID.Add(1))
	sql.Register(name, testReferenceDriver{&testReferenceConn{base, args}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, base
}

func TestTestReferencesStrictDTOAndServiceKeyBeforeDatabase(t *testing.T) {
	key := strings.Repeat("k", 32)
	s := &Server{InternalKey: key}
	for _, provided := range []string{"", "wrong", strings.Repeat("x", 32)} {
		if w := testReferencesRequest(s, `{}`, provided, "application/json"); w.Code != 401 {
			t.Fatal("unauthenticated projection reached database", w.Code)
		}
	}
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"organization_id":0}`, `{"organization_id":1,"direction_id":0}`,
		`{"organization_id":1,"division_ids":[0]}`, `{"organization_id":1,"division_ids":[2,2]}`,
		`{"organization_id":1,"include_students":true}`, `{"organization_id":1,"include_students":true,"division_ids":[2,3]}`,
		`{"organization_id":1,"account_ids":[2]}`, `{"organization_id":1,"include_students":1}`, `{"organization_id":1} {}`,
		`{"organization_id":1,"direction_ids":[0]}`, `{"organization_id":1,"direction_ids":[2,2]}`,
		`{"organization_id":1,"direction_id":2,"direction_ids":[3]}`,
	} {
		if w := testReferencesRequest(s, body, key, "application/json"); w.Code != 400 {
			t.Fatalf("accepted invalid projection DTO %s: %d", body, w.Code)
		}
	}
	if w := testReferencesRequest(s, `{"organization_id":1}`, key, "text/plain"); w.Code != 415 {
		t.Fatal("accepted non-JSON projection", w.Code)
	}
}

func testProjectionSteps(accounts [][]driver.Value) []examReferenceStep {
	return []examReferenceStep{
		{"organizations WHERE id=? AND is_active=1", []string{"id", "name"}, [][]driver.Value{{int64(7), "DEV Organization"}}},
		{"id=? AND organization_id=? AND is_active=1", []string{"id", "name", "is_active"}, [][]driver.Value{{int64(10), "DEV Division", int64(1)}}},
		{"WHERE x.organization_id=? AND x.division_id=? ORDER BY a.id LIMIT 2001", []string{"account_id", "profile_id", "full_name", "class", "is_active"}, accounts},
	}
}

func TestTestReferencesCompleteScopedStudentsIncludeBlocked(t *testing.T) {
	key := strings.Repeat("k", 32)
	for _, accounts := range [][][]driver.Value{nil, {
		{int64(12), int64(99), "DEV Active", int64(10), int64(1)},
		{int64(13), int64(100), "DEV Blocked", nil, int64(0)},
	}} {
		steps := testProjectionSteps(accounts)
		db, conn := testReferencesDB(t, steps, [][]driver.Value{{int64(7)}, {int64(10), int64(7)}, {int64(7), int64(10)}})
		w := testReferencesRequest(&Server{DB: db, InternalKey: key}, `{"organization_id":7,"division_ids":[10],"direction_id":null,"include_students":true}`, key, "application/json")
		if w.Code != 200 || !conn.committed || conn.next != len(steps) {
			t.Fatalf("complete projection failed: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Data struct {
				Complete bool `json:"accounts_complete"`
				Version  int  `json:"account_projection_version"`
				Accounts []struct {
					AccountID int64  `json:"account_id"`
					ProfileID int64  `json:"profile_id"`
					Class     *int64 `json:"class"`
					Active    bool   `json:"is_active"`
				} `json:"accounts"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !out.Data.Complete || out.Data.Version != 1 || len(out.Data.Accounts) != len(accounts) {
			t.Fatalf("projection markers or empty list lost: %s", w.Body.String())
		}
		if len(accounts) > 0 && (out.Data.Accounts[0].AccountID != 12 || out.Data.Accounts[0].ProfileID != 99 || !out.Data.Accounts[0].Active || out.Data.Accounts[1].Active || out.Data.Accounts[1].Class != nil) {
			t.Fatal("lost account/profile identity, blocked membership or nullable class", w.Body.String())
		}
	}
}

func TestTestReferencesRejectOverflowWithoutPartialProjection(t *testing.T) {
	accounts := make([][]driver.Value, 2001)
	for i := range accounts {
		accounts[i] = []driver.Value{int64(i + 1), int64(i + 10000), "DEV Student", nil, int64(1)}
	}
	db, conn := testReferencesDB(t, testProjectionSteps(accounts), [][]driver.Value{{int64(7)}, {int64(10), int64(7)}, {int64(7), int64(10)}})
	key := strings.Repeat("k", 32)
	w := testReferencesRequest(&Server{DB: db, InternalKey: key}, `{"organization_id":7,"division_ids":[10],"include_students":true}`, key, "application/json")
	if w.Code != 413 || conn.committed || !conn.rolledBack || strings.Contains(w.Body.String(), "accounts_complete") {
		t.Fatal("overflow returned misleading complete snapshot", w.Code, w.Body.String())
	}
}

func TestTestReferencesMissingForeignOrArchivedScopeFailsClosed(t *testing.T) {
	key := strings.Repeat("k", 32)
	for _, stage := range []int{0, 1} {
		steps := testProjectionSteps(nil)[:stage+1]
		steps[stage].rows = nil
		args := [][]driver.Value{{int64(7)}, {int64(10), int64(7)}}[:stage+1]
		db, conn := testReferencesDB(t, steps, args)
		w := testReferencesRequest(&Server{DB: db, InternalKey: key}, `{"organization_id":7,"division_ids":[10],"include_students":true}`, key, "application/json")
		if w.Code != 404 || conn.committed || !conn.rolledBack {
			t.Fatal("missing/foreign scope accepted", w.Code, w.Body.String())
		}
	}
}
