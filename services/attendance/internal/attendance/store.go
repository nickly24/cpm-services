package attendance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	mysql "github.com/go-sql-driver/mysql"
)

type Store struct{ DB *sql.DB }
type Result struct {
	Revision int64 `json:"revision"`
	Value    any   `json:"value"`
}

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s Store) Read(ctx context.Context, o int64) (*State, int64, error) {
	var b []byte
	var rev int64
	e := s.DB.QueryRowContext(ctx, "SELECT revision,document FROM attendance_state_v1 WHERE organization_id=?", o).Scan(&rev, &b)
	if errors.Is(e, sql.ErrNoRows) {
		return NewState(), 0, nil
	}
	if e != nil {
		return nil, 0, unavailable
	}
	var st State
	if json.Unmarshal(b, &st) != nil {
		return nil, 0, unavailable
	}
	return &st, rev, nil
}
func dbError(e error) error {
	var m *mysql.MySQLError
	if errors.As(e, &m) && (m.Number == 1213 || m.Number == 1205) {
		return conflict("concurrent_operation")
	}
	return unavailable
}
func (s Store) Mutate(ctx context.Context, a Identity, expected int64, key, path string, body []byte, authorizeReplay func(*State, Result) error, fn func(*State) (any, error), scopeBuilders ...func() (commandScope, error)) (Result, error) {
	o := a.User.OrganizationID
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return Result{}, unavailable
	}
	defer tx.Rollback()
	initial, _ := json.Marshal(NewState())
	if _, e = tx.ExecContext(ctx, "INSERT IGNORE INTO attendance_state_v1(organization_id,document) VALUES(?,?)", o, initial); e != nil {
		return Result{}, dbError(e)
	}
	var data []byte
	var rev int64
	if e = tx.QueryRowContext(ctx, "SELECT revision,document FROM attendance_state_v1 WHERE organization_id=? FOR UPDATE", o).Scan(&rev, &data); e != nil {
		return Result{}, dbError(e)
	}
	h := hash(append([]byte(path+"\n"), body...))
	var rh string
	var rb []byte
	e = tx.QueryRowContext(ctx, "SELECT request_hash,response FROM attendance_receipts_v1 WHERE organization_id=? AND actor_account_id=? AND request_key=?", o, a.User.AccountID, key).Scan(&rh, &rb)
	if e == nil {
		if rh != h {
			return Result{}, conflict("idempotency_conflict")
		}
		var result Result
		if json.Unmarshal(rb, &result) != nil {
			return result, unavailable
		}
		if authorizeReplay != nil {
			// Authorize against the immutable state of the original command:
			// later corrections must not change its historical group scope.
			var snapshot []byte
			if e = tx.QueryRowContext(ctx, "SELECT snapshot FROM attendance_audit_v1 WHERE organization_id=? AND revision=?", o, result.Revision).Scan(&snapshot); e != nil {
				return Result{}, dbError(e)
			}
			var original State
			if json.Unmarshal(snapshot, &original) != nil {
				return Result{}, unavailable
			}
			if e = authorizeReplay(&original, result); e != nil {
				return Result{}, e
			}
		}
		return result, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Result{}, dbError(e)
	}
	if rev != expected {
		return Result{}, conflict("revision_conflict")
	}
	var st State
	if json.Unmarshal(data, &st) != nil {
		return Result{}, unavailable
	}
	value, e := fn(&st)
	if e != nil {
		return Result{}, e
	}
	result := Result{rev + 1, value}
	var scope *commandScope
	if len(scopeBuilders) > 0 {
		meta, err := scopeBuilders[0]()
		if err != nil {
			return Result{}, err
		}
		scope = &meta
	}
	after, e := json.Marshal(st)
	if e != nil || len(after) > 32<<20 || len(st.Lessons) > 10000 || len(st.Entries) > 50000 || len(st.Days) > 5000 || len(st.Types) > 2000 || len(st.Zaps) > 5000 || len(st.Calendars) > 100 || len(st.Rooms) > 1000 {
		return Result{}, bad("dev_capacity_exceeded")
	}
	out, e := json.Marshal(storedReceipt{Result: result, ScopeMetadata: scope})
	if e != nil {
		return Result{}, unavailable
	}
	if _, e = tx.ExecContext(ctx, "UPDATE attendance_state_v1 SET revision=?,document=? WHERE organization_id=?", rev+1, after, o); e != nil {
		return Result{}, dbError(e)
	}
	// Immutable revision snapshot makes corrections, deletions and type versions auditable.
	if _, e = tx.ExecContext(ctx, "INSERT INTO attendance_audit_v1(organization_id,revision,actor_account_id,action,snapshot) VALUES(?,?,?,?,?)", o, rev+1, a.User.AccountID, path, after); e != nil {
		return Result{}, dbError(e)
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO attendance_receipts_v1 VALUES(?,?,?,?,?)", o, a.User.AccountID, key, h, out); e != nil {
		return Result{}, dbError(e)
	}
	if e = tx.Commit(); e != nil {
		return Result{}, dbError(e)
	}
	return result, nil
}
