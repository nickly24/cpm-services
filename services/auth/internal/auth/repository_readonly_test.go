package auth

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in read-only comparison of explicitly supplied synthetic accounts. Never
// creates tables, writes sessions, changes permissions, or logs identity data.
func TestAccountSnapshotReadOnly(t *testing.T) {
	dsn, ids := os.Getenv("CPM_AUTH_SNAPSHOT_READONLY_DSN"), os.Getenv("CPM_AUTH_SNAPSHOT_READONLY_IDS")
	if dsn == "" || ids == "" {
		t.Skip("read-only synthetic account comparison not enabled")
	}
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := &Repository{DB: db}
	for _, raw := range strings.Split(ids, ",") {
		id, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || id < 1 {
			t.Fatal("invalid explicit account ID")
		}
		old, e1 := repo.legacySnapshotAccount(ctx, id)
		current, e2 := repo.LoadAccount(ctx, id)
		if e1 != nil || e2 != nil || !reflect.DeepEqual(old, current) {
			t.Fatal("read-only account access mismatch", e1, e2)
		}
		var sid string
		e = db.QueryRowContext(ctx, "SELECT id FROM auth_sessions WHERE user_id=? AND revoked_at IS NULL AND expires_at>UTC_TIMESTAMP() ORDER BY expires_at DESC LIMIT 1", id).Scan(&sid)
		if e != nil {
			t.Fatal("synthetic session unavailable", e)
		}
		a, s, e := repo.SessionAccount(ctx, sid)
		if e != nil || !reflect.DeepEqual(a, current) || s.UserID != id {
			t.Fatal("read-only session snapshot mismatch", e)
		}
	}
	t.Log("explicit synthetic account and session access matches previous SQL loader")
}
