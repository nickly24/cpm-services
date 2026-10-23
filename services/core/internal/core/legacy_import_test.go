package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func legacyAdmin() Actor {
	return Actor{AccountID: 3, OrganizationID: 1, Role: "admin", OrganizationAdmin: true}
}
func legacyEmptyIndex() legacyImportIndex {
	return legacyImportIndex{Students: map[string][]legacyPersonMatch{}, Proctors: map[string][]legacyPersonMatch{}, Schools: map[string][]legacyNamedMatch{}, Groups: map[string][]legacyNamedMatch{}, GroupOwners: map[int64]int64{}}
}
func legacyRow(n int, name string) LegacyImportRow {
	return LegacyImportRow{Row: n, FullName: name, ClassRaw: "7.0"}
}
func legacyInt(n int64) *int64 { return &n }

func TestLegacyNameAndClassPreserveWorkbookMatching(t *testing.T) {
	for _, name := range []string{" Иванов  Иван Петрович ", "ИВАНОВ Иван Другой"} {
		if legacyPersonKey(name) != "иванов|иван" {
			t.Fatal(name)
		}
	}
	if legacyPersonKey("Петров") == "петров" || legacyPersonKey("Ёлкин Иван") == legacyPersonKey("Елкин Иван") {
		t.Fatal("invented name normalization")
	}
	for _, raw := range []string{"7", "7.0", "7e0", " +7 ", "٧", "７"} {
		if n, e := legacyClass(raw); n != 7 || e != "" {
			t.Fatalf("old numeric cell rejected %q", raw)
		}
	}
	if n, e := legacyClass("1_0"); n != 10 || e != "" {
		t.Fatal("old Python float underscore syntax lost")
	}
	for _, raw := range []string{"", "0", "-1", "7.5", "7,0", "NaN", "Inf", "100", "1e30", "0x1p3", "1__0", "7_"} {
		if _, e := legacyClass(raw); e == "" {
			t.Fatalf("invalid native class accepted %q", raw)
		}
	}
	if legacyNameKey("  Школа  №1 ") != "школа  №1" {
		t.Fatal("school matching must not collapse internal spaces")
	}
}
func TestLegacyPreviewCreateGraphAndSkipHistory(t *testing.T) {
	idx := legacyEmptyIndex()
	idx.Students["иванов|иван"] = []legacyPersonMatch{{AccountID: 91, ProfileID: 7, Active: true, InDivision: true}}
	rows := []LegacyImportRow{legacyRow(2, "Иванов Иван Другой"), legacyRow(3, "Петров Пётр")}
	rows[0].SchoolName = "Unused school"
	rows[0].ProctorName = "Unused Proctor"
	rows[1].SchoolName = "Школа №1"
	rows[1].ProctorName = "Сидоров Сергей Николаевич"
	p := legacyBuildPreview(rows, idx, legacyAdmin())
	if !p.CanCommit || p.Summary.StudentsCreate != 1 || p.Summary.StudentsSkip != 1 || p.Summary.SchoolsCreate != 1 || p.Summary.GroupsCreate != 1 || p.Summary.ProctorsCreate != 1 {
		t.Fatalf("bad graph %#v", p.Summary)
	}
	if p.Students[0].Action != "skip" || *p.Students[0].ExistingStudentID != 7 || *p.Students[0].ExistingAccountID != 91 {
		t.Fatal("account/profile mapping or existing history lost")
	}
	if _, ok := p.Schools["unused school"]; ok {
		t.Fatal("created mapping from unchanged skipped student")
	}
	if p.Groups["сидоров|сергей"].Name != "Сидоров Сергей Николаевич" {
		t.Fatal("group is not derived from proctor")
	}
}
func TestLegacyAmbiguousInactiveOtherDivisionAndRepeatedPeopleBlockCommit(t *testing.T) {
	for _, test := range []struct {
		name      string
		matches   []legacyPersonMatch
		duplicate bool
	}{
		{"ambiguous", []legacyPersonMatch{{AccountID: 10, ProfileID: 20, Active: true, InDivision: true}, {AccountID: 11, ProfileID: 21, Active: true, InDivision: true}}, false},
		{"inactive", []legacyPersonMatch{{AccountID: 10, ProfileID: 20, Active: false, InDivision: true}}, false},
		{"otherdivision", []legacyPersonMatch{{AccountID: 10, ProfileID: 20, Active: true, InDivision: false}}, false},
		{"duplicatefile", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			idx := legacyEmptyIndex()
			idx.Students["иванов|иван"] = test.matches
			rows := []LegacyImportRow{legacyRow(2, "Иванов Иван")}
			if test.duplicate {
				rows = append(rows, legacyRow(3, "Иванов Иван Другое"))
			}
			p := legacyBuildPreview(rows, idx, legacyAdmin())
			if p.CanCommit || p.Summary.RowErrors != len(rows) || len(p.Students[0].Errors) == 0 {
				t.Fatalf("unsafe matching accepted %#v", p)
			}
			if p.Students[0].ExistingAccountID != nil {
				t.Fatal("matched foreign/inactive ID disclosed")
			}
		})
	}
}
func TestLegacyExistingProctorKeepsAssignedGroup(t *testing.T) {
	idx := legacyEmptyIndex()
	idx.Proctors["сидоров|сергей"] = []legacyPersonMatch{{AccountID: 90, ProfileID: 5, GroupID: 77, Active: true, InDivision: true}}
	idx.Groups["историческая группа"] = []legacyNamedMatch{{77, "Историческая группа", true}}
	idx.GroupOwners[77] = 90
	row := legacyRow(2, "Петров Пётр")
	row.ProctorName = "Сидоров Сергей"
	p := legacyBuildPreview([]LegacyImportRow{row}, idx, legacyAdmin())
	if !p.CanCommit || p.Summary.ProctorsCreate != 0 || p.Summary.GroupsCreate != 0 || p.Groups["сидоров|сергей"].Name != "Историческая группа" || *p.Proctors["сидоров|сергей"].ExistingGroupID != 77 {
		t.Fatal("existing proctor was moved/recreated")
	}
	idx.GroupOwners[77] = 91
	if legacyBuildPreview([]LegacyImportRow{row}, idx, legacyAdmin()).CanCommit {
		t.Fatal("another proctor's group stolen")
	}
}
func TestLegacyScopedPermissionsNeverPromoteStaff(t *testing.T) {
	a := Actor{AccountID: 3, OrganizationID: 1, Role: "staff_admin", DivisionID: legacyInt(2), Permissions: map[string]Permission{"upload": {true, true}, "users": {true, true}}}
	row := legacyRow(2, "Петров Пётр")
	idx := legacyEmptyIndex()
	if !legacyBuildPreview([]LegacyImportRow{row}, idx, a).CanCommit {
		t.Fatal("plain scoped student create denied")
	}
	row.SchoolName = "Новая школа"
	if legacyBuildPreview([]LegacyImportRow{row}, idx, a).CanCommit {
		t.Fatal("scoped import created org-wide school")
	}
	row.SchoolName = ""
	row.ProctorName = "Сидоров Сергей"
	if legacyBuildPreview([]LegacyImportRow{row}, idx, a).CanCommit {
		t.Fatal("scoped import created staff or assigned group without rights")
	}
	idx.Proctors["сидоров|сергей"] = []legacyPersonMatch{{AccountID: 90, ProfileID: 5, Active: true, InDivision: true}}
	idx.Groups["сидоров сергей"] = []legacyNamedMatch{{77, "Сидоров Сергей", true}}
	a.Permissions["groups"] = Permission{true, true}
	if !legacyBuildPreview([]LegacyImportRow{row}, idx, a).CanCommit {
		t.Fatal("existing mapping with explicit rights denied")
	}
	if legacyImportPermission(a, 9, true) == nil {
		t.Fatal("actor context switched through URL")
	}
	a.Permissions["users"] = Permission{true, false}
	if legacyImportPermission(a, 2, true) == nil {
		t.Fatal("revoked live edit remained effective")
	}
}
func TestLegacyNoOpAndMalformedCellsAreHonest(t *testing.T) {
	idx := legacyEmptyIndex()
	idx.Students["иванов|иван"] = []legacyPersonMatch{{AccountID: 9, ProfileID: 8, Active: true, InDivision: true}}
	p := legacyBuildPreview([]LegacyImportRow{legacyRow(2, "Иванов Иван")}, idx, legacyAdmin())
	if p.CanCommit || p.Summary.StudentsSkip != 1 || p.Summary.StudentsCreate != 0 {
		t.Fatal("zero creates published as commit-ready")
	}
	for _, rows := range [][]LegacyImportRow{nil, {legacyRow(1, "A B")}, {legacyRow(2, "A B"), legacyRow(2, "C D")}, make([]LegacyImportRow, 101)} {
		if _, e := legacyRows(rows); e == nil {
			t.Fatal("invalid source rows accepted")
		}
	}
	row := legacyRow(2, strings.Repeat("Ф", 51))
	row.TGName = strings.Repeat("x", 31)
	row.SchoolName = strings.Repeat("Ш", 256)
	row.ClassRaw = "0"
	p = legacyBuildPreview([]LegacyImportRow{row}, legacyEmptyIndex(), legacyAdmin())
	if p.CanCommit || len(p.Students[0].Errors) < 4 {
		t.Fatal("invalid owner fields quietly truncated")
	}
}
func TestLegacyStrictDTORejectsScopeSecretsAndAmbiguousJSON(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"rows":null}`, `{"rows":[],"organization_id":9}`, `{"rows":[],"rows":[]}`, `{"rows":[{"row":2,"row":3}]}`, `{"rows":[{"row":2,"password":"secret"}]}`, `{"rows":[{"row":2,"class_raw":null}]}`, `{"rows":[]} {}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var dto LegacyImportDTO
		if decodeLegacyImport(r, &dto) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"rows":[{"row":2,"full_name":"A B","class_raw":"7","school_name":"","proctor_name":"","tg_name":""}]}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	var dto LegacyImportDTO
	if e := decodeLegacyImport(r, &dto); e != nil {
		t.Fatal(e)
	}
}
func TestLegacyReceiptDigestBindsLogicalOperationAndCAS(t *testing.T) {
	one := LegacyImportEditDTO{1, []LegacyImportRow{legacyRow(2, "A B")}}
	two := one
	two.ExpectedRevision = 2
	h := legacyCommandHash("edit", 7, one)
	if h == legacyCommandHash("edit", 8, one) || h == legacyCommandHash("commit", 7, one) || h == legacyCommandHash("edit", 7, two) {
		t.Fatal("receipt can cross operation/session/revision")
	}
	for _, session := range []LegacyImportSession{{Revision: 2, Status: "preview"}, {Revision: 1, Status: "completed"}, {Revision: 1, Status: "expired"}} {
		if legacyCAS(session, 1) == nil {
			t.Fatal("stale or closed import accepted")
		}
	}
	if legacyCAS(LegacyImportSession{Revision: 1, Status: "preview"}, 1) != nil {
		t.Fatal("fresh revision rejected")
	}
	for _, key := range []string{"", "fixed", "00000000-0000-0000-0000-000000000000", "ABCDEFAB-cdef-4abc-abcd-012345678901", "01234567-89ab-4def-8123-456789abcdef ", "01234567_89ab-4def-8123-456789abcdef"} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Idempotency-Key", key)
		if _, e := legacyCommandKey(r, true); e == nil {
			t.Fatalf("bad key accepted %q", key)
		}
	}
}

