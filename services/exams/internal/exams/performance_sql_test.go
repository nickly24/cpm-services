package exams

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Opt-in only; the database must be a dedicated loopback QA schema. All test
// tables are connection-local temporary copies and vanish when the pool closes.
func performanceDB(t *testing.T) *sql.DB {
	t.Helper()
	path := os.Getenv("CPM_EXAMS_PERF_DSN_FILE")
	if path == "" {
		t.Skip("dedicated local SQL test; set CPM_EXAMS_PERF_DSN_FILE")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read local QA connection file")
	}
	cfg, err := mysql.ParseDSN(strings.TrimSpace(string(raw)))
	if err != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") || !strings.HasPrefix(cfg.DBName, "exams_upgrade_") {
		t.Fatal("requires dedicated loopback exams_upgrade_ schema")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open local SQL")
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	migration, err := os.ReadFile("../../migrations/001_exams.sql")
	if err != nil {
		files, _ := os.ReadDir("../../migrations")
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "001") {
				migration, err = os.ReadFile("../../migrations/" + f.Name())
				break
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(string(migration), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		lines := []string{}
		for _, line := range strings.Split(stmt, "\n") {
			if !strings.Contains(line, "CONSTRAINT ") {
				lines = append(lines, line)
			}
		}
		stmt = strings.Replace(strings.Join(lines, "\n"), ",\n)", "\n)", 1)
		stmt = strings.Replace(stmt, "CREATE TABLE IF NOT EXISTS", "CREATE TEMPORARY TABLE", 1)
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal("temporary schema", err)
		}
	}
	metadata, err := os.ReadFile("../../migrations/002_import_metadata_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(string(metadata), ";") {
		if strings.TrimSpace(stmt) != "" {
			if _, err = db.Exec(stmt); err != nil {
				t.Fatal("temporary metadata", err)
			}
		}
	}
	return db
}
func performanceSeed(t *testing.T, db *sql.DB, e *Exam) {
	t.Helper()
	raw, err := encodeExam(e)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO exams_catalog_v1(id,organization_id,owner_division_id,revision,next_id,document) VALUES(?,?,?,?,?,?)", e.ID, e.OrganizationID, e.OwnerDivisionID, e.Revision, e.NextID, raw)
	if err != nil {
		t.Fatal(err)
	}
}

type performanceIdentity struct {
	actor   Actor
	members map[int64]Member
	calls   [][]int64
}

