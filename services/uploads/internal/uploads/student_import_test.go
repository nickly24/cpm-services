package uploads

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestStudentValidationDetailsAreAllowlisted(t *testing.T) {
	valid := []byte(`{"error":"student_import_validation","issues":[{"row":3,"field":"profile_id","reason":"profile_id_exists"}],"password":"NEVER"}`)
	out := studentValidation(valid)
	if out == nil || len(out.Issues) != 1 {
		t.Fatal("missing safe details")
	}
	if studentValidation([]byte(`{"error":"student_import_validation","issues":[{"row":3,"field":"password","reason":"NEVER"}]}`)) != nil {
		t.Fatal("unsafe detail accepted")
	}
}

func TestStudentBatchReservationCrashRecoverySQL(t *testing.T) {
	dsn := os.Getenv("CPM_STUDENT_IMPORT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated SQL fixture not enabled")
	}
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") || cfg.DBName != "cpm_release_student_test" {
		t.Fatal("dedicated loopback SQL fixture required")
	}
	cfg.DBName = ""
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	name := "cpm_release_student_" + uuid()
	if _, e = db.Exec("CREATE DATABASE " + name); e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DROP DATABASE " + name)
	cfg.DBName = name
	fixture, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	defer fixture.Close()
	ddl, e := os.ReadFile("../../migrations/001_uploads.sql")
	if e != nil {
		t.Fatal(e)
	}
	for _, stmt := range strings.Split(string(ddl), ";") {
		if strings.TrimSpace(stmt) != "" {
			if _, e := fixture.Exec(stmt); e != nil {
				t.Fatal(e)
			}
		}
	}
	s := &Server{DB: fixture}
	sc := Scope{OrganizationID: 9101, DivisionID: 9102, ActorID: 1}
	key := "crash-reservation-only"
	id, fresh, e := s.reserve(context.Background(), sc, key, "student.batch:synthetic:0", "hash", uuid())
	if e != nil || !fresh {
		t.Fatal("reservation failed", e)
	}
	// This durable receipt with no session is exactly the state left by a crash.
	var before int
	if fixture.QueryRow("SELECT COUNT(*) FROM uploads_sessions_v1").Scan(&before) != nil || before != 0 {
		t.Fatal("fault was not reproduced")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replayed, fresh, e := s.reserve(context.Background(), sc, key, "student.batch:synthetic:0", "hash", uuid())
			if e != nil || fresh || replayed != id {
				t.Error("reservation replay changed identity", e)
				return
			}
			proposed := Session{ID: id, Scope: sc, Type: "users.credentials", Owner: "core", Revision: 1, OwnerRevision: 1, State: "staging", FileID: uuid(), Expires: time.Now().UTC().Add(time.Hour)}
			actual, e := s.studentBatchSession(httptest.NewRequest("POST", "/", nil), sc, key, proposed)
			if e != nil || actual.ID != id || actual.FileID == "" {
				t.Error("crashed session did not recover", e)
			}
		}()
	}
	wg.Wait()
	for _, table := range []string{"uploads_receipts_v1", "uploads_sessions_v1", "uploads_files_v1"} {
		var count int
		if fixture.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count) != nil || count != 1 {
			t.Fatal("recovery duplicated durable identity", table, count)
		}
	}
	if _, _, e := s.reserve(context.Background(), sc, key, "student.batch:synthetic:0", "changed", uuid()); e == nil {
		t.Fatal("changed payload accepted on recovery")
	}
}
