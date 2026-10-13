package homework

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type RatingSnapshotRequest struct {
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	DateFrom       string `json:"date_from"`
	DateTo         string `json:"date_to"`
	AsOf           string `json:"as_of"`
}

func (in RatingSnapshotRequest) validate() (time.Time, time.Time, time.Time, error) {
	if in.OrganizationID < 1 || in.DivisionID < 1 {
		return time.Time{}, time.Time{}, time.Time{}, bad("invalid_scope")
	}
	start, e := parseDay(in.DateFrom)
	if e != nil {
		return time.Time{}, time.Time{}, time.Time{}, bad("invalid_period")
	}
	end, e := parseDay(in.DateTo)
	if e != nil || end.Before(start) || end.Sub(start) > 365*24*time.Hour {
		return time.Time{}, time.Time{}, time.Time{}, bad("invalid_period")
	}
	at, e := time.Parse(time.RFC3339Nano, in.AsOf)
	if e != nil || at.Year() < 1000 {
		return time.Time{}, time.Time{}, time.Time{}, bad("invalid_as_of")
	}
	_, offset := at.Zone()
	if offset != 0 {
		return time.Time{}, time.Time{}, time.Time{}, bad("invalid_as_of")
	}
	return start, end, at.UTC(), nil
}

type RatingHomeworkResult struct {
	AccountID int64    `json:"account_id"`
	Status    int      `json:"status"`
	Result    *float64 `json:"result"`
	DatePass  *string  `json:"date_pass"`
}

type RatingHomeworkAssignment struct {
	AssignmentID int64                  `json:"assignment_id"`
	Name         string                 `json:"name"`
	TypeName     string                 `json:"type_name"`
	Deadline     string                 `json:"deadline"`
	Results      []RatingHomeworkResult `json:"results"`
}

type RatingHomeworkSnapshot struct {
	Source           string                     `json:"source"`
	OrganizationID   int64                      `json:"organization_id"`
	DivisionID       int64                      `json:"division_id"`
	Assignments      []RatingHomeworkAssignment `json:"assignments"`
	InputHash        string                     `json:"input_hash"`
	NextTransitionAt *string                    `json:"next_transition_at"`
}

func (out RatingHomeworkSnapshot) fingerprint() (string, error) {
	body, e := json.Marshal(struct {
		Source           string                     `json:"source"`
		OrganizationID   int64                      `json:"organization_id"`
		DivisionID       int64                      `json:"division_id"`
		Assignments      []RatingHomeworkAssignment `json:"assignments"`
		NextTransitionAt *string                    `json:"next_transition_at"`
	}{out.Source, out.OrganizationID, out.DivisionID, out.Assignments, out.NextTransitionAt})
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:]), nil
}