func (i *performanceIdentity) Identify(*http.Request) (Actor, error) { return i.actor, nil }
func (i *performanceIdentity) Ready(*http.Request) error             { return nil }
func (i *performanceIdentity) References(_ *http.Request, _ int64, _ []int64, _ int64, ids []int64) (map[int64]Member, error) {
	i.calls = append(i.calls, append([]int64{}, ids...))
	out := map[int64]Member{}
	for _, id := range ids {
		m, ok := i.members[id]
		if !ok {
			return nil, Missing
		}
		out[id] = m
	}
	return out, nil
}
func (i *performanceIdentity) Fetch(*http.Request, int64, []int64, int64, []int64, []string) (ReferenceSet, error) {
	var refs ReferenceSet
	_ = json.Unmarshal([]byte(`{"directions":[{"id":7,"name":"Civil Law","is_active":true},{"id":8,"name":"Appeal Law","is_active":true}]}`), &refs)
	return refs, nil
}
func performanceGET(t *testing.T, s *Server, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	var response struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

func TestPerformanceSQLCatalogReadinessAndStudentPrivacy(t *testing.T) {
	db := performanceDB(t)
	f := fixtureDomain(t, "round_up")
	e := f.e
	e.Name = "Alpha"
	e.DirectionID = 7
	e.OwnerDivisionID = 1
	e.CreatedAt = f.p.Now
	e.Divisions[1].TrainingEnabled = true
	admin := f.admin
	admin.OrganizationID = 10
	admin.DivisionID = ptr(1)
	admin.DivisionIDs = []int64{1, 2}
	members := map[int64]Member{}
	for id, m := range f.p.Members {
		m.DivisionIDs = []int64{1}
		members[id] = m
	}
	d := e.Divisions[1]
	d.Assignments[0].Student = members[11]
	d.Assignments[0].Members = []Member{members[21], members[22]}
	for n := 0; n < 1500; n++ {
		id := int64(1000 + n)
		m := Member{AccountID: id, FullName: fmt.Sprintf("Student %04d", n), Role: "student", Active: true, DivisionIDs: []int64{1}}
		members[id] = m
		d.Assignments = append(d.Assignments, Assignment{ID: 10000 + id, Student: m, Members: []Member{members[21]}, CommissionID: f.commission, AttemptNo: 1})
	}
	for n := 0; n < 2000; n++ {
		e.Questions = append(e.Questions, Question{ID: int64(20000 + n), PartID: e.Parts[0].ID, Text: strings.Repeat("private bank text ", 16), Answer: "private answer"})
	}
	completed := f.p.Now
	d.Attempts = []Attempt{{ID: 8001, AssignmentID: f.assignment, Student: members[11], Members: []Member{members[21]}, AttemptNo: 1, Status: "completed", Config: d.Config, Parts: e.Parts, Questions: e.Questions, CalculatedGrade: 0, CompletedAt: &completed, Presented: []Presented{{ID: 1, Question: Question{Text: "protocol private question"}}}}, {ID: 8002, AssignmentID: 11000, Student: members[1000], Members: []Member{members[21]}, AttemptNo: 1, Status: "completed", Config: d.Config, Parts: e.Parts, Questions: e.Questions, CalculatedGrade: 5, CompletedAt: &completed}}
	performanceSeed(t, db, e)
	outside := &Exam{ID: 2, Name: "Beta", Kind: "outside_lms", DirectionID: 8, OrganizationID: 10, OwnerDivisionID: 1, Divisions: map[int64]*Division{1: {ID: 1, Date: "2026-11-01", Outside: []OutsideResult{{ID: 9, Student: members[11], Points: json.Number("12.5"), Grade: 5}}}}}
	performanceSeed(t, db, outside)
	foreign := &Exam{ID: 3, Name: "foreign secret", Kind: "classic", OrganizationID: 99, Divisions: map[int64]*Division{1: {ID: 1}}}
	performanceSeed(t, db, foreign)
	identity := &performanceIdentity{actor: admin, members: members}
	s := &Server{DB: db, Deps: identity}
	records, err := listCatalog(context.Background(), db, admin, Params{DivisionID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(records[0].Exam.Questions) != 0 || len(records[0].Exam.Divisions[1].Attempts) != 0 || records[0].Questions != 2008 {
		t.Fatal("catalog hydrated history or wrong counts")
	}
	page := performanceGET(t, s, "/v1/exams?limit=1&offset=1&sort=created_desc")
	if page["total"] != float64(2) || len(page["items"].([]any)) != 1 {
		t.Fatal(page)
	}
	page = performanceGET(t, s, "/v1/exams?q=Civil&limit=1")
	if page["total"] != float64(1) {
		t.Fatal("direction search", page)
	}
	page = performanceGET(t, s, "/v1/exams?training=true")
	if page["total"] != float64(1) {
		t.Fatal("training filter", page)
	}
	overview := performanceGET(t, s, "/v1/exams/1/overview")
	counts := overview["counts"].(map[string]any)
	if counts["assignments"] != float64(1501) || counts["completed"] != float64(2) || counts["questions"] != float64(2008) {
		t.Fatal(counts)
	}
	bank, err := loadTrainingBank(context.Background(), db, 10, 1, 1)
	if err != nil || len(bank.Questions) != 2008 || len(bank.Divisions[1].Attempts) != 0 || len(bank.Divisions) != 1 {
		t.Fatal("training projection", err)
	}
	examiner := f.x1
	examiner.OrganizationID = 10
	examiner.DivisionID = ptr(1)
	examiner.DivisionIDs = []int64{1}
	identity.actor = examiner
	page = performanceGET(t, s, "/v1/exams?limit=20")
	if page["total"] != float64(1) || page["items"].([]any)[0].(map[string]any)["assigned_students_count"] != float64(1501) {
		t.Fatal("examiner scope/count", page)
	}
	identity.calls = nil
	page = performanceGET(t, s, "/v1/exams/1/assignments?limit=20&offset=1000&include_readiness=true")
	if page["total"] != float64(1501) || len(identity.calls) != 2 || len(identity.calls[1]) != 21 {
		t.Fatal("not bounded readiness", len(identity.calls), page["total"])
	}
	for _, v := range page["items"].([]any) {
		if v.(map[string]any)["readiness"] == nil {
			t.Fatal("readiness missing")
		}
	}
	student := f.student
	student.OrganizationID = 10
	student.DivisionID = ptr(1)
	student.DivisionIDs = []int64{1}
	identity.actor = student
	page = performanceGET(t, s, "/v1/exams/student-results?limit=1&sort=grade_desc")
	if page["total"] != float64(2) || page["items"].([]any)[0].(map[string]any)["result"].(map[string]any)["grade"] != float64(5) {
		t.Fatal("grades order", page)
	}
	page = performanceGET(t, s, "/v1/exams/student-results?grade=0")
	if page["total"] != float64(1) {
		t.Fatal("grade zero", page)
	}
	result := page["items"].([]any)[0].(map[string]any)["result"].(map[string]any)
	if result["question_count"] != float64(1) {
		t.Fatal("question count", result)
	}
	serialized, _ := json.Marshal(page)
	for _, forbidden := range []string{"private answer", "private bank", "protocol private", "Student 0000", "immutable_definitions", "votes_half", "calculated_grade"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatal("student privacy", forbidden)
		}
	}
	page = performanceGET(t, s, "/v1/exams/student-results?q=Appeal&sort=direction_asc")
	if page["total"] != float64(1) {
		t.Fatal("student direction search", page)
	}
	d.Attempts[0].Appeals = []Appeal{{ID: 1, Grade: 4, PreviousGrade: 0}}
	d.Assignments = append(d.Assignments, Assignment{ID: 9001, Student: members[11], Members: []Member{members[21]}, AttemptNo: 2})
	d.Attempts = append(d.Attempts, Attempt{ID: 9002, AssignmentID: 9001, Student: members[11], Members: []Member{members[21]}, AttemptNo: 2, Status: "pending_ready"})
	encoded, er := encodeExam(e)
	if er != nil {
		t.Fatal(er)
	}
	if _, er = db.Exec("UPDATE exams_catalog_v1 SET document=? WHERE id=1", encoded); er != nil {
		t.Fatal(er)
	}
	page = performanceGET(t, s, "/v1/exams/student-results?grade=4")
	if page["total"] != float64(1) {
		t.Fatal("appeal effective grade", page)
	}
	result = page["items"].([]any)[0].(map[string]any)["result"].(map[string]any)
	if result["has_appeal"] != true || result["has_active_retake"] != true || result["retake_status"] != "pending_ready" || result["appeals"] != nil {
		t.Fatal("appeal/retake projection", result)
	}
	student.DivisionID = ptr(2)
	student.DivisionIDs = []int64{2}
	identity.actor = student
	page = performanceGET(t, s, "/v1/exams/student-results")
	if page["total"] != float64(0) {
		t.Fatal("division leak", page)
	}
	identity.actor = admin
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/exams/student-results", nil))
	if w.Code != 403 {
		t.Fatal("admin owner route", w.Code)
	}
}

func TestPerformanceSQLTransactionLocalCommandRollbackAndReplay(t *testing.T) {
	db := performanceDB(t)
	f := fixtureDomain(t, "round_up")
	e := f.e
	e.OwnerDivisionID = 1
	performanceSeed(t, db, e)
	a := f.admin
	a.OrganizationID = 10
	a.DivisionID = ptr(1)
	a.DivisionIDs = []int64{1, 2}
	s := &Server{DB: db}
	before, err := load(context.Background(), db, 10, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/exams/1/parts", nil)
	r.Header.Set("Idempotency-Key", UUID())
	raw := []byte(`{"code":"C"}`)
	_, _, err = s.mutate(r, a, raw, 1, func(tx *sql.Tx, locked *Exam) (any, error) {
		if _, er := command(locked, a, "part-create", f.p, raw); er != nil {
			return nil, er
		}
		if er := save(context.Background(), tx, locked); er != nil {
			return nil, er
		}
		return nil, Conflict("injected_after_save")
	})
	if err == nil {
		t.Fatal("expected rollback")
	}
	after, err := load(context.Background(), db, 10, 1, false)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed mutation escaped rollback", err)
	}
	var receipts int
	if err = db.QueryRow("SELECT COUNT(*) FROM exams_receipts_v1").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("failed receipt persisted", receipts, err)
	}
	calls := 0
	apply := func(tx *sql.Tx, locked *Exam) (any, error) {
		calls++
		out, er := command(locked, a, "part-create", f.p, raw)
		if er != nil {
			return nil, er
		}
		locked.Revision++
		return out, save(context.Background(), tx, locked)
	}
	one, _, err := s.mutate(r, a, raw, 1, apply)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := s.mutate(r, a, raw, 1, apply)
	if err != nil || calls != 1 {
		t.Fatal("replay reran command", calls, err)
	}
	left, _ := json.Marshal(one)
	right, _ := json.Marshal(two)
	var canonicalLeft, canonicalRight any
	_ = json.Unmarshal(left, &canonicalLeft)
	_ = json.Unmarshal(right, &canonicalRight)
	if !reflect.DeepEqual(canonicalLeft, canonicalRight) {
		t.Fatal("replay mismatch")
	}
	after, err = load(context.Background(), db, 10, 1, false)
	if err != nil || len(after.Parts) != len(before.Parts)+1 || after.Revision != before.Revision+1 {
		t.Fatal("unexpected persisted mutation", err)
	}
	if _, _, err = s.mutate(r, a, []byte(`{"code":"D"}`), 1, apply); err == nil {
		t.Fatal("key reuse accepted")
	}
}

func TestPerformanceSQLImportSnapshotStaysStableAndCommitRevalidates(t *testing.T) {
	db := performanceDB(t)
	e, a, p := auxFixture()
	a.DivisionID = ptr(2)
	a.DivisionIDs = []int64{2}
	performanceSeed(t, db, e)
	rows := []ImportRow{auxRow(t, "first", map[string]any{"studentId": 11, "examinator1Id": 13}), auxRow(t, "second", map[string]any{"studentId": 12, "examinator1Id": 13})}
	checked, err := ValidateImport(e, a, "assignments", p, rows)
	if err != nil || len(checked[0].Errors) != 0 || len(checked[1].Errors) != 0 {
		t.Fatal("seed validation", err, checked)
	}
	session := UUID()
	raw, _ := json.Marshal(checked)
	_, err = db.Exec("INSERT INTO exams_imports_v1(id,organization_id,division_id,account_id,exam_id,kind,revision,state,rows_json,expires_at,source_filename,source_hash) VALUES(?,?,?,?,?,'assignments',1,'editable',?,?,?,?)", session, 1, 2, a.AccountID, e.ID, raw, time.Now().Add(time.Hour), "fixture.xlsx", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	identity := &performanceIdentity{actor: a, members: p.Members}
	s := &Server{DB: db, Deps: identity}
	url := "/v1/exams/1/imports/assignments/" + session
	first := performanceGET(t, s, url+"?limit=1")
	second := performanceGET(t, s, url+"?limit=1&offset=1&expected_revision=1")
	if first["validation_hash"] != second["validation_hash"] || len(identity.calls) != 0 || second["total"] != float64(2) {
		t.Fatal("paging revalidated or changed hash")
	}
	member := identity.members[11]
	member.Active = false
	identity.members[11] = member
	stale := performanceGET(t, s, url+"?limit=1&only_errors=true")
	if stale["validation_hash"] != first["validation_hash"] || stale["total"] != float64(0) || len(identity.calls) != 0 {
		t.Fatal("read changed persisted snapshot")
	}
	request := httptest.NewRequest("POST", url+"/commit", strings.NewReader(`{"expected_revision":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", UUID())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request)
	if w.Code != 409 || len(identity.calls) == 0 {
		t.Fatal("commit did not revalidate stale membership", w.Code, w.Body.String())
	}
	persisted, err := load(context.Background(), db, 1, 1, false)
	if err != nil || len(persisted.Divisions[2].Assignments) != 0 {
		t.Fatal("stale preview committed", err)
	}
	var state string
	if err = db.QueryRow("SELECT state FROM exams_imports_v1 WHERE id=?", session).Scan(&state); err != nil || state != "editable" {
		t.Fatal("failed commit changed session", err, state)
	}
	var receipts int
	if err = db.QueryRow("SELECT COUNT(*) FROM exams_receipts_v1").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("failed import receipt retained", err, receipts)
	}
}
