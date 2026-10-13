package uploads

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"io"
	"net/http"
	"regexp"
)

type Server struct {
	DB     *sql.DB
	Config Config
	HTTP   *http.Client
	Broker *DeliveryBroker
}

func New(db *sql.DB, c Config) *Server {
	s := &Server{DB: db, Config: c, HTTP: newHTTP()}
	s.Broker = NewDeliveryBroker(db, c.RabbitURL, s.process)
	s.Broker.AuthorizationEnabled = c.OperationsEnabled
	return s
}
func uuid() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *Server) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func event(ctx context.Context, tx *sql.Tx, sc Scope, t Type, id, action string, rev int64) error {
	_, e := tx.ExecContext(ctx, "INSERT INTO uploads_events_v1(organization_id,division_id,actor_account_id,type,owner,resource_id,action,revision) VALUES(?,?,?,?,?,?,?,?)", sc.OrganizationID, sc.DivisionID, sc.ActorID, t.ID, t.Owner, id, action, rev)
	return e
}

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9:_-]{8,128}$`)

func command(r *http.Request) (string, error) {
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !keyPattern.MatchString(key) {
		return "", api(400, "idempotency_key_required")
	}
	return key, nil
}
func (s *Server) reserve(ctx context.Context, sc Scope, key, action, hash, id string) (string, bool, error) {
	_, e := s.DB.ExecContext(ctx, "INSERT INTO uploads_receipts_v1(organization_id,division_id,actor_account_id,command_id,action,request_hash,resource_id,state) VALUES(?,?,?,?,?,?,?,'reserved')", sc.OrganizationID, sc.DivisionID, sc.ActorID, key, action, hash, id)
	if e == nil {
		return id, true, nil
	}
	var my *mysql.MySQLError
	if !errors.As(e, &my) || my.Number != 1062 {
		return "", false, e
	}
	var a, h, stored, state string
	e = s.DB.QueryRowContext(ctx, "SELECT action,request_hash,resource_id,state FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=?", sc.OrganizationID, sc.DivisionID, sc.ActorID, key).Scan(&a, &h, &stored, &state)
	if e != nil {
		return "", false, e
	}
	if a != action || h != hash {
		return "", false, api(409, "command_payload_conflict")
	}
	return stored, false, nil
}
func receipt(ctx context.Context, tx *sql.Tx, sc Scope, key, state string) error {
	_, e := tx.ExecContext(ctx, "UPDATE uploads_receipts_v1 SET state=? WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=?", state, sc.OrganizationID, sc.DivisionID, sc.ActorID, key)
	return e
}

const sessionColumns = "id,organization_id,division_id,actor_account_id,type,owner,owner_session_id,target_id,revision,owner_revision,state,file_id,source_hash,expires_at,created_at"

type query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func loadSession(ctx context.Context, q query, sc Scope, id string, lock bool) (Session, error) {
	var ss Session
	stmt := "SELECT " + sessionColumns + " FROM uploads_sessions_v1 WHERE id=? AND organization_id=? AND division_id=? AND actor_account_id=?"
	if lock {
		stmt += " FOR UPDATE"
	}
	e := q.QueryRowContext(ctx, stmt, id, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(&ss.ID, &ss.OrganizationID, &ss.DivisionID, &ss.ActorID, &ss.Type, &ss.Owner, &ss.OwnerSessionID, &ss.TargetID, &ss.Revision, &ss.OwnerRevision, &ss.State, &ss.FileID, &ss.SourceHash, &ss.Expires, &ss.Created)
	return ss, e
}
func insertSession(ctx context.Context, tx *sql.Tx, ss Session) error {
	_, e := tx.ExecContext(ctx, "INSERT INTO uploads_sessions_v1(id,organization_id,division_id,actor_account_id,type,owner,owner_session_id,target_id,revision,owner_revision,state,file_id,source_hash,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", ss.ID, ss.OrganizationID, ss.DivisionID, ss.ActorID, ss.Type, ss.Owner, ss.OwnerSessionID, ss.TargetID, ss.Revision, ss.OwnerRevision, ss.State, ss.FileID, ss.SourceHash, ss.Expires)
	return e
}

const jobColumns = "id,organization_id,division_id,actor_account_id,session_id,type,owner,status,attempt,processed,total,error_code,report,created_at"

func loadJob(ctx context.Context, q query, sc Scope, id string) (Job, error) {
	var j Job
	var report []byte
	e := q.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM uploads_jobs_v1 WHERE id=? AND organization_id=? AND division_id=? AND actor_account_id=?", id, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(&j.ID, &j.OrganizationID, &j.DivisionID, &j.ActorID, &j.SessionID, &j.Type, &j.Owner, &j.Status, &j.Attempt, &j.Processed, &j.Total, &j.Error, &report, &j.Created)
	j.Report = report
	return j, e
}
func scrub(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, b := range x {
			if sensitive(k) {
				delete(x, k)
			} else {
				x[k] = scrub(b)
			}
		}
	case []any:
		for i, b := range x {
			x[i] = scrub(b)
		}
	}
	return v
}
func sensitive(k string) bool {
	return regexp.MustCompile(`(?i)(password|passwd|secret|token|credential|authorization|cookie|private_key)`).MatchString(k)
}
func masked(raw json.RawMessage) json.RawMessage {
	var v any
	if decodeJSONNumbers(raw, &v) != nil {
		return json.RawMessage(`null`)
	}
	b, _ := json.Marshal(scrub(v))
	return b
}

// Generic redaction/report transformations must preserve owner integer IDs and
// numeric lexemes; decoding through float64 changes values above 2^53.
func decodeJSONNumbers(raw json.RawMessage, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := d.Decode(value); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid JSON")
	}
	return nil
}
