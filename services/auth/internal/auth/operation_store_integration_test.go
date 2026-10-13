package auth

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Explicit opt-in integration against a connection-owned temporary table.
// No shared Auth table or account/permission row is read or modified.
func TestOperationSQLTemporaryStore(t *testing.T) {
	dsn := os.Getenv("CPM_AUTH_OPERATION_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated local SQL test not enabled")
	}
	c, err := mysql.ParseDSN(dsn)
	if err != nil || c.Net != "tcp" || c.Addr != "127.0.0.1:13306" || c.DBName != "cpm_dev" || !c.ParseTime {
		t.Fatal("only explicit local Dev test DSN is accepted")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("isolated SQL connection unavailable")
	}
	defer db.Close()
	// A TEMPORARY TABLE lives on one connection. All calls share exactly that
	// isolated connection; concurrent issuance tests the store transaction path
	// and immutable replay, not cross-connection InnoDB lock contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ddl, err := os.ReadFile("../../migrations/003_import_operation_grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	statement := strings.Replace(string(ddl), "CREATE TABLE IF NOT EXISTS", "CREATE TEMPORARY TABLE", 1)
	if _, err = db.ExecContext(ctx, statement); err != nil {
		t.Fatal("temporary grant schema could not be created")
	}
	defer db.ExecContext(context.Background(), "DROP TEMPORARY TABLE IF EXISTS auth_operation_grants")
	svc, accounts, actor, binding := operationFixture(t)
	sourceDeadline := svc.now().Add(time.Minute).Add(123456789 * time.Nanosecond)
	binding.UploadExpiresAt = sourceDeadline.Format(time.RFC3339Nano)
	svc.Store = SQLOperationStore{DB: db}
	var wg sync.WaitGroup
	results := make(chan OperationResponse, 12)
	errors := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			r, e := svc.Issue(ctx, accounts.account, actor, "22222222-2222-4222-8222-222222222222", binding)
			results <- r
			errors <- e
		})
	}
	wg.Wait()
	close(results)
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	var original OperationResponse
	for r := range results {
		if original.GrantID == "" {
			original = r
		}
		if r.GrantID != original.GrantID || r.ExpiresAt != original.ExpiresAt {
			t.Fatal("SQL issuance changed grant or expiry")
		}
		expiry, e := time.Parse(time.RFC3339Nano, r.ExpiresAt)
		if e != nil || !expiry.Equal(sourceDeadline.Truncate(time.Microsecond)) || expiry.After(sourceDeadline) {
			t.Fatal("SQL expiry rounded beyond immutable source deadline")
		}
	}
	var count int
	if e := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_operation_grants").Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate temporary grant records")
	}
	changed := binding
	changed.OwnerRequestSHA256 = strings.Repeat("b", 64)
	_, err = svc.Issue(ctx, accounts.account, actor, "22222222-2222-4222-8222-222222222222", changed)
	expectOperationError(t, err, 409)
	// Re-open the service against the same SQL store to verify persistence and
	// JSON database normalization preserves exact native hash and scope strings.
	svc = &OperationService{Store: SQLOperationStore{DB: db}, Accounts: accounts, Now: svc.Now}
	if _, err = svc.Authorize(ctx, original.GrantID, "core", operationRequest(binding, "apply")); err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC) }
	replay, err := svc.Issue(ctx, accounts.account, actor, "22222222-2222-4222-8222-222222222222", binding)
	if err != nil || replay.GrantID != original.GrantID || replay.ExpiresAt != original.ExpiresAt {
		t.Fatal("historical expired issuance identity lost")
	}
	_, err = svc.Issue(ctx, accounts.account, actor, "33333333-3333-4333-8333-333333333333", binding)
	expectOperationError(t, err, 410)
	recovered, err := svc.Recover(ctx, original.GrantID, "44444444-4444-4444-8444-444444444444", accounts.account, actor)
	if err != nil || recovered.Mode != "receipt" || recovered.Binding.NativeCommandKey != binding.NativeCommandKey {
		t.Fatal("SQL receipt recovery failed")
	}
	_, err = svc.Authorize(ctx, recovered.GrantID, "core", operationRequest(binding, "apply"))
	expectOperationError(t, err, 403)
	if _, err = svc.Authorize(ctx, recovered.GrantID, "core", operationRequest(binding, "receipt")); err != nil {
		t.Fatal(err)
	}
	if err = svc.Revoke(ctx, recovered.GrantID, accounts.account, actor); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Authorize(ctx, recovered.GrantID, "core", operationRequest(binding, "receipt"))
	expectOperationError(t, err, 410)
}
