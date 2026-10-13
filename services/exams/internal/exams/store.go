package exams

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func UUID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic("random source unavailable")
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func load(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, org, id int64, locked bool) (*Exam, error) {
	stmt := "SELECT document,next_id FROM exams_catalog_v1 WHERE organization_id=? AND id=?"
	if locked {
		stmt += " FOR UPDATE"
	}
	var raw []byte
	var next int64
	if e := q.QueryRowContext(ctx, stmt, org, id).Scan(&raw, &next); e != nil {
		if e == sql.ErrNoRows {
			return nil, Missing
		}
		return nil, e
	}
	return decodeExam(raw, next)
}
func save(ctx context.Context, tx *sql.Tx, e *Exam) error {
	raw, err := encodeExam(e)
	if err != nil {
		return err
	}
	if len(raw) > 40<<20 {
		return &APIError{422, "exam_capacity_exceeded"}
	}
	_, err = tx.ExecContext(ctx, "UPDATE exams_catalog_v1 SET document=?,next_id=?,revision=? WHERE organization_id=? AND id=?", raw, e.NextID, e.Revision, e.OrganizationID, e.ID)
	return err
}
func create(ctx context.Context, tx *sql.Tx, e *Exam) error {
	result, err := tx.ExecContext(ctx, "INSERT INTO exams_catalog_v1(organization_id,owner_division_id,revision,next_id,document) VALUES(?,?,?,?,JSON_OBJECT())", e.OrganizationID, e.OwnerDivisionID, e.Revision, e.NextID)
	if err != nil {
		return err
	}
	e.ID, err = result.LastInsertId()
	if err != nil {
		return err
	}
	return save(ctx, tx, e)
}
func sourceChange(ctx context.Context, tx *sql.Tx, e *Exam, divs []int64, reason string) error {
	sort.Slice(divs, func(i, j int) bool { return divs[i] < divs[j] })
	seen := map[int64]bool{}
	for _, division := range divs {
		if seen[division] {
			continue
		}
		seen[division] = true
		_, err := tx.ExecContext(ctx, "INSERT INTO exams_source_v1(organization_id,division_id,revision) VALUES(?,?,1) ON DUPLICATE KEY UPDATE revision=revision+1", e.OrganizationID, division)
		if err != nil {
			return err
		}
		var rev int64
		if err = tx.QueryRowContext(ctx, "SELECT revision FROM exams_source_v1 WHERE organization_id=? AND division_id=?", e.OrganizationID, division).Scan(&rev); err != nil {
			return err
		}
		id := UUID()
		envelope := map[string]any{"message_id": id, "message_type": "exams.source.changed", "schema_version": 1, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "producer": "exams-service", "correlation_id": id, "payload": map[string]any{"organization_id": e.OrganizationID, "division_id": division, "exam_id": e.ID, "exam_revision": e.Revision, "source_revision": rev, "reason": reason}}
		raw, _ := json.Marshal(envelope)
		if _, err = tx.ExecContext(ctx, "INSERT INTO exams_outbox_v1(id,organization_id,division_id,source_revision,envelope) VALUES(?,?,?,?,?)", id, e.OrganizationID, division, rev, raw); err != nil {
			return err
		}
	}
	return nil
}
func allDivisions(e *Exam) []int64 {
	ids := make([]int64, 0, len(e.Divisions))
	for id := range e.Divisions {
		ids = append(ids, id)
	}
	return ids
}
func listExams(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, org int64) ([]*Exam, error) {
	rows, err := q.QueryContext(ctx, "SELECT document,next_id FROM exams_catalog_v1 WHERE organization_id=? ORDER BY id", org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Exam{}
	for rows.Next() {
		var raw []byte
		var n int64
		if err = rows.Scan(&raw, &n); err != nil {
			return nil, err
		}
		e, decodeErr := decodeExam(raw, n)
		if decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func validateKey(s string) bool {
	if len(s) != 36 {
		return false
	}
	_, err := hex.DecodeString(s[:8] + s[9:13] + s[14:18] + s[19:23] + s[24:])
	return err == nil && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
}
func ptr(n int64) *int64 { return &n }
func contains(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func memberIDs(e *Exam, division int64) []int64 {
	seen := map[int64]bool{}
	if d := e.Divisions[division]; d != nil {
		for _, c := range d.Commissions {
			for _, m := range c.Members {
				seen[m.AccountID] = true
			}
		}
		for _, a := range d.Assignments {
			seen[a.Student.AccountID] = true
			for _, m := range a.Members {
				seen[m.AccountID] = true
			}
		}
	}
	ids := []int64{}
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
func requestMemberIDs(raw json.RawMessage) []int64 {
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	ids := []int64{}
	for _, key := range []string{"student_account_id", "account_id"} {
		var id int64
		if json.Unmarshal(body[key], &id) == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	var members []int64
	if json.Unmarshal(body["examinator_ids"], &members) == nil {
		ids = append(ids, members...)
	}
	return ids
}
func uniqueIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
func scopeMembers(members map[int64]Member, division int64) map[int64]Member {
	out := map[int64]Member{}
	for id, m := range members {
		if contains(m.DivisionIDs, division) {
			out[id] = m
		}
	}
	return out
}
func sqlError(err error) error {
	_ = fmt.Sprintf("%T", err)
	return &APIError{503, "storage_unavailable"}
}
