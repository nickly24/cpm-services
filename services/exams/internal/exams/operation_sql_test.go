package exams

import (
	"bytes"
	"context"
	"database/sql"
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

func operationIsolatedDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	values := maintenanceTestEnv(t)
	if values["MYSQL_DATABASE"] != "cpm_dev" || values["MYSQL_HOST"] != "127.0.0.1" || values["MYSQL_PORT"] != "13306" {
		t.Fatal("requires exact loopback cpm_dev")
	}
	cfg := mysql.NewConfig()
	cfg.User = values["MYSQL_USER"]
	cfg.Passwd = values["MYSQL_PASSWORD"]
	cfg.Net = "tcp"
	cfg.Addr = "127.0.0.1:13306"
	cfg.DBName = "cpm_dev"
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 3 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	cfg.Params = map[string]string{"time_zone": "'+00:00'"}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open isolated database failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if db.PingContext(ctx) != nil {
		t.Fatal("local MySQL unavailable")
	}
	for _, table := range []string{"exams_catalog_v1", "exams_imports_v1", "exams_receipts_v1", "exams_source_v1", "exams_outbox_v1"} {
		var name, ddl string
		if db.QueryRowContext(ctx, "SHOW CREATE TABLE "+table).Scan(&name, &ddl) != nil || name != table || !strings.HasPrefix(ddl, "CREATE TABLE `"+table+"`") {
			t.Fatal("read isolated schema failed")
		}
		ddl = strings.Replace(ddl, "CREATE TABLE ", "CREATE TEMPORARY TABLE ", 1)
		lines := []string{}
		for _, line := range strings.Split(ddl, "\n") {
			if !strings.Contains(line, "CONSTRAINT ") {
				lines = append(lines, line)
			}
		}
		ddl = strings.Replace(strings.Join(lines, "\n"), ",\n)", "\n)", 1)
		if _, err = db.ExecContext(ctx, ddl); err != nil {
			t.Fatal("create connection-local temporary table failed")
		}
	}
	if _, err = db.ExecContext(ctx, "CREATE TEMPORARY TABLE exams_operation_test_guard(token CHAR(36))"); err != nil {
		t.Fatal("create connection guard failed")
	}
	guard := UUID()
	if _, err = db.ExecContext(ctx, "INSERT INTO exams_operation_test_guard VALUES(?)", guard); err != nil {
		t.Fatal("seed connection guard failed")
	}
	check := func() {
		t.Helper()
		var found string
		if db.QueryRow("SELECT token FROM exams_operation_test_guard").Scan(&found) != nil || found != guard {
			t.Fatal("SQL connection changed; refusing live table access")
		}
	}
	check()
	return db, check
}

