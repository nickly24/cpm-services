package homework

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"time"
)

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func list(ctx context.Context, db DBTX, q string, args ...any) ([]map[string]any, error) {
	rows, e := db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, translate(e)
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for rows.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if e = rows.Scan(p...); e != nil {
			return nil, e
		}
		r := map[string]any{}
		for i, k := range cols {
			if b, ok := v[i].([]byte); ok {
				r[k] = string(b)
			} else {
				r[k] = v[i]
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func one(ctx context.Context, db DBTX, q string, args ...any) (map[string]any, error) {
	rows, e := list(ctx, db, q, args...)
	if e != nil {
		return nil, e
	}
	if len(rows) == 0 {
		return nil, missing
	}
	return rows[0], nil
}
func translate(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return missing
	}
	var m *mysql.MySQLError
	if errors.As(e, &m) {
		if m.Number == 1062 {
			return conflict("duplicate")
		}
		if m.Number == 1451 || m.Number == 1452 {
			return conflict("reference_conflict")
		}
		if m.Number == 1213 || m.Number == 1205 {
			return conflict("concurrent_change")
		}
	}
	return e
}
func num(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case uint64:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func stamp(v any) time.Time { t, _ := v.(time.Time); return t }
func (s *Server) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return translate(e)
	}
	return translate(tx.Commit())
}
func (s *Server) settings(ctx context.Context, db DBTX, div int64) (Settings, error) {
	var raw string
	e := db.QueryRowContext(ctx, "SELECT setting_value FROM application_settings WHERE setting_key=?", fmt.Sprintf("homework.%d.limits", div)).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if e != nil {
		return Settings{}, e
	}
	var out Settings
	if json.Unmarshal([]byte(raw), &out) != nil || out.Validate() != nil {
		return out, &APIError{503, "invalid_persisted_settings"}
	}
	return out, nil
}
func audit(ctx context.Context, tx *sql.Tx, a Actor, div int64, action string, id any) error {
	var actor any
	if a.AccountID > 0 {
		actor = a.AccountID
	}
	_, e := tx.ExecContext(ctx, "INSERT INTO hw_audit(organization_id,division_id,actor_id,action,entity_id) VALUES(?,?,?,?,?)", a.OrganizationID, div, actor, action, fmt.Sprint(id))
	return e
}
func enqueue(ctx context.Context, tx *sql.Tx, kind string, payload any) error {
	id := uuid()
	ev := Event{id, kind, 1, time.Now().UTC(), "homework", id, payload}
	b, e := json.Marshal(ev)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO hw_outbox(id,event_type,body_json) VALUES(?,?,?)", id, kind, b)
	return e
}
func queueDelete(ctx context.Context, tx *sql.Tx, key string) error {
	if key == "" {
		return nil
	}
	// Upload credentials may still be usable after cancellation/completion. Keep
	// their tombstone until they expire; an object uploaded late is then removed.
	available := time.Now().UTC()
	jobs, e := list(ctx, tx, "SELECT status,source_key,settings_json,updated_at FROM hw_jobs WHERE source_key=? OR output_key=?", key, key)
	if e != nil {
		return e
	}
	for _, j := range jobs {
		var limits Settings
		if json.Unmarshal([]byte(str(j["settings_json"])), &limits) != nil {
			return unavailable
		}
		delay := time.Duration(0)
		if str(j["source_key"]) == key {
			delay = time.Duration(limits.PresignTTLSeconds+10) * time.Second
		} else if state := str(j["status"]); state == "running" || state == "queued" || state == "cancelled" || state == "failed" {
			delay = 310 * time.Second
		}
		until := stamp(j["updated_at"]).Add(delay)
		if until.After(available) {
			available = until
		}
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO hw_delete_jobs(object_key,status,available_at) VALUES(?,'queued',?) ON DUPLICATE KEY UPDATE attempts=IF(status='done',0,attempts),available_at=GREATEST(VALUES(available_at),IF(status='done',VALUES(available_at),available_at)),status=IF(status='done','queued',status)", key, available)
	return e
}
