package core

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

//go:embed testdata/exam_workflow_mysql.sql
var examWorkflowTemporarySchema string

// Opt-in real MySQL QA. Temporary tables are created on one pinned connection;
// no pre-existing application schema, migrations or persistent rows are needed.
func TestExamWorkflowMySQL(t *testing.T) {
	file := os.Getenv("CPM_EXAM_CORE_MYSQL_DSN_FILE")
	if file == "" {
		t.Skip("set CPM_EXAM_CORE_MYSQL_DSN_FILE for isolated MySQL integration")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if cfg.Net != "tcp" || !strings.HasPrefix(cfg.DBName, "exams_upgrade_") || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") {
		t.Fatal("isolated local QA database required")
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range strings.Split(examWorkflowTemporarySchema, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal("temporary fixture schema", err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	org := time.Now().UnixMicro()
	d1, d2, foreign, inactive := org+10, org+11, org+12, org+13
	exec("INSERT INTO organizations(id,name) VALUES(?,?),(?,?)", org, "SYNTHETIC exam workflow", org+1, "SYNTHETIC foreign")
	exec("INSERT INTO divisions(id,organization_id,name,is_active) VALUES(?,?,?,1),(?,?,?,1),(?,?,?,1),(?,?,?,0)", d1, org, "First", d2, org, "Second", foreign, org+1, "Foreign", inactive, org, "Inactive")
	var onHash func()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onHash != nil {
			onHash()
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"hash": "pbkdf2:sha256:600000$synthetic$fixture"})
	}))
	defer auth.Close()
	s := &Server{DB: db, Auth: NewAuthClient(Config{AuthURL: auth.URL, InternalKey: strings.Repeat("s", 32)})}
	a := Actor{AccountID: 1, OrganizationID: org, Role: "admin", OrganizationAdmin: true}
	create := func(login string, targets []int64) (int64, error) {
		payload, _ := json.Marshal(UserDTO{Role: "examinator", FullName: "SYNTHETIC Examiner", Login: login, Password: "synthetic-password", DivisionIDs: targets})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(payload)))
		r.Header.Set("Content-Type", "application/json")
		body, _, e := s.userCreate(r, a, d1)
		if e != nil {
			return 0, e
		}
		return body.(map[string]any)["account_id"].(int64), nil
	}
	prefix := fmt.Sprintf("qa%d-", org)
	uid, err := create(prefix+"0012", []int64{d2, d1, d2})
	if err != nil {
		t.Fatal(err)
	}
	var memberships int
	if err = db.QueryRow("SELECT COUNT(*) FROM auth_division_access WHERE user_id=?", uid).Scan(&memberships); err != nil || memberships != 2 {
		t.Fatal("memberships not atomic/deduplicated", memberships, err)
	}
	for name, target := range map[string]int64{"foreign": foreign, "inactive": inactive, "missing": org + 99} {
		if _, e := create(prefix+name, []int64{target}); e == nil {
			t.Fatal("accepted target", name)
		}
		var count int
		_ = db.QueryRow("SELECT COUNT(*) FROM auth_users WHERE username=?", prefix+name).Scan(&count)
		if count != 0 {
			t.Fatal("partial account retained", name)
		}
	}
	if _, e := create(prefix+"0012", []int64{d2}); e == nil {
		t.Fatal("duplicate login created")
	}
	onHash = func() { exec("UPDATE divisions SET is_active=0 WHERE id=?", d2) }
	if _, e := create(prefix+"revoked", []int64{d2}); e == nil {
		t.Fatal("division revoked during hashing still accepted")
	}
	onHash = nil
	exec("UPDATE divisions SET is_active=1 WHERE id=?", d2)
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM auth_users WHERE username=?", prefix+"revoked").Scan(&count)
	if count != 0 {
		t.Fatal("revoked target left account")
	}
	// More than a page of synthetic accounts exercises actual LIMIT/OFFSET and cursor SQL.
	for n := 0; n < 205; n++ {
		if _, e := create(fmt.Sprintf("%sbulk%03d", prefix, n), nil); e != nil {
			t.Fatal(e)
		}
	}
	exec("UPDATE auth_account_state SET is_active=0 WHERE user_id=?", uid)
	read := func(query string, division int64) map[string]any {
		t.Helper()
		body, _, e := s.examParticipants(httptest.NewRequest("GET", "/?"+query, nil), a, division)
		if e != nil {
			t.Fatal(e)
		}
		return body.(map[string]any)
	}
	first := read("limit=200", d1)
	items := first["items"].([]map[string]any)
	if first["total"] != int64(205) || len(items) != 200 {
		t.Fatal("active scoped first page", first["total"], len(items))
	}
	second := read("limit=200&offset=200", d1)
	if len(second["items"].([]map[string]any)) != 5 {
		t.Fatal("last offset page truncated")
	}
	cursor := num(items[len(items)-1]["account_id"])
	last := read(fmt.Sprintf("limit=200&after_id=%d", cursor), d1)
	if len(last["items"].([]map[string]any)) != 5 {
		t.Fatal("cursor page truncated")
	}
	if read("limit=200", d2)["total"] != int64(0) {
		t.Fatal("inactive account leaked into other division")
	}
	exec("UPDATE auth_account_state SET is_active=1 WHERE user_id=?", uid)
	lookup := read("q="+prefix+"0012", d1)["items"].([]map[string]any)
	if len(lookup) != 1 || lookup[0]["login"] != prefix+"0012" {
		t.Fatal("numeric login/search changed")
	}
	exec("DELETE FROM auth_division_access WHERE user_id=? AND division_id=?", uid, d2)
	if read("limit=200", d2)["total"] != int64(0) {
		t.Fatal("revoked membership leaked")
	}
	for _, row := range items {
		if _, exists := row["password"]; exists {
			t.Fatal("credentials leaked")
		}
	}
	t.Log("verified atomic multi-division creation, invalid/inactive/foreign rollback, revocation during hashing, duplicate login, 205 active participants, offset and cursor pages, search and revoked membership")
}
