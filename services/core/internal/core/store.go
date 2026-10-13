package core

import (
	"context"
	"database/sql"
	"errors"
	"github.com/go-sql-driver/mysql"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func list(ctx context.Context, db DBTX, q string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, k := range cols {
			v := values[i]
			if strings.HasPrefix(k, "is_") || k == "can_view" || k == "can_edit" {
				if n, ok := v.(int64); ok {
					v = n != 0
				}
			}
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if t, ok := v.(time.Time); ok {
				if strings.Contains(k, "date") && !strings.Contains(k, "created") {
					v = t.Format("2006-01-02")
				} else {
					v = t.UTC().Format(time.RFC3339)
				}
			}
			row[k] = v
		}
		out = append(out, row)
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
func num(v any) int64 {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case int64:
		return x
	case uint64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}
func pointer(v any) *int64 {
	if v == nil {
		return nil
	}
	n := num(v)
	return &n
}
func validText(s string, max int, required bool) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && (!required || strings.TrimSpace(s) != "") && !strings.ContainsRune(s, 0)
}
func positive(p *int64) bool { return p == nil || *p > 0 }
func validDate(s string) bool {
	t, e := time.Parse("2006-01-02", s)
	return e == nil && t.Format("2006-01-02") == s
}
func period(from, to string) error {
	if !validDate(from) || !validDate(to) || from > to {
		return bad("invalid_date_range")
	}
	a, _ := time.Parse("2006-01-02", from)
	b, _ := time.Parse("2006-01-02", to)
	if b.Sub(a) > 365*24*time.Hour {
		return bad("date_range_too_large")
	}
	return nil
}
func translate(err error) error {
	if err == nil {
		return nil
	}
	var api *APIError
	if errors.As(err, &api) {
		return api
	}
	if errors.Is(err, sql.ErrNoRows) {
		return missing
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		if me.Number == 1062 || me.Number == 1451 {
			return conflict
		}
		if me.Number == 1452 {
			return bad("invalid_reference")
		}
	}
	return err
}
func (s *Server) transact(rctx context.Context, fn func(*sql.Tx) error) error {
	tx, e := s.DB.BeginTx(rctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func audit(ctx context.Context, db DBTX, a Actor, d *int64, action string, id int64) error {
	_, e := db.ExecContext(ctx, "INSERT INTO core_audit(actor_id,organization_id,division_id,action,entity_id) VALUES(?,?,?,?,?)", a.AccountID, a.OrganizationID, d, action, id)
	return e
}
func lockDivision(ctx context.Context, db DBTX, a Actor, d int64) error {
	var id int64
	return db.QueryRowContext(ctx, "SELECT id FROM divisions WHERE id=? AND organization_id=? AND is_active=1 FOR UPDATE", d, a.OrganizationID).Scan(&id)
}