func TestOperationImportLocalSQLIntegration(t *testing.T) {
	if os.Getenv("CPM_EXAMS_OPERATIONS_INTEGRATION") != "1" {
		t.Skip("opt-in connection-local temporary MySQL grant integration")
	}
	db, guard := operationIsolatedDB(t)
	ctx := context.Background()
	checks := 0
	for _, kind := range []string{"questions", "assignments", "outside"} {
		t.Run(kind, func(t *testing.T) {
			guard()
			e, _, p := auxFixture()
			e.OrganizationID = 9_000_000_100_002
			e.Divisions[3] = &Division{ID: 3, Revision: 1, RatingRevision: 1, Config: Config{Revision: 1}}
			if kind == "outside" {
				e.Kind = "outside_lms"
			}
			a := Actor{AccountID: 10, OrganizationID: e.OrganizationID, Role: "staff_admin", DivisionID: ptr(2), DivisionIDs: []int64{2, 3}, Permissions: map[string]Permission{"exams": {View: true, Edit: true}, "upload": {View: true, Edit: true}}}
			var rows []ImportRow
			switch kind {
			case "questions":
				rows = []ImportRow{auxRow(t, "row-2", map[string]any{"partCode": "A", "questionText": "new question", "answerText": "answer"})}
			case "assignments":
				rows = []ImportRow{auxRow(t, "row-2", map[string]any{"studentId": 11, "examinator1Id": 13, "replacementLimit": 2})}
			case "outside":
				rows = []ImportRow{auxRow(t, "row-2", map[string]any{"studentId": 11, "points": 123.25, "grade": 0, "examinator": "External"})}
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = create(ctx, tx, e); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if tx.Commit() != nil {
				t.Fatal("seed isolated exam failed")
			}
			checked, err := ValidateImport(e, a, kind, p, rows)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range checked {
				if len(row.Errors) != 0 {
					t.Fatal("invalid test fixture", row.Errors)
				}
			}
			hash, err := importValidationHash(checked)
			if err != nil {
				t.Fatal(err)
			}
			session, key, job := UUID(), UUID(), strings.Repeat("b", 32)
			source := strings.Repeat("c", 64)
			encoded, _ := json.Marshal(checked)
			_, err = db.ExecContext(ctx, "INSERT INTO exams_imports_v1(id,organization_id,division_id,account_id,exam_id,kind,rows_json,expires_at,source_filename,source_hash) VALUES(?,?,?,?,?,?,?,?,?,?)", session, a.OrganizationID, 2, a.AccountID, e.ID, kind, encoded, time.Now().UTC().Add(time.Hour), "synthetic.xlsx", source)
			if err != nil {
				t.Fatal("seed isolated import failed")
			}
			base := fmt.Sprintf("/v1/exams/%d/imports/%s/%s", e.ID, kind, session)
			body := []byte("{\"expected_revision\":1}\n")
			original := httptest.NewRequest("POST", base+"/commit", nil)
			binding := OperationBinding{OrganizationID: a.OrganizationID, AccountID: a.AccountID, DivisionID: 2, ExamID: e.ID, Kind: kind, SessionID: session, OwnerRevision: 1, TargetRevision: e.Revision, SourceHash: source, ValidationHash: hash, NativeCommandKey: key, NativeRequestHash: requestHash(original, a, body), ConnectedDivisionIDs: []int64{2, 3}}
			live := a
			permissions := map[int64]Permission{2: {Edit: true}, 3: {Edit: true}}
			var authErr error
			identity := &grantIdentity{auditIdentity: auditIdentity{actor: a, members: p.Members}, answer: func(_ *http.Request, in OperationRequest) (OperationAuthorization, error) {
				return OperationAuthorization{Request: in, Actor: live, Binding: binding, DivisionPermissions: permissions}, authErr
			}}
			server := (&Server{DB: db, Deps: identity, OperationKey: strings.Repeat("d", 32)}).Handler()
			request := func(stage, path string, content []byte) *httptest.ResponseRecorder {
				t.Helper()
				guard()
				method := "GET"
				if stage == "apply" {
					method = "POST"
				}
				r := httptest.NewRequest(method, path, bytes.NewReader(content))
				r.Header.Set(operationGrantHeader, strings.Repeat("a", 32))
				r.Header.Set(operationStageHeader, stage)
				r.Header.Set(operationJobHeader, job)
				r.Header.Set(operationDeliveryHeader, strings.Repeat("d", 32))
				r.Header.Set("Idempotency-Key", key)
				if stage == "apply" {
					r.Header.Set("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				guard()
				checks++
				return w
			}
			assertStatus := func(w *httptest.ResponseRecorder, want int) {
				t.Helper()
				if w.Code != want {
					t.Fatalf("HTTP %d want %d: %s", w.Code, want, w.Body.String())
				}
			}
			receiptURL := base + "/receipts/" + key
			assertStatus(request("receipt", receiptURL, nil), 404)
			assertStatus(request("preview", base+"?expected_revision=1&limit=200&offset=0", nil), 200)
			binding.TargetRevision++
			assertStatus(request("apply", base+"/commit", body), 409)
			binding.TargetRevision--
			binding.SourceHash = strings.Repeat("f", 64)
			assertStatus(request("apply", base+"/commit", body), 409)
			binding.SourceHash = source
			binding.ValidationHash = strings.Repeat("e", 64)
			assertStatus(request("apply", base+"/commit", body), 409)
			binding.ValidationHash = hash
			if kind != "questions" {
				originalMember := identity.members[11]
				revoked := originalMember
				revoked.Active = false
				identity.members[11] = revoked
				assertStatus(request("apply", base+"/commit", body), 409)
				identity.members[11] = originalMember
			}
			live.Permissions = map[string]Permission{"exams": {Edit: true}, "upload": {View: true}}
			assertStatus(request("apply", base+"/commit", body), 403)
			live = a
			if kind == "questions" {
				permissions[3] = Permission{View: true}
				assertStatus(request("apply", base+"/commit", body), 403)
				permissions[3] = Permission{Edit: true}
			}
			for _, denial := range []error{&APIError{410, "operation_expired"}, &APIError{410, "operation_revoked"}, &APIError{503, "authorization_unavailable"}} {
				authErr = denial
				assertStatus(request("apply", base+"/commit", body), denial.(*APIError).Status)
			}
			authErr = nil
			var receipts int
			if db.QueryRow("SELECT COUNT(*) FROM exams_receipts_v1 WHERE idempotency_key=?", key).Scan(&receipts) != nil || receipts != 0 {
				t.Fatal("denied apply retained an executed receipt")
			}
			if _, err = db.Exec("UPDATE exams_imports_v1 SET expires_at=? WHERE id=?", time.Now().UTC().Add(-time.Minute), session); err != nil {
				t.Fatal(err)
			}
			assertStatus(request("apply", base+"/commit", body), 410)
			if _, err = db.Exec("UPDATE exams_imports_v1 SET expires_at=? WHERE id=?", time.Now().UTC().Add(time.Hour), session); err != nil {
				t.Fatal(err)
			}
			committed := request("apply", base+"/commit", body)
			assertStatus(committed, 200)
			// Rebuild the handler and drop the first response: durable matching replay,
			// not process memory, must return exactly the initial result.
			server = (&Server{DB: db, Deps: identity, OperationKey: strings.Repeat("d", 32)}).Handler()
			for i := 0; i < 3; i++ {
				replayed := request("apply", base+"/commit", body)
				assertStatus(replayed, 200)
				if replayed.Body.String() != committed.Body.String() {
					t.Fatal("duplicate response differs")
				}
			}
			recovered := request("receipt", receiptURL, nil)
			assertStatus(recovered, 200)
			if recovered.Body.String() != committed.Body.String() {
				t.Fatal("receipt differs from original result")
			}
			// Fresh dependencies now mark imported rows as duplicates. Historical
			// recovery must not treat those rows as a new application.
			current, err := load(ctx, db, a.OrganizationID, e.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "questions":
				if len(current.Questions) != 2 {
					t.Fatal("question applied more than once")
				}
			case "assignments":
				if len(current.Divisions[2].Assignments) != 1 || len(current.Divisions[2].Commissions) != 1 {
					t.Fatal("assignment applied more than once")
				}
			case "outside":
				if len(current.Divisions[2].Outside) != 1 || current.Divisions[2].Outside[0].Grade != 0 || current.Divisions[2].Outside[0].Points != "123.25" {
					t.Fatal("outside result changed")
				}
			}
			live.Permissions = map[string]Permission{"exams": {View: true}, "upload": {Edit: true}}
			assertStatus(request("receipt", receiptURL, nil), 403)
			live = a
			binding.ValidationHash = strings.Repeat("e", 64)
			assertStatus(request("receipt", receiptURL, nil), 409)
			binding.ValidationHash = hash
			oldAccount, oldLive := binding.AccountID, live.AccountID
			binding.AccountID = 99
			live.AccountID = 99
			assertStatus(request("receipt", receiptURL, nil), 404)
			binding.AccountID = oldAccount
			live.AccountID = oldLive
			// Receipt survives preview expiry/purge; apply cannot revive the session.
			if _, err = db.Exec("UPDATE exams_imports_v1 SET expires_at=? WHERE id=?", time.Now().UTC().Add(-time.Minute), session); err != nil {
				t.Fatal(err)
			}
			assertStatus(request("apply", base+"/commit", body), 410)
			assertStatus(request("receipt", receiptURL, nil), 200)
			if _, err = db.Exec("DELETE FROM exams_imports_v1 WHERE id=?", session); err != nil {
				t.Fatal(err)
			}
			assertStatus(request("apply", base+"/commit", body), 404)
			assertStatus(request("receipt", receiptURL, nil), 200)
			if _, err = db.Exec("UPDATE exams_receipts_v1 SET created_at=? WHERE idempotency_key=?", time.Now().UTC().Add(-ReceiptRetention-time.Minute), key); err != nil {
				t.Fatal(err)
			}
			assertStatus(request("receipt", receiptURL, nil), 410)
			if _, err = db.Exec("UPDATE exams_receipts_v1 SET status_code=410,response=NULL WHERE idempotency_key=?", key); err != nil {
				t.Fatal(err)
			}
			assertStatus(request("receipt", receiptURL, nil), 410)
		})
	}
	guard()
	var events int
	if db.QueryRow("SELECT COUNT(*) FROM exams_outbox_v1").Scan(&events) != nil || events != 1 {
		t.Fatal("expected exactly one outside source event")
	}
	t.Logf("%d scoped HTTP checks; all three import kinds; durable replay and readonly recovery; one outside event; no shared HTTP or permanent DDL", checks)
}
