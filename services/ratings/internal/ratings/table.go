package ratings

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
)

// The table reads only persisted summary numbers. Source validation is a
// separate authenticated read; report/details keep the complete old contract.
type tableComponent struct {
	Rating *float64 `json:"rating"`
}
type tableResult struct {
	JobID          string         `json:"job_id"`
	Revision       int64          `json:"revision"`
	OrganizationID int64          `json:"organization_id"`
	DivisionID     int64          `json:"division_id"`
	AccountID      int64          `json:"account_id"`
	Algorithm      string         `json:"algorithm_version"`
	Status         string         `json:"status"`
	InputHash      string         `json:"input_hash"`
	Homework       tableComponent `json:"homework"`
	Exams          tableComponent `json:"exams"`
	Tests          tableComponent `json:"tests"`
	Final          *float64       `json:"final_rating"`
}
type tableRow struct {
	Account Account     `json:"profile"`
	Result  tableResult `json:"rating"`
	Place   int         `json:"place"`
}

func (s *Server) publication(r *http.Request, tx *sql.Tx, a Actor, d int64, snapshot bool) (Job, error) {
	version := r.URL.Query().Get("version")
	if version != "" && !validUUID(version) {
		return Job{}, bad("invalid_version")
	}
	var id *string
	if version != "" {
		id = &version
	} else {
		e := tx.QueryRowContext(r.Context(), "SELECT published_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=?", a.OrganizationID, d).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			return Job{}, nil
		}
		if e != nil {
			return Job{}, e
		}
		if id == nil {
			return Job{}, nil
		}
	}
	columns := jobColumns
	if !snapshot {
		columns = strings.Replace(columns, ",snapshot,", ",NULL AS snapshot,", 1)
	}
	return scanJob(tx.QueryRowContext(r.Context(), "SELECT "+columns+" FROM ratings_jobs_v1 WHERE id=? AND organization_id=? AND division_id=? AND status='completed'", id, a.OrganizationID, d))
}

const tableSearch = `(?='' OR LOCATE(?,LOWER(CONCAT_WS('\n',
 JSON_UNQUOTE(JSON_EXTRACT(profile,'$.full_name')),
 NULLIF(JSON_UNQUOTE(JSON_EXTRACT(profile,'$.group_name')),'null'),
 NULLIF(JSON_UNQUOTE(JSON_EXTRACT(profile,'$.class')),'null'),CAST(account_id AS CHAR))))>0)`

const tableProjection = `JSON_OBJECT(
 'job_id',JSON_EXTRACT(output,'$.job_id'),'revision',JSON_EXTRACT(output,'$.revision'),
 'organization_id',JSON_EXTRACT(output,'$.organization_id'),'division_id',JSON_EXTRACT(output,'$.division_id'),
 'account_id',JSON_EXTRACT(output,'$.account_id'),'algorithm_version',JSON_EXTRACT(output,'$.algorithm_version'),
 'status',JSON_EXTRACT(output,'$.status'),'input_hash',JSON_EXTRACT(output,'$.input_hash'),
 'homework',JSON_OBJECT('rating',JSON_EXTRACT(output,'$.homework.rating')),
 'exams',JSON_OBJECT('rating',JSON_EXTRACT(output,'$.exams.rating')),
 'tests',JSON_OBJECT('rating',JSON_EXTRACT(output,'$.tests.rating')),
 'final_rating',JSON_EXTRACT(output,'$.final_rating'))`

const tablePageSQL = `WITH ranked AS (
 SELECT account_id,profile,` + tableProjection + ` AS summary,
 ROW_NUMBER() OVER (ORDER BY CAST(JSON_UNQUOTE(JSON_EXTRACT(output,'$.final_rating')) AS DOUBLE) DESC,
 CAST(JSON_UNQUOTE(JSON_EXTRACT(profile,'$.full_name')) AS BINARY),account_id) AS place
 FROM ratings_items_v1 WHERE job_id=? AND status='ready'
) SELECT profile,summary,place FROM ranked WHERE ` + tableSearch + ` ORDER BY place LIMIT ? OFFSET ?`

func validTableRow(v tableRow, j Job) bool {
	r := v.Result
	if v.Place < 1 || v.Account.AccountID < 1 || r.AccountID != v.Account.AccountID || r.JobID != j.ID || r.Revision != j.Revision || r.OrganizationID != j.OrganizationID || r.DivisionID != j.DivisionID || r.Status != "ready" || r.Algorithm != Algorithm || len(r.InputHash) != 64 {
		return false
	}
	for _, n := range []*float64{r.Final, r.Homework.Rating, r.Exams.Rating, r.Tests.Rating} {
		if n == nil || math.IsNaN(*n) || math.IsInf(*n, 0) {
			return false
		}
	}
	return true
}

func (s *Server) table(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !viewJobs(a) {
		fail(w, denied)
		return
	}
	n, offset, e := queryLimit(r)
	if e != nil {
		fail(w, e)
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	tx, e := s.Store.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	j, e := s.publication(r, tx, a, d, false)
	if e != nil {
		fail(w, e)
		return
	}
	rows := []tableRow{}
	total := 0
	if j.ID != "" {
		e = tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM ratings_items_v1 WHERE job_id=? AND status='ready' AND "+tableSearch, j.ID, query, query).Scan(&total)
		if e != nil {
			fail(w, e)
			return
		}
		cursor, e := tx.QueryContext(r.Context(), tablePageSQL, j.ID, query, query, n, offset)
		if e != nil {
			fail(w, e)
			return
		}
		for cursor.Next() {
			var p, o []byte
			var row tableRow
			if e = cursor.Scan(&p, &o, &row.Place); e != nil {
				break
			}
			if json.Unmarshal(p, &row.Account) != nil || json.Unmarshal(o, &row.Result) != nil || !validTableRow(row, j) {
				e = unavailable
				break
			}
			rows = append(rows, row)
		}
		if e == nil {
			e = cursor.Err()
		}
		cursor.Close()
		if e != nil {
			fail(w, e)
			return
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, e)
		return
	}
	var publication any
	if j.ID != "" {
		publication = map[string]any{"version_id": j.ID, "revision": j.Revision, "as_of": j.AsOf, "date_from": j.DateFrom, "date_to": j.DateTo}
	}
	reply(w, 200, map[string]any{"ratings": rows, "total": total, "publication": publication})
}

func (s *Server) tableFreshness(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !viewJobs(a) {
		fail(w, denied)
		return
	}
	tx, e := s.Store.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	j, e := s.publication(r, tx, a, d, true)
	if e != nil {
		fail(w, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"freshness": s.freshness(r.Context(), j)})
}
