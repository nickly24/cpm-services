package exams

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestImportNativePreviewPaginatesAll5000Rows(t *testing.T) {
	e, a, p := auxFixture()
	rows := make([]ImportRow, 5000)
	version := int64(17)
	for i := range rows {
		rows[i] = ImportRow{RowID: fmt.Sprintf("row-%d", i+2), SourceRow: i + 2, Input: json.RawMessage(`{"partCode":"A","questionText":"q","answerText":"a"}`), Errors: []string{}, PrivilegeBaselineVersion: &version, PrivilegeBaselineAccountID: 42}
	}
	rows[333].Errors = []string{"question_duplicate"}
	rows[4999].Excluded = true
	expires := p.Now.Add(ImportEditableRetention)
	hash := ""
	visited := map[string]bool{}
	for offset := 0; offset < 5000; offset += 200 {
		page, err := importPreview("session", 3, "editable", "questions", e, a, p, rows, expires, "bank.xlsx", "source", nil, ImportPage{Limit: 200, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 5000 || page.Offset != offset || page.Limit != 200 || len(page.Rows) != 200 || page.Summary.Errors != 1 || page.Summary.Valid != 4998 || page.Summary.Excluded != 1 {
			t.Fatal(page.Summary, page.Total, page.Offset, len(page.Rows))
		}
		if hash == "" {
			hash = page.ValidationHash
		} else if hash != page.ValidationHash {
			t.Fatal("same preview pages have different validation hash")
		}
		raw := auxJSON(t, page)
		if bytes.Contains(raw, []byte("privilege_baseline")) {
			t.Fatal("private CAS data leaked")
		}
		for _, row := range page.Rows {
			if visited[row.RowID] {
				t.Fatal("duplicate page row", row.RowID)
			}
			visited[row.RowID] = true
		}
		if offset < 4800 && (page.NextOffset == nil || *page.NextOffset != offset+200 || !page.HasMore) {
			t.Fatal("missing continuation")
		}
	}
	if len(visited) != 5000 {
		t.Fatal("incomplete native preview")
	}
	stale := int64(2)
	_, err := importPreview("session", 3, "editable", "questions", e, a, p, rows, expires, "bank.xlsx", "source", nil, ImportPage{Limit: 200, ExpectedRevision: &stale})
	auxError(t, err, "preview_modified")
	for _, query := range []map[string]string{{"limit": "201"}, {"limit": "0"}, {"offset": "-1"}, {"offset": "100001"}, {"expected_revision": "-1"}} {
		if _, err := importPage(Params{Query: query}); err == nil {
			t.Fatal("invalid pagination accepted", query)
		}
	}
	rows[333].Errors = nil
	changed, err := importPreview("session", 3, "editable", "questions", e, a, p, rows, expires, "bank.xlsx", "source", nil, ImportPage{Limit: 200})
	if err != nil || changed.ValidationHash == hash {
		t.Fatal("changed validation still has same hash", err)
	}
}
func TestNativeTemplatesUseCanonicalParserHeaders(t *testing.T) {
	for _, kind := range []string{"questions", "assignments", "outside"} {
		file, err := BuildImportTemplate(kind)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		entries := map[string]bool{}
		for _, entry := range archive.File {
			entries[entry.Name] = true
		}
		if !entries["[Content_Types].xml"] || !entries["_rels/.rels"] {
			t.Fatal("invalid Office package")
		}
		_, err = ParseXLSX(file, "template.xlsx", kind)
		auxError(t, err, "import_no_rows")
		headers, err := importTemplateHeaders(kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, header := range headers {
			if _, err = importHeader(header, kind); err != nil {
				t.Fatal("template differs from subject parser", kind, header, err)
			}
		}
	}
}
func TestImportPermissionUsesBothCanonicalCapabilities(t *testing.T) {
	actor := Actor{Role: "staff_admin", Permissions: map[string]Permission{"exams": {View: true, Edit: true}}}
	if importPermission(actor, false) || importPermission(actor, true) {
		t.Fatal("exams permission alone grants imports")
	}
	actor.Permissions["upload"] = Permission{View: true}
	if !importPermission(actor, false) || importPermission(actor, true) {
		t.Fatal("upload.view write permission")
	}
	actor.Permissions["upload"] = Permission{Edit: true}
	if !importPermission(actor, true) {
		t.Fatal("both edit permissions denied")
	}
	actor.Permissions["exams"] = Permission{View: true}
	if importPermission(actor, true) {
		t.Fatal("upload.edit overrides exams.view")
	}
}

func TestImportPreviewHashCanonicalizesRawInputs(t *testing.T) {
	e, a, p := auxFixture()
	rows := []ImportRow{{RowID: "row-2", SourceRow: 2, Input: json.RawMessage(`{"studentId":9007199254740993,"grade":5}`), Errors: []string{}}}
	first, err := importPreview("session", 1, "editable", "outside", e, a, p, rows, p.Now.Add(time.Hour), "source.xlsx", "sha", nil, ImportPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	rows[0].Input = json.RawMessage(`{"grade": 5, "studentId": 9007199254740993}`)
	next, err := importPreview("session", 1, "editable", "outside", e, a, p, rows, p.Now.Add(time.Hour), "source.xlsx", "sha", nil, ImportPage{Limit: 100})
	if err != nil || next.ValidationHash != first.ValidationHash {
		t.Fatal("SQL JSON normalization changed validation hash", err)
	}
	rows[0].Input = json.RawMessage(`{"grade":5,"studentId":9007199254740992}`)
	next, err = importPreview("session", 1, "editable", "outside", e, a, p, rows, p.Now.Add(time.Hour), "source.xlsx", "sha", nil, ImportPage{Limit: 100})
	if err != nil || next.ValidationHash == first.ValidationHash {
		t.Fatal("large integer precision lost in validation hash", err)
	}
}

func TestImportExcludedOnlyCommitDoesNotChangeSubject(t *testing.T) {
	e, a, p := auxFixture()
	before, err := encodeExam(e)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CommitImport(e, a, "questions", p, []ImportRow{{RowID: "row-2", SourceRow: 2, Excluded: true, Input: json.RawMessage(`{"unrecognized":"excluded"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	response := result.(map[string]any)
	if response["changed"] != false || response["rating_changed"] != false || response["committed"] != true {
		t.Fatal("excluded-only commit reported a subject change", result)
	}
	after, err := encodeExam(e)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("excluded-only commit mutated subject or allocator", err)
	}
}

func TestStoredTrainingSettingsFailClosed(t *testing.T) {
	for _, raw := range []string{`{"batch_size":20,"study_mode":"all"}`, ``} {
		if _, err := storedTrainingSettings([]byte(raw)); err != nil {
			t.Fatal("valid stored settings rejected", err)
		}
	}
	for _, raw := range []string{`null`, `{"batch_size":15}`, `{"study_mode":"unknown"}`, `{"batch_size":10,"batch_size":20}`, `{"unexpected":1}`, `{`} {
		_, err := storedTrainingSettings([]byte(raw))
		auxError(t, err, "invalid_training_settings")
	}
}

func TestQuestionImportStaffNeedsAllConnectedDivisions(t *testing.T) {
	e, _, p := auxFixture()
	e.Divisions[3] = &Division{ID: 3, Revision: 1}
	staff := Actor{AccountID: 10, OrganizationID: 1, Role: "staff_admin", DivisionIDs: []int64{2}, Permissions: map[string]Permission{"exams": {Edit: true}}}
	rows := []ImportRow{auxRow(t, "row-2", map[string]any{"partCode": "A", "questionText": "new question", "answerText": "new answer"})}
	_, err := CommitImport(e, staff, "questions", p, rows)
	auxError(t, err, "forbidden")
	if len(e.Questions) != 1 {
		t.Fatal("partial division access mutated shared content")
	}
	staff.DivisionIDs = []int64{2, 3}
	p.DivisionID = 3
	_, err = CommitImport(e, staff, "questions", p, rows)
	auxError(t, err, "forbidden")
	p.DivisionID = 2
	if _, err = CommitImport(e, staff, "questions", p, rows); err != nil || len(e.Questions) != 2 {
		t.Fatal("fully scoped staff could not import shared questions", err)
	}
}

type auditIdentity struct {
	actor   Actor
	members map[int64]Member
}

func (a *auditIdentity) Identify(*http.Request) (Actor, error) { return a.actor, nil }
func (a *auditIdentity) References(*http.Request, int64, []int64, int64, []int64) (map[int64]Member, error) {
	return a.members, nil
}
func (a *auditIdentity) Ready(*http.Request) error { return nil }

// Opt-in exercises the actual HTTP handlers and SQL transactions in connection
// local temporary tables. It never writes another service's data or connects to
// auth/core; the scoped identity/reference fixture is supplied in-process.
func TestImportHTTPAndRetentionLocalIntegration(t *testing.T) {
	if os.Getenv("CPM_EXAMS_AUX_INTEGRATION") != "1" {
		t.Skip("opt-in isolated local MySQL HTTP/retention integration")
	}
	values := maintenanceTestEnv(t)
	if values["MYSQL_DATABASE"] != "cpm_dev" || values["MYSQL_HOST"] != "127.0.0.1" || values["MYSQL_PORT"] != "13306" {
		t.Fatal("requires exact local cpm_dev database")
	}
	config := mysql.NewConfig()
	config.User = values["MYSQL_USER"]
	config.Passwd = values["MYSQL_PASSWORD"]
	config.Net = "tcp"
	config.Addr = "127.0.0.1:13306"
	config.DBName = "cpm_dev"
	config.ParseTime = true
	config.Loc = time.UTC
	config.Timeout = 3 * time.Second
	config.Params = map[string]string{"time_zone": "'+00:00'"}
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal("local MySQL open failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if db.PingContext(ctx) != nil {
		t.Fatal("local MySQL unavailable")
	}
	for _, table := range []string{"exams_catalog_v1", "exams_receipts_v1", "exams_imports_v1", "exams_source_v1", "exams_outbox_v1", "exams_training_settings_v1", "exams_training_progress_v1"} {
		var name, ddl string
		if db.QueryRowContext(ctx, "SHOW CREATE TABLE "+table).Scan(&name, &ddl) != nil || name != table {
			t.Fatal("read local Exams schema failed")
		}
		ddl = strings.Replace(ddl, "CREATE TABLE ", "CREATE TEMPORARY TABLE ", 1)
		// MySQL forbids foreign keys on TEMPORARY tables. Domain scope and creation
		// order still use the real handlers; the deployed FK is not disabled.
		lines := []string{}
		for _, line := range strings.Split(ddl, "\n") {
			if !strings.Contains(line, "CONSTRAINT ") {
				lines = append(lines, line)
			}
		}
		ddl = strings.Replace(strings.Join(lines, "\n"), ",\n)", "\n)", 1)
		if _, err = db.ExecContext(ctx, ddl); err != nil {
			t.Fatal("create isolated HTTP SQL schema failed")
		}
	}
	var columnCount int
	if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='exams_imports_v1' AND column_name='source_hash'").Scan(&columnCount) != nil {
		t.Fatal("inspect migration metadata failed")
	}
	if columnCount == 0 {
		for _, ddl := range []string{"ALTER TABLE exams_imports_v1 ADD COLUMN source_filename VARCHAR(255) NOT NULL DEFAULT ''", "ALTER TABLE exams_imports_v1 ADD COLUMN source_hash CHAR(64) NOT NULL DEFAULT ''"} {
			if _, err = db.ExecContext(ctx, ddl); err != nil {
				t.Fatal("add isolated import metadata failed")
			}
		}
	}
	if _, err = db.ExecContext(ctx, "CREATE TEMPORARY TABLE exams_aux_integration_guard(token CHAR(36))"); err != nil {
		t.Fatal("create HTTP SQL guard failed")
	}
	guard := UUID()
	if _, err = db.ExecContext(ctx, "INSERT INTO exams_aux_integration_guard(token) VALUES(?)", guard); err != nil {
		t.Fatal("seed HTTP SQL guard failed")
	}
	checkGuard := func() {
		t.Helper()
		var token string
		if db.QueryRowContext(ctx, "SELECT token FROM exams_aux_integration_guard").Scan(&token) != nil || token != guard {
			t.Fatal("SQL connection changed; refusing live table access")
		}
	}
	e, a, p := auxFixture()
	e.OrganizationID = 9_000_000_100_001
	a.OrganizationID = e.OrganizationID
	a.DivisionID = ptr(2)
	a.DivisionIDs = []int64{2}
	e.Questions = nil
	checkGuard()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("begin fixture transaction failed")
	}
	if create(ctx, tx, e) != nil {
		_ = tx.Rollback()
		t.Fatal("create isolated exam failed")
	}
	if tx.Commit() != nil {
		t.Fatal("commit isolated exam failed")
	}
	identity := &auditIdentity{actor: a, members: p.Members}
	server := (&Server{DB: db, Deps: identity}).Handler()
	base := fmt.Sprintf("/v1/exams/%d/imports/questions", e.ID)
	request := func(method, path string, content []byte, key, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		checkGuard()
		r := httptest.NewRequest(method, path, bytes.NewReader(content))
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		checkGuard()
		return w
	}
	preview := func(w *httptest.ResponseRecorder) ImportPreview {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("preview HTTP %d: %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data ImportPreview `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil {
			t.Fatal("invalid native preview JSON")
		}
		if bytes.Contains(w.Body.Bytes(), []byte("privilege_baseline")) {
			t.Fatal("private baseline in HTTP response")
		}
		return envelope.Data
	}
	valuesRows := make([][]string, 5000)
	for i := range valuesRows {
		valuesRows[i] = []string{"A", fmt.Sprintf("question-%d", i), "synthetic"}
	}
	file := auxXLSX(t, auxSheet([]string{"part_code", "question_text", "answer_text"}, valuesRows))
	sourceHash := sha256.Sum256(file)
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "synthetic-bank.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(file)
	_ = writer.Close()
	uploadKey := UUID()
	first := preview(request("POST", base+"?limit=200", upload.Bytes(), uploadKey, writer.FormDataContentType()))
	if first.Total != 5000 || len(first.Rows) != 200 || first.SourceHash != hex.EncodeToString(sourceHash[:]) || first.Summary.Valid != 5000 {
		t.Fatal("upload page incomplete", first.Total, first.Summary)
	}
	if delta := time.Until(first.ExpiresAt); delta < 71*time.Hour || delta > 73*time.Hour {
		t.Fatal("incorrect editable TTL")
	}
	repeated := preview(request("POST", base+"?limit=200", upload.Bytes(), uploadKey, writer.FormDataContentType()))
	if repeated.ID != first.ID {
		t.Fatal("lost upload reply created another session")
	}
	seen := map[string]bool{}
	for offset := 0; offset < 5000; offset += 200 {
		page := preview(request("GET", fmt.Sprintf("%s/%s?limit=200&offset=%d&expected_revision=1", base, first.ID, offset), nil, "", ""))
		if page.Total != 5000 || page.ValidationHash != first.ValidationHash || page.Revision != 1 {
			t.Fatalf("paging snapshot changed: total %d, revision %d, hash %s vs %s", page.Total, page.Revision, page.ValidationHash, first.ValidationHash)
		}
		for _, row := range page.Rows {
			if seen[row.RowID] {
				t.Fatal("repeated native row")
			}
			seen[row.RowID] = true
		}
	}
	if len(seen) != 5000 {
		t.Fatal("5000-row preview truncated")
	}
	identity.actor.AccountID = 99
	if code := request("GET", base+"/"+first.ID, nil, "", "").Code; code != 404 {
		t.Fatal("other account read preview", code)
	}
	if code := request("PUT", base+"/"+first.ID, []byte(`{"expected_revision":1,"changes":[]}`), UUID(), "application/json").Code; code != 404 {
		t.Fatal("other account edited preview", code)
	}
	identity.actor = a
	identity.actor.DivisionID = ptr(3)
	if code := request("GET", base+"/"+first.ID, nil, "", "").Code; code != 404 {
		t.Fatal("other division read preview", code)
	}
	identity.actor = a
	identity.actor.OrganizationID++
	if code := request("GET", base+"/"+first.ID, nil, "", "").Code; code != 404 {
		t.Fatal("other organization read preview", code)
	}
	if code := request("GET", base+"/template", nil, "", "").Code; code != 404 {
		t.Fatal("other organization downloaded template", code)
	}
	identity.actor = a
	template := request("GET", base+"/template", nil, "", "")
	if template.Code != 200 || !strings.Contains(template.Header().Get("Content-Disposition"), "exams-questions.xlsx") {
		t.Fatal("native template unavailable", template.Code)
	}
	identity.actor.Role = "student"
	identity.actor.OrganizationAdmin = false
	if code := request("GET", base+"/template", nil, "", "").Code; code != 403 {
		t.Fatal("student downloaded template", code)
	}
	identity.actor = a
	identity.actor.Role = "staff_admin"
	identity.actor.OrganizationAdmin = false
	identity.actor.Permissions = map[string]Permission{"exams": {View: true, Edit: true}, "upload": {View: true}}
	if code := request("POST", base+"?limit=200", upload.Bytes(), uploadKey, writer.FormDataContentType()).Code; code != 403 {
		t.Fatal("revoked upload edit permission replayed upload receipt", code)
	}
	identity.actor.Permissions["exams"] = Permission{View: true}
	identity.actor.Permissions["upload"] = Permission{Edit: true}
	if code := request("PUT", base+"/"+first.ID, []byte(`{"expected_revision":1,"changes":[]}`), UUID(), "application/json").Code; code != 403 {
		t.Fatal("upload edit permission overrode revoked exams edit", code)
	}
	identity.actor = a
	changed := preview(request("PUT", base+"/"+first.ID+"?limit=200", []byte(`{"expected_revision":1,"changes":[{"row_id":"row-2","input":{"partCode":"A","questionText":"question-1","answerText":"synthetic"}}]}`), UUID(), "application/json"))
	if changed.Revision != 2 || changed.Summary.Errors != 2 || len(changed.ErrorReport) != 2 || changed.SourceHash != first.SourceHash {
		t.Fatal("edit/duplicate report incorrect", changed.Summary)
	}
	if code := request("GET", base+"/"+first.ID+"?expected_revision=1", nil, "", "").Code; code != 409 {
		t.Fatal("stale page accepted", code)
	}
	fixed := preview(request("PUT", base+"/"+first.ID+"?limit=200", []byte(`{"expected_revision":2,"changes":[{"row_id":"row-3","excluded":true}]}`), UUID(), "application/json"))
	if fixed.Revision != 3 || fixed.Summary.Valid != 4999 || fixed.Summary.Errors != 0 || fixed.Summary.Excluded != 1 {
		t.Fatal("exclusion incorrect", fixed.Summary)
	}
	commitURL := base + "/" + first.ID + "/commit"
	commitBody := []byte(`{"expected_revision":3}`)
	commitKey := UUID()
	committed := request("POST", commitURL, commitBody, commitKey, "application/json")
	if committed.Code != 200 {
		t.Fatal("native commit failed", committed.Code, committed.Body.String())
	}
	// Reconstruct the server to prove recovery comes from durable SQL receipts,
	// rather than process memory or an already-held preview object.
	server = (&Server{DB: db, Deps: identity}).Handler()
	replayed := request("POST", commitURL, commitBody, commitKey, "application/json")
	if replayed.Code != 200 || replayed.Body.String() != committed.Body.String() {
		t.Fatal("lost commit reply not recovered identically")
	}
	duplicate := request("POST", commitURL, commitBody, UUID(), "application/json")
	if duplicate.Code != 200 || duplicate.Body.String() != committed.Body.String() {
		t.Fatal("committed session re-applied")
	}
	identity.actor.Role = "staff_admin"
	identity.actor.OrganizationAdmin = false
	identity.actor.Permissions = map[string]Permission{"exams": {Edit: true}, "upload": {View: true}}
	if code := request("POST", commitURL, commitBody, commitKey, "application/json").Code; code != 403 {
		t.Fatal("revoked permissions replayed committed receipt", code)
	}
	identity.actor = a
	current, err := load(ctx, db, e.OrganizationID, e.ID, false)
	if err != nil || len(current.Questions) != 4999 {
		t.Fatal("duplicate commit changed exam")
	}
	var events int
	if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exams_outbox_v1").Scan(&events) != nil || events != 0 || current.Divisions[2].RatingRevision != 1 {
		t.Fatal("question import changed the effective rating source")
	}
	seed := func(fixture *Exam) {
		t.Helper()
		checkGuard()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal("begin isolated fixture transaction failed")
		}
		if err = create(ctx, tx, fixture); err != nil {
			_ = tx.Rollback()
			t.Fatal("create isolated auxiliary fixture failed")
		}
		if tx.Commit() != nil {
			t.Fatal("commit isolated auxiliary fixture failed")
		}
	}
	training, _, _ := auxFixture()
	training.OrganizationID = e.OrganizationID
	training.Divisions[3] = &Division{ID: 3, Revision: 1, RatingRevision: 1, Config: Config{Revision: 1}, TrainingEnabled: true}
	seed(training)
	trainingURL := fmt.Sprintf("/v1/exams/%d/training", training.ID)
	student := Actor{AccountID: 11, OrganizationID: e.OrganizationID, Role: "student", DivisionID: ptr(2), DivisionIDs: []int64{2, 3}}
	identity.actor = student
	markKey := UUID()
	markBody := []byte(fmt.Sprintf(`{"content_fingerprint":"%s"}`, TrainingFingerprint("q", "a")))
	if code := request("PUT", trainingURL+"/progress/5", markBody, markKey, "application/json").Code; code != 200 {
		t.Fatal("student could not mark scoped card", code)
	}
	trainingView := func(path string) (TrainingStats, TrainingSettings) {
		t.Helper()
		w := request("GET", path, nil, "", "")
		if w.Code != 200 {
			t.Fatal("training view failed", w.Code, w.Body.String())
		}
		var envelope struct {
			Data struct {
				Stats    TrainingStats    `json:"stats"`
				Settings TrainingSettings `json:"settings"`
			} `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil {
			t.Fatal("invalid training view")
		}
		return envelope.Data.Stats, envelope.Data.Settings
	}
	stats, _ := trainingView(trainingURL)
	if stats.Learned != 1 {
		t.Fatal("scoped progress not visible")
	}
	identity.actor.DivisionID = ptr(3)
	stats, _ = trainingView(trainingURL)
	if stats.Learned != 0 {
		t.Fatal("training progress crossed divisions")
	}
	identity.actor = student
	identity.actor.AccountID = 12
	stats, _ = trainingView(trainingURL)
	if stats.Learned != 0 {
		t.Fatal("training progress crossed accounts")
	}
	identity.actor.OrganizationID++
	if code := request("GET", trainingURL, nil, "", "").Code; code != 404 {
		t.Fatal("training progress crossed organizations", code)
	}
	identity.actor = student
	if code := request("PUT", trainingURL+"/settings?part_id=4", []byte(`{"batch_size":20,"last_part_id":4}`), UUID(), "application/json").Code; code != 200 {
		t.Fatal("part settings update failed", code)
	}
	if code := request("PUT", trainingURL+"/settings", []byte(`{"last_part_id":null}`), UUID(), "application/json").Code; code != 200 {
		t.Fatal("root settings update failed", code)
	}
	_, selected := trainingView(trainingURL + "?part_id=4")
	if selected.LastPartID != nil || selected.BatchSize != 20 {
		t.Fatal("part view did not use root last selection", selected)
	}
	identity.actor = a
	if code := request("PUT", trainingURL, []byte(`{"training_enabled":false,"expected_revision":1}`), UUID(), "application/json").Code; code != 200 {
		t.Fatal("disable training failed", code)
	}
	identity.actor = student
	if code := request("PUT", trainingURL+"/progress/5", markBody, markKey, "application/json").Code; code != 409 {
		t.Fatal("disabled training replayed old learned receipt", code)
	}
	trained, err := load(ctx, db, e.OrganizationID, training.ID, false)
	if err != nil || trained.Divisions[2].RatingRevision != 1 {
		t.Fatal("training changed effective rating revision")
	}
	identity.actor = a
	external, _, _ := auxFixture()
	external.OrganizationID = e.OrganizationID
	external.Kind = "outside_lms"
	external.Divisions[2].Date = "2026-10-09"
	seed(external)
	externalURL := fmt.Sprintf("/v1/exams/%d/imports/outside", external.ID)
	outsideRow := auxRow(t, "row-2", map[string]any{"studentId": 11, "points": 12.5, "grade": 0, "examinator": "Synthetic"})
	insertSession := func(excluded bool) string {
		t.Helper()
		row := outsideRow
		row.Excluded = excluded
		rowsJSON, err := json.Marshal([]ImportRow{row})
		if err != nil {
			t.Fatal(err)
		}
		id := UUID()
		if _, err = db.ExecContext(ctx, "INSERT INTO exams_imports_v1(id,organization_id,division_id,account_id,exam_id,kind,rows_json,expires_at) VALUES(?,?,?,?,?,?,?,?)", id, e.OrganizationID, 2, a.AccountID, external.ID, "outside", rowsJSON, time.Now().Add(time.Hour)); err != nil {
			t.Fatal("insert isolated outside preview failed")
		}
		return id
	}
	applySession := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		w := request("POST", externalURL+"/"+id+"/commit", []byte(`{"expected_revision":1}`), UUID(), "application/json")
		if w.Code != 200 {
			t.Fatal("outside commit failed", w.Code, w.Body.String())
		}
		return w
	}
	applySession(insertSession(true))
	unchanged, err := load(ctx, db, e.OrganizationID, external.ID, false)
	if err != nil || unchanged.Revision != 1 || unchanged.Divisions[2].RatingRevision != 1 || len(unchanged.Divisions[2].Outside) != 0 {
		t.Fatal("excluded-only HTTP commit mutated subject")
	}
	if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exams_outbox_v1").Scan(&events) != nil || events != 0 {
		t.Fatal("excluded-only commit emitted rating event")
	}
	applySession(insertSession(false))
	updated, err := load(ctx, db, e.OrganizationID, external.ID, false)
	if err != nil || updated.Revision != 2 || updated.Divisions[2].RatingRevision != 2 || len(updated.Divisions[2].Outside) != 1 {
		t.Fatal("outside import did not advance selected rating revision")
	}
	if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exams_outbox_v1 WHERE organization_id=? AND division_id=2", e.OrganizationID).Scan(&events) != nil || events != 1 {
		t.Fatal("outside import did not emit exactly one scoped source event")
	}
	if _, err = db.ExecContext(ctx, "UPDATE exams_imports_v1 SET expires_at=? WHERE id=?", time.Now().Add(-time.Second), first.ID); err != nil {
		t.Fatal("expire isolated session failed")
	}
	if code := request("POST", commitURL, commitBody, commitKey, "application/json").Code; code != 410 {
		t.Fatal("receipt bypassed expired session", code)
	}
	if _, err = db.ExecContext(ctx, "UPDATE exams_receipts_v1 SET created_at=? WHERE organization_id=?", time.Now().Add(-8*24*time.Hour), e.OrganizationID); err != nil {
		t.Fatal("expire isolated receipts failed")
	}
	if code := request("POST", base+"?limit=200", upload.Bytes(), uploadKey, writer.FormDataContentType()).Code; code != 410 {
		t.Fatal("logical receipt expiry depended on cleanup", code)
	}
	cleanup, err := CleanupRetention(ctx, db, time.Now(), 1)
	if err != nil || cleanup.ImportsDeleted != 1 || cleanup.ReceiptsRetired != 1 {
		t.Fatal("cleanup not bounded", cleanup, err)
	}
	if code := request("GET", base+"/"+first.ID, nil, "", "").Code; code != 404 {
		t.Fatal("purged expired session was recoverable", code)
	}
	if code := request("POST", commitURL, commitBody, commitKey, "application/json").Code; code != 404 {
		t.Fatal("purged expired session replayed commit receipt", code)
	}
	for i := 0; i < 100; i++ {
		out, err := CleanupRetention(ctx, db, time.Now(), 1)
		if err != nil {
			t.Fatal("cleanup failed")
		}
		if out.ReceiptsRetired == 0 {
			break
		}
	}
	if code := request("POST", base+"?limit=200", upload.Bytes(), uploadKey, writer.FormDataContentType()).Code; code != 410 {
		t.Fatal("retired old upload key created another session", code)
	}
	var receipts, responses int
	if db.QueryRowContext(ctx, "SELECT COUNT(*),COUNT(response) FROM exams_receipts_v1 WHERE organization_id=?", e.OrganizationID).Scan(&receipts, &responses) != nil || receipts < 4 || responses != 0 {
		t.Fatal("cleanup removed tombstones or retained response payloads")
	}
	current, err = load(ctx, db, e.OrganizationID, e.ID, false)
	if err != nil || len(current.Questions) != 4999 {
		t.Fatal("expired retry changed subject data")
	}
	t.Log("native5000-row paging, source hash, templates, scope, edits, lost replies, session expiry and permanent receipt tombstones passed")
}