type legacySQLStep struct {
	kind, contains string
	args           []any
	cols           []string
	rows           [][]driver.Value
	inserted       int64
	err            error
}
type legacySQLConnector struct {
	steps                 []legacySQLStep
	committed, rolledBack bool
}
type legacySQLConn struct{ c *legacySQLConnector }
type legacySQLDriver struct{}
type legacySQLTx struct{ c *legacySQLConnector }
type legacySQLRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (c *legacySQLConnector) Connect(context.Context) (driver.Conn, error) {
	return &legacySQLConn{c}, nil
}
func (*legacySQLConnector) Driver() driver.Driver          { return legacySQLDriver{} }
func (legacySQLDriver) Open(string) (driver.Conn, error)   { return nil, driver.ErrSkip }
func (*legacySQLConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*legacySQLConn) Close() error                        { return nil }
func (c *legacySQLConn) Begin() (driver.Tx, error)         { return &legacySQLTx{c.c}, nil }
func (c *legacySQLConn) step(kind, q string, args []driver.NamedValue) (legacySQLStep, error) {
	if len(c.c.steps) == 0 {
		return legacySQLStep{}, fmt.Errorf("unexpected %s query", kind)
	}
	step := c.c.steps[0]
	c.c.steps = c.c.steps[1:]
	if step.kind != kind || !strings.Contains(q, step.contains) {
		return step, fmt.Errorf("expected %s %s", step.kind, step.contains)
	}
	if step.args != nil {
		values := []any{}
		for _, arg := range args {
			values = append(values, arg.Value)
		}
		if !reflect.DeepEqual(values, step.args) {
			return step, fmt.Errorf("scope/command arguments differ")
		}
	}
	return step, step.err
}
func (c *legacySQLConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	step, e := c.step("query", q, args)
	if e != nil {
		return nil, e
	}
	return &legacySQLRows{step.cols, step.rows, 0}, nil
}
func (c *legacySQLConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	step, e := c.step("exec", q, args)
	if e != nil {
		return nil, e
	}
	return legacySQLResult(step.inserted), nil
}

