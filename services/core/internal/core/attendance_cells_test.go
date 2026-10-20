package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestAttendanceCellsRejectsPublicAndAmbiguousRequests(t *testing.T) {
	s := &Server{InternalKey: strings.Repeat("k", 32)}
	valid := `{"organization_id":1,"division_id":2,"account_ids":[7],"operation_key":"fixed-key"}`
	for _, key := range []string{"", "wrong"} {
		r := httptest.NewRequest("POST", "/internal/core/attendance/cell-targets", strings.NewReader(valid))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	for _, raw := range []string{`{}`, `null`, `[]`, strings.Replace(valid, `[7]`, `[7,7]`, 1), strings.Replace(valid, `[7]`, `[]`, 1), strings.Replace(valid, `[7]`, `[0]`, 1), strings.Replace(valid, `"division_id":2`, `"division_id":2,"Division_id":3`, 1), strings.Replace(valid, `"division_id":2`, `"division_id":2,"role":"admin"`, 1), strings.Replace(valid, `"division_id":2`, `"division_id":2,"group_id":11`, 1), strings.Replace(valid, `"division_id":2`, `"division_id":2,"proctor_account_id":6`, 1), strings.Replace(valid, `"fixed-key"`, `" short "`, 1), strings.Replace(valid, `[7]`, `[`+strings.Repeat("7,", 100)+`8]`, 1), valid + ` {}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.Header.Set("X-Service-Key", s.InternalKey)
		s.attendanceCells(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid DTO accepted %d %s", w.Code, raw)
		}
	}
}
func coreCellsDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CORE_CELLS_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL requires CORE_CELLS_TEST_DSN")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.DBName != "cpm_dev" {
		t.Fatal("only local cpm_dev bootstrap", err)
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("cpm_cells_qa_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP DATABASE " + name); admin.Close() })
	statements := []string{
		`CREATE TABLE organizations(id BIGINT PRIMARY KEY,is_active BOOL)`,
		`CREATE TABLE divisions(id BIGINT PRIMARY KEY,organization_id BIGINT,is_active BOOL)`,
		`CREATE TABLE auth_users(id BIGINT PRIMARY KEY,role VARCHAR(32),ref_id BIGINT)`,
		`CREATE TABLE auth_account_state(user_id BIGINT PRIMARY KEY,is_active BOOL)`,
		`CREATE TABLE auth_organization_access(user_id BIGINT,organization_id BIGINT,PRIMARY KEY(user_id,organization_id))`,
		`CREATE TABLE auth_division_access(user_id BIGINT,organization_id BIGINT,division_id BIGINT,group_id BIGINT,PRIMARY KEY(organization_id,division_id,user_id))`,
		`CREATE TABLE core_group_scope(group_id BIGINT PRIMARY KEY,organization_id BIGINT,division_id BIGINT,is_active BOOL)`,
		`CREATE TABLE core_proctor_groups(group_id BIGINT,division_id BIGINT,user_id BIGINT,PRIMARY KEY(division_id,group_id))`,
		`CREATE TABLE core_attendance_history_reservations(organization_id BIGINT,division_id BIGINT,user_id BIGINT,reservation_id VARCHAR(64),operation_key VARCHAR(100),PRIMARY KEY(organization_id,division_id,user_id))`,
		"CREATE TABLE students(id BIGINT PRIMARY KEY,full_name VARCHAR(100),school_id BIGINT,`class` INT,tg_name VARCHAR(100))",
	}
	for _, table := range []string{"admins", "proctors", "examinators", "supervisors", "admin_role_users"} {
		statements = append(statements, "CREATE TABLE "+table+"(id BIGINT PRIMARY KEY,full_name VARCHAR(100))")
	}
	statements = append(statements, `INSERT INTO organizations VALUES(1,1),(99,1)`, `INSERT INTO divisions VALUES(2,1,1),(3,1,0),(4,99,1)`, `INSERT INTO auth_users VALUES(7,'student',70),(8,'student',80),(6,'proctor',60),(9,'student',90)`, `INSERT INTO students VALUES(70,'Student 7',NULL,10,NULL),(80,'Student 8',NULL,11,NULL),(90,'Student 9',NULL,10,NULL)`, `INSERT INTO proctors VALUES(60,'Proctor')`, `INSERT INTO auth_account_state VALUES(6,1),(7,1),(8,1),(9,1)`, `INSERT INTO auth_organization_access VALUES(6,1),(7,1),(8,1),(9,99)`, `INSERT INTO auth_division_access VALUES(6,1,2,11),(7,1,2,11),(8,1,2,12),(9,99,4,99)`, `INSERT INTO core_group_scope VALUES(11,1,2,1),(12,1,2,1),(99,99,4,1)`, `INSERT INTO core_proctor_groups VALUES(11,2,6)`)
	for _, q := range statements {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
func coreCellCall(t *testing.T, s *Server, organization, division int64, accounts []int64, proctor bool) (int, []AttendanceCellMember) {
	t.Helper()
	in := map[string]any{"organization_id": organization, "division_id": division, "account_ids": accounts, "operation_key": "synthetic-cell-command"}
	if proctor {
		in["proctor_account_id"] = 6
		in["group_id"] = 11
	}
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/internal/core/attendance/cell-targets", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", s.InternalKey)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var out struct {
		Data struct {
			Members []AttendanceCellMember `json:"members"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out.Data.Members
}
func TestMySQLAttendanceCellsAtomicScopeReservations(t *testing.T) {
	db := coreCellsDB(t)
	s := &Server{DB: db, InternalKey: strings.Repeat("k", 32)}
	for _, test := range []struct {
		org, div int64
		ids      []int64
		proctor  bool
		code     int
	}{{1, 3, []int64{7}, false, 404}, {99, 2, []int64{7}, false, 404}, {1, 2, []int64{9}, false, 404}, {1, 2, []int64{6}, false, 404}, {1, 2, []int64{7, 9}, false, 404}, {1, 2, []int64{7, 8}, true, 403}} {
		code, _ := coreCellCall(t, s, test.org, test.div, test.ids, test.proctor)
		if code != test.code {
			t.Fatal(test, code)
		}
		var count int
		db.QueryRow("SELECT COUNT(*) FROM core_attendance_history_reservations").Scan(&count)
		if count != 0 {
			t.Fatal("partially committed guards", test, count)
		}
	}
	code, members := coreCellCall(t, s, 1, 2, []int64{8, 7}, false)
	if code != 200 || len(members) != 2 || members[0].AccountID != 7 || members[0].GroupID == nil || *members[0].GroupID != 11 || members[0].Name != "Student 7" || !members[0].Permanent || len(members[0].ReservationID) != 64 {
		t.Fatal(code, members)
	}
	code, repeated := coreCellCall(t, s, 1, 2, []int64{7}, true)
	if code != 200 || len(repeated) != 1 || repeated[0].ReservationID != members[0].ReservationID {
		t.Fatal("unstable guard", code, repeated)
	}
	db.Exec("DELETE FROM core_proctor_groups WHERE user_id=6")
	if code, _ = coreCellCall(t, s, 1, 2, []int64{7}, true); code != 403 {
		t.Fatal("revoked proctor accepted", code)
	}
	db.Exec("UPDATE auth_account_state SET is_active=0 WHERE user_id=7")
	if code, _ = coreCellCall(t, s, 1, 2, []int64{7}, false); code != 404 {
		t.Fatal("disabled student accepted", code)
	}
	db.Exec("UPDATE organizations SET is_active=0 WHERE id=1")
	if code, _ = coreCellCall(t, s, 1, 2, []int64{8}, false); code != 404 {
		t.Fatal("disabled org accepted", code)
	}
}
func TestMySQLAttendanceCellsSerializesMembershipRevocation(t *testing.T) {
	db := coreCellsDB(t)
	s := &Server{DB: db, InternalKey: strings.Repeat("k", 32)}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRow("SELECT id FROM divisions WHERE id=2 FOR UPDATE").Scan(&id); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() { code, _ := coreCellCall(t, s, 1, 2, []int64{7}, false); result <- code }()
	select {
	case code := <-result:
		t.Fatal("ignored membership serialization", code)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err = tx.Exec("DELETE FROM auth_division_access WHERE user_id=7"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		if code != 404 {
			t.Fatal("revoked membership accepted", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lock not released")
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM core_attendance_history_reservations").Scan(&count)
	if count != 0 {
		t.Fatal("guard created after revocation")
	}
}
