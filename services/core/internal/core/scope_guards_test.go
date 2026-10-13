package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Executes handlers against an ordered transaction trace. Each expectation
// verifies SQL boundaries and bound arguments; no database is changed.
type scopeGuardStep struct {
	exec       bool
	contains   []string
	notContain []string
	args       []any
	columns    []string
	rows       [][]driver.Value
	id         int64
}
type scopeGuardConnector struct {
	steps     []scopeGuardStep
	committed bool
}
type scopeGuardConn struct{ c *scopeGuardConnector }
type scopeGuardTx struct{ c *scopeGuardConnector }

func (c *scopeGuardConnector) Connect(context.Context) (driver.Conn, error) {
	return &scopeGuardConn{c}, nil
}
func (*scopeGuardConnector) Driver() driver.Driver          { return ratingSnapshotDriver{} }
func (*scopeGuardConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*scopeGuardConn) Close() error                        { return nil }
func (c *scopeGuardConn) Begin() (driver.Tx, error)         { return &scopeGuardTx{c.c}, nil }
func (c *scopeGuardConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &scopeGuardTx{c.c}, nil
}
func (c *scopeGuardConn) next(q string, args []driver.NamedValue, exec bool) (scopeGuardStep, error) {
	if len(c.c.steps) == 0 {
		return scopeGuardStep{}, fmt.Errorf("unexpected database operation")
	}
	w := c.c.steps[0]
	c.c.steps = c.c.steps[1:]
	if w.exec != exec {
		return w, fmt.Errorf("incorrect operation kind")
	}
	for _, part := range w.contains {
		if !strings.Contains(q, part) {
			return w, fmt.Errorf("missing scope: %s", part)
		}
	}
	for _, part := range w.notContain {
		if strings.Contains(q, part) {
			return w, fmt.Errorf("forbidden projection: %s", part)
		}
	}
	got := make([]any, len(args))
	for i, arg := range args {
		got[i] = arg.Value
	}
	if !reflect.DeepEqual(got, w.args) {
		return w, fmt.Errorf("incorrect scope arguments: %v", got)
	}
	return w, nil
}
func (c *scopeGuardConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	w, e := c.next(q, args, false)
	if e != nil {
		return nil, e
	}
	return &ratingSnapshotRows{cols: w.columns, rows: w.rows}, nil
}
func (c *scopeGuardConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	w, e := c.next(q, args, true)
	if e != nil {
		return nil, e
	}
	return scopeGuardResult{w.id}, nil
}

type scopeGuardResult struct{ id int64 }

func (r scopeGuardResult) LastInsertId() (int64, error) { return r.id, nil }
func (scopeGuardResult) RowsAffected() (int64, error)   { return 1, nil }
func (tx *scopeGuardTx) Commit() error                  { tx.c.committed = true; return nil }
func (*scopeGuardTx) Rollback() error                   { return nil }
func scopeGuardDB(t *testing.T, steps []scopeGuardStep) (*sql.DB, *scopeGuardConnector) {
	t.Helper()
	c := &scopeGuardConnector{steps: steps}
	db := sql.OpenDB(c)
	t.Cleanup(func() {
		db.Close()
		if len(c.steps) != 0 {
			t.Errorf("%d expected database operations were omitted", len(c.steps))
		}
	})
	return db, c
}