type legacySQLResult int64

func (r legacySQLResult) LastInsertId() (int64, error) { return int64(r), nil }
func (legacySQLResult) RowsAffected() (int64, error)   { return 1, nil }
func (t *legacySQLTx) Commit() error                   { t.c.committed = true; return nil }
func (t *legacySQLTx) Rollback() error                 { t.c.rolledBack = true; return nil }
func (r *legacySQLRows) Columns() []string             { return r.cols }
func (*legacySQLRows) Close() error                    { return nil }
func (r *legacySQLRows) Next(dest []driver.Value) error {
	if r.i == len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func TestLegacyLostCommitReplyReplaysBeforeHashAndCASAfterRightsRestore(t *testing.T) {
	key := "01234567-89ab-4def-8123-456789abcdef"
	digest := legacyCommandHash("commit", 7, LegacyImportCommitDTO{2})
	out := LegacyImportSession{ID: 7, ActorID: 3, OrganizationID: 1, DivisionID: 2, Revision: 3, Status: "completed", MaxRows: 100, Report: &LegacyImportReport{Created: []LegacyImportCreated{{AccountID: 91, ProfileID: 7, Role: "student", FullName: "A B"}}, CreatedStudents: 1, CredentialsPolicy: "reset_required"}}
	body, _ := json.Marshal(out)
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "organization_id=? AND division_id=? AND actor_id=? AND command_key=?", args: []any{int64(1), int64(2), int64(3), key}, cols: []string{"hash", "response", "code"}, rows: [][]driver.Value{{digest, body, int64(200)}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"expected_revision":2}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	r.SetPathValue("import", "7")
	deniedActor := Actor{AccountID: 3, OrganizationID: 1, Role: "staff_admin", DivisionID: legacyInt(2), Permissions: map[string]Permission{"upload": {true, true}, "users": {true, false}}}
	if _, _, e := s.legacyImportCommit(r, deniedActor, 2); e == nil || len(c.steps) != 1 {
		t.Fatal("revoked permission read/replayed receipt")
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"expected_revision":2}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	r.SetPathValue("import", "7")
	result, code, e := s.legacyImportCommit(r, legacyAdmin(), 2)
	if e != nil || code != 200 || result.(LegacyImportSession).Report.Created[0].AccountID != 91 || len(c.steps) != 0 {
		t.Fatalf("lost reply did not recover exact account: %v", e)
	}
	if strings.Contains(string(body), "password") || strings.Contains(string(body), "pbkdf2") || strings.Contains(string(body), "login") {
		t.Fatal("credential material in durable receipt")
	}
}
func TestLegacyApplyRollsBackAllSubjectWritesOnLaterFailure(t *testing.T) {
	count := func() legacySQLStep {
		return legacySQLStep{kind: "query", contains: "COUNT(*) FROM auth_users WHERE username=?", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}
	}
	steps := []legacySQLStep{count(), count(), {kind: "exec", contains: "INSERT INTO students", inserted: 101}, {kind: "query", contains: "COUNT(*) FROM auth_users WHERE role='student' AND ref_id=?", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, {kind: "exec", contains: "INSERT INTO auth_users", inserted: 201}}
	for _, table := range []string{"auth_account_state", "core_user_owners", "auth_organization_access", "auth_division_access", "core_audit"} {
		steps = append(steps, legacySQLStep{kind: "exec", contains: "INSERT INTO " + table})
	}
	steps = append(steps, legacySQLStep{kind: "query", contains: "SELECT ref_id", args: []any{int64(201), "student"}, cols: []string{"ref"}, rows: [][]driver.Value{{int64(101)}}}, count(), count(), legacySQLStep{kind: "exec", contains: "INSERT INTO students", err: errors.New("synthetic later failure")})
	c := &legacySQLConnector{steps: steps}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db}
	r := httptest.NewRequest("POST", "/", nil)
	p := legacyBuildPreview([]LegacyImportRow{legacyRow(2, "A B"), legacyRow(3, "C D")}, legacyEmptyIndex(), legacyAdmin())
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		_, e := s.legacyApply(r, tx, legacyAdmin(), 2, p, map[string]string{"student:2": "pbkdf2:sha256:600000$synthetic", "student:3": "pbkdf2:sha256:600000$synthetic"})
		return e
	})
	if e == nil || c.committed || !c.rolledBack || len(c.steps) != 0 {
		t.Fatalf("partial import escaped atomic transaction: %v", e)
	}
}
func TestLegacyLoadSessionPinsActorOrgDivisionAndExpiresPreview(t *testing.T) {
	preview, _ := json.Marshal(legacyBuildPreview([]LegacyImportRow{legacyRow(2, "A B")}, legacyEmptyIndex(), legacyAdmin()))
	payload, _ := json.Marshal(LegacyImportDTO{[]LegacyImportRow{legacyRow(2, "A B")}})
	now := time.Now().UTC()
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "id=? AND organization_id=? AND division_id=? AND actor_id=?", args: []any{int64(7), int64(1), int64(2), int64(3)}, cols: []string{"id", "org", "div", "actor", "revision", "status", "payload", "preview", "report", "created", "expires"}, rows: [][]driver.Value{{int64(7), int64(1), int64(2), int64(3), int64(1), "preview", payload, preview, nil, now.Add(-48 * time.Hour), now.Add(-24 * time.Hour)}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	r := httptest.NewRequest("GET", "/", nil)
	out, _, e := legacyLoadSession(r, db, legacyAdmin(), 2, 7, false)
	if e != nil || out.Status != "expired" || out.Preview.CanCommit || out.MaxRows != 100 {
		t.Fatal("expired/pinned preview incorrect", e)
	}
}

func TestCredentialPreviewLostReplyResolvesBeforeExistingLoginValidation(t *testing.T) {
	key := "01234567-89ab-4def-8123-456789abcdef"
	in := ImportDTO{Users: []UserDTO{{Role: "student", FullName: "A B", Class: 7, Login: "synthetic-login", Password: "not-logged-password"}}}
	digest := legacyCommandHash("credential_preview", 0, in)
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "core_import_receipts WHERE organization_id=? AND division_id=? AND actor_id=? AND command_key=?", args: []any{int64(1), int64(2), int64(3), key}, cols: []string{"hash", "session"}, rows: [][]driver.Value{{digest, int64(37)}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db}
	body, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	out, code, e := s.importPreview(r, legacyAdmin(), 2)
	if e != nil || code != 201 || out.(map[string]any)["id"] != int64(37) || len(c.steps) != 0 {
		t.Fatal("retry validated existing login or called hash/create", e)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), in.Users[0].Password) || strings.Contains(string(encoded), in.Users[0].Login) {
		t.Fatal("receipt reply leaked credentials")
	}
}
func TestCredentialPreviewChangedPayloadCannotReuseReceipt(t *testing.T) {
	key := "01234567-89ab-4def-8123-456789abcdef"
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "core_import_receipts", cols: []string{"hash", "session"}, rows: [][]driver.Value{{strings.Repeat("f", 64), int64(37)}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"users":[{"role":"student","full_name":"A B","class":7,"login":"changed-login","password":"secret-password"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	_, _, e := s.importPreview(r, legacyAdmin(), 2)
	var api *APIError
	if !errors.As(e, &api) || api.Code != 409 || api.Message != "idempotency_key_reused" || len(c.steps) != 0 {
		t.Fatal("receipt overwrite accepted", e)
	}
}
func TestCredentialReceiptReadIsPinnedAndContainsNoCredentialRows(t *testing.T) {
	key := "01234567-89ab-4def-8123-456789abcdef"
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "i.organization_id=c.organization_id AND i.division_id=c.division_id AND i.actor_id=c.actor_id", args: []any{int64(1), int64(2), int64(3), key}, cols: []string{"session_id", "status", "created_at"}, rows: [][]driver.Value{{int64(37), "completed", time.Now().UTC()}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("receipt", key)
	out, code, e := s.importReceiptGet(r, legacyAdmin(), 2)
	if e != nil || code != 200 {
		t.Fatal(e)
	}
	row := out.(map[string]any)
	if len(row) != 3 || num(row["session_id"]) != 37 {
		t.Fatal("unexpected private receipt projection")
	}
}
