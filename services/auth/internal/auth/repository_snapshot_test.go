package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
)

// Explicit opt-in against a dedicated local test instance only. Each run owns
// a fresh disposable schema; application tables and rows are never touched.
func TestAccountSnapshotSQL(t *testing.T) {
	dsn := os.Getenv("CPM_AUTH_SNAPSHOT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated-schema SQL test not enabled")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !cfg.ParseTime || cfg.Addr != "127.0.0.1:13316" || cfg.DBName != "auth_snapshot" {
		t.Fatal("parseTime connection required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.ExecContext(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	schema := fmt.Sprintf("auth_snapshot_%d", time.Now().UnixNano())
	exec("CREATE DATABASE " + schema)
	defer db.ExecContext(context.Background(), "DROP DATABASE "+schema)
	exec("USE " + schema)
	ddl := []string{
		"auth_users (id BIGINT PRIMARY KEY,ref_id BIGINT,username VARCHAR(50),password VARCHAR(200),role VARCHAR(30))",
		"auth_account_state (user_id BIGINT PRIMARY KEY,is_active BOOL,auth_version BIGINT)",
		"organizations (id BIGINT PRIMARY KEY,name VARCHAR(100),is_active BOOL)",
		"divisions (id BIGINT PRIMARY KEY,organization_id BIGINT,name VARCHAR(100),is_active BOOL,KEY(organization_id))",
		"auth_organization_access (user_id BIGINT,organization_id BIGINT,PRIMARY KEY(user_id,organization_id))",
		"auth_division_access (user_id BIGINT,organization_id BIGINT,division_id BIGINT,group_id BIGINT,role_id BIGINT,PRIMARY KEY(user_id,division_id))",
		"auth_scoped_roles (id BIGINT PRIMARY KEY,organization_id BIGINT,name VARCHAR(100))",
		"auth_scoped_role_permissions (role_id BIGINT,section VARCHAR(100),can_view BOOL,can_edit BOOL,PRIMARY KEY(role_id,section))",
		"auth_sessions (id VARCHAR(32) PRIMARY KEY,user_id BIGINT,organization_id BIGINT,division_id BIGINT NULL,expires_at DATETIME(6),revoked_at DATETIME(6) NULL,auth_version BIGINT,session_version BIGINT,context_version BIGINT)",
	}
	for _, def := range ddl {
		exec("CREATE TABLE " + def)
	}
	for _, table := range profileTables {
		columns := " (id BIGINT PRIMARY KEY,full_name VARCHAR(100))"
		if table == "admin_role_users" {
			columns = " (id BIGINT PRIMARY KEY,full_name VARCHAR(100),is_active BOOL,session_version BIGINT)"
		}
		exec("CREATE TABLE " + table + columns)
		if table == "admin_role_users" {
			exec("INSERT INTO "+table+" VALUES (1,?,1,3)", table)
		} else {
			exec("INSERT INTO "+table+" VALUES (1,?)", table)
		}
	}
	exec("INSERT INTO organizations VALUES (1,'first',1),(2,'second',1),(3,'inactive',0),(4,'empty',1)")
	exec("INSERT INTO divisions VALUES (11,1,'first-a',1),(12,1,'first-b',1),(13,1,'inactive',0),(21,2,'second-a',1),(31,3,'hidden',1)")
	exec("INSERT INTO auth_scoped_roles VALUES (101,1,'viewer'),(201,2,'editor')")
	exec("INSERT INTO auth_scoped_role_permissions VALUES (101,'tests',1,0),(101,'users',0,0),(101,'unsupported-section',1,1),(201,'tests',1,1)")
	roles := []string{"student", "proctor", "examinator", "admin", "supervisor", "staff_admin"}
	repo := &Repository{DB: db}
	for i, role := range roles {
		id := int64(i + 10)
		exec("INSERT INTO auth_users VALUES (?,1,?,'hash',?)", id, role, role)
		exec("INSERT INTO auth_account_state VALUES (?,1,7)", id)
		exec("INSERT INTO auth_organization_access VALUES (?,1),(?,3),(?,4)", id, id, id)
		if role != "admin" {
			exec("INSERT INTO auth_organization_access VALUES (?,2)", id)
		}
		exec("INSERT INTO auth_division_access VALUES (?,1,11,91,101),(?,1,12,NULL,NULL),(?,1,13,91,101),(?,2,21,92,201),(?,3,31,93,101)", id, id, id, id, id)
		old, e1 := repo.legacySnapshotAccount(ctx, id)
		got, e2 := repo.LoadAccount(ctx, id)
		if role == "admin" {
			if !errors.Is(e1, ErrForbidden) || !errors.Is(e2, ErrForbidden) {
				t.Fatal("multi-org admin accepted")
			}
			exec("DELETE FROM auth_organization_access WHERE user_id=13 AND organization_id=4")
			old, e1 = repo.legacySnapshotAccount(ctx, id)
			got, e2 = repo.LoadAccount(ctx, id)
		}
		if e1 != nil || e2 != nil || !reflect.DeepEqual(old, got) {
			t.Fatalf("%s access mismatch: errors=%v/%v old=%+v new=%+v", role, e1, e2, old, got)
		}
		login, e := repo.FindAccount(ctx, role)
		if e != nil || login.Password != "hash" {
			t.Fatalf("%s login failed: %v", role, e)
		}
		if got.Password != "" {
			t.Fatal("authorization read password")
		}
	}
	if _, e := repo.LoadAccount(ctx, 999); !errors.Is(e, ErrCredentials) {
		t.Fatal("unknown account accepted")
	}
	for _, change := range []struct {
		sql, undo string
		id        int64
	}{
		{"UPDATE auth_account_state SET is_active=0 WHERE user_id=10", "UPDATE auth_account_state SET is_active=1 WHERE user_id=10", 10},
		{"DELETE FROM students WHERE id=1", "INSERT INTO students VALUES (1,'students')", 10},
		{"UPDATE admin_role_users SET is_active=0 WHERE id=1", "UPDATE admin_role_users SET is_active=1 WHERE id=1", 15},
	} {
		exec(change.sql)
		if _, e := repo.LoadAccount(ctx, change.id); !errors.Is(e, ErrCredentials) {
			t.Fatal("inactive/missing profile accepted", e)
		}
		exec(change.undo)
	}
	a, e := repo.LoadAccount(ctx, 15)
	if e != nil {
		t.Fatal(e)
	}
	division := int64(11)
	sess := Session{ID: "01234567890123456789012345678901", UserID: 15, Context: ContextDTO{1, &division}, AuthVersion: 7, LegacySessionVersion: 3, ContextVersion: 2, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	exec("INSERT INTO auth_sessions VALUES (?,15,1,11,?,NULL,7,3,2)", sess.ID, sess.ExpiresAt)
	var before, after int
	var label string
	if e = db.QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Com_select'").Scan(&label, &before); e != nil {
		t.Fatal(e)
	}
	actual, loaded, e := repo.SessionAccount(ctx, sess.ID)
	if e != nil || !reflect.DeepEqual(a, actual) || loaded.ID != sess.ID {
		t.Fatal("session snapshot failed", e)
	}
	if e = db.QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Com_select'").Scan(&label, &after); e != nil {
		t.Fatal(e)
	}
	if after-before != 1 {
		t.Fatalf("authorization used %d SELECTs; want 1", after-before)
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	tokens := &Tokens{Key: key, KeyID: "snapshot-test", Issuer: "snapshot-test", Audience: "snapshot-test", TTL: time.Minute}
	user, e := a.User(sess.Context)
	if e != nil {
		t.Fatal(e)
	}
	token, e := tokens.Issue(*a, user, sess)
	if e != nil {
		t.Fatal(e)
	}
	server := &Server{Repo: repo, Tokens: tokens}
	request := func(want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/auth/me", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.me(w, r)
		if w.Code != want {
			t.Fatalf("me status %d want %d", w.Code, want)
		}
	}
	request(200)
	for _, change := range []struct{ sql, undo string }{
		{"UPDATE auth_sessions SET revoked_at=UTC_TIMESTAMP()", "UPDATE auth_sessions SET revoked_at=NULL"},
		{"UPDATE auth_sessions SET expires_at='2000-01-01'", "UPDATE auth_sessions SET expires_at='2099-01-01'"},
		{"UPDATE auth_sessions SET context_version=3", "UPDATE auth_sessions SET context_version=2"},
		{"UPDATE auth_sessions SET division_id=12", "UPDATE auth_sessions SET division_id=11"},
		{"UPDATE auth_sessions SET organization_id=2", "UPDATE auth_sessions SET organization_id=1"},
		{"UPDATE auth_sessions SET auth_version=8", "UPDATE auth_sessions SET auth_version=7"},
		{"UPDATE auth_sessions SET session_version=4", "UPDATE auth_sessions SET session_version=3"},
		{"UPDATE auth_account_state SET auth_version=8 WHERE user_id=15", "UPDATE auth_account_state SET auth_version=7 WHERE user_id=15"},
		{"UPDATE auth_account_state SET is_active=0 WHERE user_id=15", "UPDATE auth_account_state SET is_active=1 WHERE user_id=15"},
		{"UPDATE admin_role_users SET session_version=4", "UPDATE admin_role_users SET session_version=3"},
		{"UPDATE admin_role_users SET is_active=0", "UPDATE admin_role_users SET is_active=1"},
		{"UPDATE divisions SET is_active=0 WHERE id=11", "UPDATE divisions SET is_active=1 WHERE id=11"},
		{"UPDATE organizations SET is_active=0 WHERE id=1", "UPDATE organizations SET is_active=1 WHERE id=1"},
		{"UPDATE auth_division_access SET role_id=NULL WHERE user_id=15 AND division_id=11", "UPDATE auth_division_access SET role_id=101 WHERE user_id=15 AND division_id=11"},
		{"DELETE FROM auth_division_access WHERE user_id=15 AND division_id=11", "INSERT INTO auth_division_access VALUES (15,1,11,91,101)"},
		{"DELETE FROM auth_organization_access WHERE user_id=15 AND organization_id=1", "INSERT INTO auth_organization_access VALUES (15,1)"},
	} {
		exec(change.sql)
		request(401)
		exec(change.undo)
		request(200)
	}
	exec("UPDATE auth_scoped_role_permissions SET can_edit=1 WHERE role_id=101 AND section='tests'")
	fresh, _, e := repo.SessionAccount(ctx, sess.ID)
	if e != nil {
		t.Fatal(e)
	}
	u, e := fresh.User(sess.Context)
	if e != nil || !u.Permissions["tests"].Edit {
		t.Fatal("live role change hidden")
	}
	t.Log("all six roles equivalent; one SELECT; revoked/expired/inactive/version/tenant/membership and live permissions verified")
}