// Validate identities through their owner. Reading another service's database
// would couple this snapshot to the shared dev schema and bypass that boundary.
func (d *Dependencies) ratingScope(ctx context.Context, org, div int64) error {
	if d == nil || d.HTTP == nil {
		return unavailable
	}
	body, _ := json.Marshal(struct {
		OrganizationID int64   `json:"organization_id"`
		DivisionIDs    []int64 `json:"division_ids"`
	}{org, []int64{div}})
	req, e := http.NewRequestWithContext(ctx, "POST", d.CoreURL+"/internal/core/test-references", bytes.NewReader(body))
	if e != nil {
		return unavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", d.Key)
	res, e := d.HTTP.Do(req)
	if e != nil {
		return unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return missing
	}
	if res.StatusCode != http.StatusOK {
		return unavailable
	}
	var out struct {
		Status bool `json:"status"`
		Data   struct {
			Organization struct {
				ID int64 `json:"id"`
			} `json:"organization"`
			Divisions []struct {
				ID       int64 `json:"id"`
				IsActive bool  `json:"is_active"`
			} `json:"divisions"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || !out.Status || out.Data.Organization.ID != org || len(out.Data.Divisions) != 1 || out.Data.Divisions[0].ID != div {
		return unavailable
	}
	if !out.Data.Divisions[0].IsActive {
		return missing
	}
	return nil
}

func ratingHomeworkTransition(assignments []RatingHomeworkAssignment, at time.Time) *string {
	var next time.Time
	for _, a := range assignments {
		if a.TypeName != "ОВ" {
			continue
		}
		deadline, e := time.Parse("2006-01-02", a.Deadline)
		if e != nil {
			continue
		}
		// Legacy deadline < today starts counting at the following Moscow midnight.
		transition := time.Date(deadline.Year(), deadline.Month(), deadline.Day()+1, 0, 0, 0, 0, moscow).UTC()
		if transition.After(at) && (next.IsZero() || transition.Before(next)) {
			next = transition
		}
	}
	if next.IsZero() {
		return nil
	}
	v := next.Format(time.RFC3339)
	return &v
}

func (s *Server) ratingSnapshot(w http.ResponseWriter, r *http.Request) {
	// This read credential is shared with core. PDF workers use Config.Key and
	// must not gain access to grades just because they can claim processing jobs.
	if len(s.Config.CoreKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.Config.CoreKey)) != 1 {
		fail(w, &APIError{http.StatusUnauthorized, "invalid_service_key"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var in RatingSnapshotRequest
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	start, end, at, e := in.validate()
	if e != nil {
		fail(w, e)
		return
	}
	if e = s.Deps.ratingScope(ctx, in.OrganizationID, in.DivisionID); e != nil {
		fail(w, e)
		return
	}
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	out := RatingHomeworkSnapshot{Source: "homework", OrganizationID: in.OrganizationID, DivisionID: in.DivisionID,
		Assignments: []RatingHomeworkAssignment{}}
	rows, e := tx.QueryContext(ctx, `SELECT h.id,h.name,t.name,h.deadline FROM hw_assignments h
 JOIN hw_types t ON t.id=h.type_id AND t.organization_id=h.organization_id AND t.division_id=h.division_id
 WHERE h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL AND h.deadline BETWEEN ? AND ?
 ORDER BY h.id LIMIT 2001`, in.OrganizationID, in.DivisionID, start, end)
	if e != nil {
		fail(w, e)
		return
	}
	index := map[int64]int{}
	for rows.Next() {
		var a RatingHomeworkAssignment
		var deadline time.Time
		if e = rows.Scan(&a.AssignmentID, &a.Name, &a.TypeName, &deadline); e != nil {
			break
		}
		a.Deadline = deadline.Format("2006-01-02")
		a.Results = []RatingHomeworkResult{}
		index[a.AssignmentID] = len(out.Assignments)
		out.Assignments = append(out.Assignments, a)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		fail(w, e)
		return
	}
	if len(out.Assignments) > 2000 {
		fail(w, bad("rating_snapshot_too_large"))
		return
	}
	rows, e = tx.QueryContext(ctx, `SELECT r.assignment_id,r.account_id,r.status,r.result,r.date_pass FROM hw_results r
 JOIN hw_assignments h ON h.id=r.assignment_id
 WHERE h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL AND h.deadline BETWEEN ? AND ?
 ORDER BY r.assignment_id,r.account_id LIMIT 100001`, in.OrganizationID, in.DivisionID, start, end)
	if e != nil {
		fail(w, e)
		return
	}
	count := 0
	for rows.Next() {
		var assignmentID int64
		var result RatingHomeworkResult
		var date sql.NullTime
		if e = rows.Scan(&assignmentID, &result.AccountID, &result.Status, &result.Result, &date); e != nil {
			break
		}
		if date.Valid {
			v := date.Time.Format("2006-01-02")
			result.DatePass = &v
		}
		position, found := index[assignmentID]
		if !found {
			e = unavailable
			break
		}
		out.Assignments[position].Results = append(out.Assignments[position].Results, result)
		count++
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		fail(w, e)
		return
	}
	if count > 100000 {
		fail(w, bad("rating_snapshot_too_large"))
		return
	}
	out.NextTransitionAt = ratingHomeworkTransition(out.Assignments, at)
	if out.InputHash, e = out.fingerprint(); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, http.StatusOK, map[string]any{"status": true, "data": out})
}
