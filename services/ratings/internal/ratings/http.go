package ratings

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Store  *Store
	Deps   *Dependencies
	Config Config
}

func reply(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": data})
}
func fail(w http.ResponseWriter, e error) {
	var a *APIError
	if !errors.As(e, &a) {
		a = &APIError{503, "ratings_storage_unavailable"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.Code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": a.Name})
}
func decode(r *http.Request, v any) error {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return &APIError{415, "json_required"}
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
	if e != nil || len(b) > 4<<20 {
		return bad("invalid_json")
	}
	return strict(b, v)
}
func (s *Server) internal(r *http.Request) bool {
	return len(s.Config.ServiceKey) >= 32 && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.Config.ServiceKey)) == 1
}
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]any{"service": "ratings"}) })
	m.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if s.Store.DB.PingContext(r.Context()) != nil {
			fail(w, unavailable)
			return
		}
		reply(w, 200, map[string]any{"service": "ratings", "exams_mode": s.Config.ExamsMode, "external_tests_mode": s.Config.ExternalTestsMode, "exams_connected": s.Config.ExamsURL != ""})
	})
	m.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openAPI())
	})
	m.HandleFunc("POST /v1/ratings/divisions/{division}/jobs", s.public(s.create))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/jobs", s.public(s.jobs))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/jobs/{job}", s.public(s.job))
	m.HandleFunc("POST /v1/ratings/divisions/{division}/jobs/{job}/cancel", s.public(s.cancel))
	m.HandleFunc("POST /v1/ratings/divisions/{division}/jobs/{job}/retry", s.public(s.retry))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/ratings", s.public(s.list))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/table", s.public(s.table))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/freshness", s.public(s.tableFreshness))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/me", s.public(s.me))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/accounts/{account}", s.public(s.details))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/versions", s.public(s.versions))
	m.HandleFunc("GET /v1/ratings/divisions/{division}/report", s.public(s.report))
	m.HandleFunc("GET /internal/ratings/jobs/{job}/accounts/{account}/input", s.input)
	m.HandleFunc("POST /internal/ratings/jobs/{job}/accounts/{account}/result", s.result)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}

type business func(http.ResponseWriter, *http.Request, Actor, int64)

func (s *Server) public(h business) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, e := s.Deps.Identify(r)
		if e != nil {
			fail(w, e)
			return
		}
		div, e := strconv.ParseInt(r.PathValue("division"), 10, 64)
		if e != nil || div < 1 {
			fail(w, bad("invalid_division"))
			return
		}
		if !a.OrganizationAdmin && (a.DivisionID == nil || *a.DivisionID != div) {
			fail(w, denied)
			return
		}
		var refs struct {
			Divisions []struct {
				ID     int64 `json:"id"`
				Active bool  `json:"is_active"`
			} `json:"divisions"`
		}
		e = s.Deps.Call(r.Context(), s.Config.CoreURL, "POST", "/internal/core/test-references", map[string]any{"organization_id": a.OrganizationID, "division_ids": []int64{div}}, &refs)
		if e != nil {
			fail(w, e)
			return
		}
		if len(refs.Divisions) != 1 || refs.Divisions[0].ID != div || !refs.Divisions[0].Active {
			fail(w, missing)
			return
		}
		h(w, r, a, div)
	}
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (s *Server) create(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !a.Can(true) {
		fail(w, denied)
		return
	}
	var in struct {
		DateFrom string `json:"date_from"`
		DateTo   string `json:"date_to"`
	}
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	if !validPeriod(in.DateFrom, in.DateTo) {
		fail(w, bad("invalid_period"))
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		fail(w, bad("idempotency_key_required"))
		return
	}
	j, new, e := s.Store.Create(r.Context(), a, d, in.DateFrom, in.DateTo, key)
	if e != nil {
		fail(w, e)
		return
	}
	code := 200
	if new {
		code = 202
	}
	reply(w, code, map[string]any{"job": j, "status_url": "/v1/ratings/divisions/" + strconv.FormatInt(d, 10) + "/jobs/" + j.ID})
}
func (s *Server) scopedJob(r *http.Request, a Actor, d int64) (Job, error) {
	id := r.PathValue("job")
	if !validUUID(id) {
		return Job{}, bad("invalid_job")
	}
	j, e := s.Store.Job(r.Context(), id)
	if e != nil {
		return j, e
	}
	if j.OrganizationID != a.OrganizationID || j.DivisionID != d {
		return j, missing
	}
	return j, nil
}
func viewJobs(a Actor) bool { return a.Can(false) || a.Role == "supervisor" }
func (s *Server) job(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !viewJobs(a) {
		fail(w, denied)
		return
	}
	j, e := s.scopedJob(r, a, d)
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, j)
}
func queryLimit(r *http.Request) (int, int, error) {
	n, o := 100, 0
	var e error
	if v := r.URL.Query().Get("limit"); v != "" {
		n, e = strconv.Atoi(v)
		if e != nil {
			return 0, 0, bad("invalid_limit")
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		o, e = strconv.Atoi(v)
		if e != nil {
			return 0, 0, bad("invalid_offset")
		}
	}
	if n < 1 || n > 200 || o < 0 || o > 100000 {
		return 0, 0, bad("invalid_pagination")
	}
	return n, o, nil
}
func (s *Server) jobs(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !viewJobs(a) {
		fail(w, denied)
		return
	}
	n, o, e := queryLimit(r)
	if e != nil {
		fail(w, e)
		return
	}
	rows, e := s.Store.DB.QueryContext(r.Context(), "SELECT "+jobColumns+" FROM ratings_jobs_v1 WHERE organization_id=? AND division_id=? ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", a.OrganizationID, d, n, o)
	if e != nil {
		fail(w, e)
		return
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			fail(w, e)
			return
		}
		out = append(out, j)
	}
	if e = rows.Err(); e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"jobs": out})
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !a.Can(true) {
		fail(w, denied)
		return
	}
	var in struct{}
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	j, e := s.scopedJob(r, a, d)
	if e != nil {
		fail(w, e)
		return
	}
	if j.Status == "completed" {
		fail(w, conflict("rating_already_published"))
		return
	}
	j.LeaseToken = nil
	if e = s.Store.Finish(r.Context(), j, "cancelled", "cancelled_by_user"); e != nil {
		fail(w, e)
		return
	}
	j, e = s.Store.Job(r.Context(), j.ID)
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, j)
}
func (s *Server) retry(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !a.Can(true) {
		fail(w, denied)
		return
	}
	var in struct{}
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	j, e := s.scopedJob(r, a, d)
	if e != nil {
		fail(w, e)
		return
	}
	if !terminal(j.Status) || j.Status == "completed" {
		fail(w, conflict("job_not_retryable"))
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		fail(w, bad("idempotency_key_required"))
		return
	}
	next, new, e := s.Store.Create(r.Context(), a, d, j.DateFrom, j.DateTo, key)
	if e != nil {
		fail(w, e)
		return
	}
	code := 200
	if new {
		code = 202
	}
	reply(w, code, next)
}
func (s *Server) input(w http.ResponseWriter, r *http.Request) {
	if !s.internal(r) {
		fail(w, &APIError{401, "invalid_service_key"})
		return
	}
	j, id, e := s.internalIDs(r)
	if e != nil {
		fail(w, e)
		return
	}
	if terminal(j.Status) && j.Status != "completed" {
		fail(w, conflict("stale_rating_job"))
		return
	}
	var raw []byte
	e = s.Store.DB.QueryRowContext(r.Context(), "SELECT input FROM ratings_items_v1 WHERE job_id=? AND account_id=?", j.ID, id).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		e = missing
	}
	if e != nil {
		fail(w, e)
		return
	}
	// MySQL JSON normalizes numeric lexemes. Re-encode the typed immutable DTO
	// so calculate receives the same Go canonical number representation used at capture.
	var in Input
	if json.Unmarshal(raw, &in) != nil {
		fail(w, unavailable)
		return
	}
	reply(w, 200, in)
}
func (s *Server) internalIDs(r *http.Request) (Job, int64, error) {
	id := r.PathValue("job")
	a, e := strconv.ParseInt(r.PathValue("account"), 10, 64)
	if e != nil || a < 1 || !validUUID(id) {
		return Job{}, 0, bad("invalid_identifier")
	}
	j, e := s.Store.Job(r.Context(), id)
	return j, a, e
}
func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	if !s.internal(r) {
		fail(w, &APIError{401, "invalid_service_key"})
		return
	}
	var in Result
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	j, id, e := s.internalIDs(r)
	if e != nil {
		fail(w, e)
		return
	}
	if in.JobID != j.ID || in.AccountID != id || in.Revision != j.Revision || in.OrganizationID != j.OrganizationID || in.DivisionID != j.DivisionID || in.Algorithm != Algorithm || in.Status != "ready" && in.Status != "failed" {
		fail(w, conflict("stale_rating_input"))
		return
	}
	// Only the offline migration may retain ambiguous old detail entries. Live
	// Calculate results must continue to have one outcome per exam.
	if historicalExamEntries(in.Exams) {
		fail(w, bad("historical_entry_read_only"))
		return
	}
	if e = validateResult(in); e != nil {
		fail(w, e)
		return
	}
	var expected string
	e = s.Store.DB.QueryRowContext(r.Context(), "SELECT input_hash FROM ratings_items_v1 WHERE job_id=? AND account_id=?", j.ID, id).Scan(&expected)
	if errors.Is(e, sql.ErrNoRows) {
		e = missing
	}
	if e != nil {
		fail(w, e)
		return
	}
	if in.InputHash != expected {
		fail(w, conflict("stale_rating_input"))
		return
	}
	if terminal(j.Status) && j.Status != "completed" {
		fail(w, conflict("stale_rating_job"))
		return
	}
	if in.Status == "ready" {
		if e = s.Deps.Call(r.Context(), s.Config.CoreURL, "POST", "/internal/core/rating-locks", map[string]any{"account_id": id, "division_id": j.DivisionID}, nil); e != nil {
			fail(w, e)
			return
		}
	}
	raw, _ := json.Marshal(in)
	outHash, _ := canonicalHash(raw, "")
	if e = s.Store.SaveResult(r.Context(), j, id, in.Status, in.ErrorCode, raw, outHash); e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"accepted": true})
}
func validateResult(r Result) error {
	if !regexp.MustCompile(`^[a-z0-9_]{0,100}$`).MatchString(r.ErrorCode) {
		return bad("invalid_error_code")
	}
	if r.Status == "failed" {
		if r.ErrorCode == "" {
			return bad("error_code_required")
		}
		return nil
	}
	if r.Status != "ready" || !validResultNumber(r.Final) {
		return bad("invalid_result")
	}
	for kind, raw := range map[string]json.RawMessage{"homework": r.Homework, "tests": r.Tests, "exams": r.Exams} {
		var component struct {
			Rating     *float64            `json:"rating"`
			Details    []json.RawMessage   `json:"details"`
			Directions map[string]*float64 `json:"directions"`
		}
		if strict(raw, &component) != nil || !validResultNumber(component.Rating) || component.Details == nil {
			return bad("incomplete_rating_result")
		}
		if kind == "tests" {
			if component.Directions == nil {
				return bad("incomplete_rating_result")
			}
			for _, score := range component.Directions {
				if !validResultNumber(score) {
					return bad("incomplete_rating_result")
				}
			}
		}
		seen := map[string]bool{}
		for _, detail := range component.Details {
			identity, ok := validResultDetail(kind, detail)
			if !ok || seen[identity] {
				return bad("incomplete_rating_result")
			}
			seen[identity] = true
		}
	}
	return nil
}

func validResultNumber(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}

func validResultDate(value *string) bool {
	if value == nil {
		return false
	}
	_, err := time.Parse(time.DateOnly, *value)
	return err == nil
}

func validResultNullableID(raw json.RawMessage) bool {
	var value *int64
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil && (value == nil || *value > 0)
}

func historicalExamEntries(raw json.RawMessage) bool {
	var component struct {
		Details []map[string]json.RawMessage `json:"details"`
	}
	if json.Unmarshal(raw, &component) != nil {
		return false
	}
	for _, detail := range component.Details {
		if _, exists := detail["legacy_entry_id"]; exists {
			return true
		}
	}
	return false
}

// Validate the Calculate wire contract without regrading or changing persisted
// output bytes. Only calculation_status may be absent in historical test details.
func validResultDetail(kind string, raw json.RawMessage) (string, bool) {
	switch kind {
	case "homework":
		var detail struct {
			ID       *int64          `json:"homework_id"`
			Name     *string         `json:"name"`
			Deadline *string         `json:"deadline"`
			Score    *float64        `json:"score"`
			Status   *string         `json:"status"`
			DatePass json.RawMessage `json:"date_pass"`
		}
		var datePass *string
		if strict(raw, &detail) != nil || detail.ID == nil || *detail.ID < 1 || detail.Name == nil || !validResultDate(detail.Deadline) || !validResultNumber(detail.Score) || detail.Status == nil || len(detail.DatePass) == 0 || json.Unmarshal(detail.DatePass, &datePass) != nil {
			return "", false
		}
		if *detail.Status != "Сдано" && *detail.Status != "Не сдано" || *detail.Status == "Не сдано" && *detail.Score != 0 {
			return "", false
		}
		return strconv.FormatInt(*detail.ID, 10), true
	case "tests":
		var detail struct {
			ID        *string         `json:"test_id"`
			Title     *string         `json:"title"`
			Direction *string         `json:"direction"`
			Score     *float64        `json:"score"`
			Source    *string         `json:"source"`
			Status    json.RawMessage `json:"calculation_status"`
		}
		if strict(raw, &detail) != nil || detail.ID == nil || *detail.ID == "" || detail.Title == nil || detail.Direction == nil || !validResultNumber(detail.Score) || detail.Source == nil || *detail.Source != "internal" && *detail.Source != "external" {
			return "", false
		}
		if len(detail.Status) > 0 {
			var status *string
			if json.Unmarshal(detail.Status, &status) != nil || status == nil || *status != "ready" && *status != "missing" || *status == "missing" && *detail.Score != 0 {
				return "", false
			}
		}
		return *detail.Source + ":" + *detail.ID, true
	case "exams":
		var detail struct {
			ID            *string         `json:"exam_id"`
			Name          *string         `json:"exam_name"`
			Date          *string         `json:"exam_date"`
			Score         *float64        `json:"score"`
			Status        *string         `json:"status"`
			Type          *string         `json:"exam_type"`
			DirectionID   json.RawMessage `json:"direction_id"`
			AttemptNo     json.RawMessage `json:"attempt_no"`
			HasAppeal     *bool           `json:"has_appeal"`
			MissingResult *bool           `json:"missing_result"`
			LegacyEntryID *string         `json:"legacy_entry_id,omitempty"`
		}
		if strict(raw, &detail) != nil || detail.ID == nil || *detail.ID == "" || detail.Name == nil || !validResultDate(detail.Date) || !validResultNumber(detail.Score) || detail.Status == nil || detail.Type == nil || !validResultNullableID(detail.DirectionID) || !validResultNullableID(detail.AttemptNo) || detail.HasAppeal == nil || detail.MissingResult == nil {
			return "", false
		}
		if *detail.MissingResult {
			if *detail.Status != "Не сдавал" || *detail.Score != 0 {
				return "", false
			}
		} else if *detail.Status != "Сдан" {
			return "", false
		}
		if detail.LegacyEntryID != nil {
			if !regexp.MustCompile(`^legacy:[0-9a-f]{24}:[1-9][0-9]{0,5}$`).MatchString(*detail.LegacyEntryID) {
				return "", false
			}
			return *detail.ID + ":" + *detail.LegacyEntryID, true
		}
		return *detail.ID, true
	}
	return "", false
}
func (s *Store) SaveResult(ctx context.Context, j Job, a int64, status, reason string, raw []byte, h string) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var revision int64
	var active *string
	if e = tx.QueryRowContext(ctx, "SELECT revision,active_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=? FOR UPDATE", j.OrganizationID, j.DivisionID).Scan(&revision, &active); e != nil {
		return e
	}
	var current string
	if e = tx.QueryRowContext(ctx, "SELECT status FROM ratings_jobs_v1 WHERE id=? FOR UPDATE", j.ID).Scan(&current); e != nil {
		return e
	}
	var old string
	var oldHash *string
	if e = tx.QueryRowContext(ctx, "SELECT status,output_hash FROM ratings_items_v1 WHERE job_id=? AND account_id=? FOR UPDATE", j.ID, a).Scan(&old, &oldHash); e != nil {
		return e
	}
	if oldHash != nil {
		if *oldHash == h {
			return nil
		}
		return conflict("result_idempotency_conflict")
	}
	if terminal(current) || revision != j.Revision || active == nil || *active != j.ID {
		return conflict("stale_rating_job")
	}
	if old != "queued" {
		return conflict("stale_rating_item")
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_items_v1 SET status=?,output=?,output_hash=?,error_code=?,completed_at=? WHERE job_id=? AND account_id=?", status, raw, h, reason, time.Now().UTC(), j.ID, a)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET processed_count=processed_count+1,status='running',next_attempt_at=? WHERE id=?", time.Now().UTC(), j.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}

type publishedRow struct {
	Account Account `json:"profile"`
	Result  Result  `json:"rating"`
	Place   int     `json:"place"`
}

func (s *Server) readPublished(r *http.Request, a Actor, d int64) (Job, []publishedRow, error) {
	if !a.Can(false) && a.Role != "supervisor" && a.Role != "student" && a.Role != "proctor" {
		return Job{}, nil, denied
	}
	version := r.URL.Query().Get("version")
	if version != "" && !validUUID(version) {
		return Job{}, nil, bad("invalid_version")
	}
	tx, e := s.Store.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return Job{}, nil, e
	}
	defer tx.Rollback()
	var id *string
	if version != "" {
		// An explicit historical version is scoped by its completed job below,
		// independently of whether this division has a current publication.
		id = &version
	} else {
		e = tx.QueryRowContext(r.Context(), "SELECT published_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=?", a.OrganizationID, d).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			return Job{}, []publishedRow{}, nil
		}
		if e != nil {
			return Job{}, nil, e
		}
		if id == nil {
			return Job{}, []publishedRow{}, nil
		}
	}
	j, e := scanJob(tx.QueryRowContext(r.Context(), "SELECT "+jobColumns+" FROM ratings_jobs_v1 WHERE id=? AND organization_id=? AND division_id=? AND status='completed'", id, a.OrganizationID, d))
	if e != nil {
		return Job{}, nil, e
	}
	rows, e := tx.QueryContext(r.Context(), "SELECT profile,output FROM ratings_items_v1 WHERE job_id=? AND status='ready' ORDER BY account_id", j.ID)
	if e != nil {
		return j, nil, e
	}
	out := []publishedRow{}
	for rows.Next() {
		var p, o []byte
		if e = rows.Scan(&p, &o); e != nil {
			rows.Close()
			return j, nil, e
		}
		var v publishedRow
		if json.Unmarshal(p, &v.Account) != nil || json.Unmarshal(o, &v.Result) != nil || validateResult(v.Result) != nil {
			rows.Close()
			return j, nil, unavailable
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return j, nil, e
	}
	if e = tx.Commit(); e != nil {
		return j, nil, e
	}
	allowed := map[int64]bool{}
	if a.Role == "proctor" {
		var live CoreSnapshot
		req := SnapshotRequest{a.OrganizationID, d, j.DateFrom, j.DateTo, j.AsOf.Format(time.RFC3339Nano)}
		if e = s.Deps.Call(r.Context(), s.Config.CoreURL, "POST", "/internal/core/ratings/snapshot", req, &live); e != nil {
			return j, nil, e
		}
		if live.OrganizationID != a.OrganizationID || live.DivisionID != d {
			return j, nil, unavailable
		}
		for _, p := range live.Accounts {
			if a.GroupID != nil && p.GroupID != nil && *a.GroupID == *p.GroupID {
				allowed[p.AccountID] = true
			}
		}
	}
	sort.SliceStable(out, func(i, k int) bool {
		if *out[i].Result.Final != *out[k].Result.Final {
			return *out[i].Result.Final > *out[k].Result.Final
		}
		if out[i].Account.FullName != out[k].Account.FullName {
			return out[i].Account.FullName < out[k].Account.FullName
		}
		return out[i].Account.AccountID < out[k].Account.AccountID
	})
	for i := range out {
		out[i].Place = i + 1
	}
	filtered := []publishedRow{}
	for _, v := range out {
		if a.Role == "student" && v.Account.AccountID != a.AccountID {
			continue
		}
		if a.Role == "proctor" && !allowed[v.Account.AccountID] {
			continue
		}
		filtered = append(filtered, v)
	}
	return j, filtered, nil
}
func (s *Server) freshness(ctx context.Context, j Job) map[string]any {
	if j.ID == "" {
		return map[string]any{"is_stale": true, "reason": "never_calculated"}
	}
	var snap Snapshot
	if json.Unmarshal(j.Snapshot, &snap) != nil {
		return map[string]any{"is_stale": true, "reason": "source_snapshot_invalid"}
	}
	if s.Store != nil && snap.ExamsMode == "required" {
		revision, err := sourceRevision(ctx, s.Store.DB, j, false)
		if err != nil || revision > snap.Exams.SourceRevision {
			reason := "source_changed"
			if err != nil {
				reason = "source_unavailable"
			}
			return map[string]any{"is_stale": true, "reason": reason, "version_id": j.ID, "revision": j.Revision, "as_of": j.AsOf, "exams_mode": snap.ExamsMode, "external_tests_mode": snap.ExternalTestsMode}
		}
	}
	next, e := s.Deps.Collect(ctx, j)
	reason := ""
	if e != nil {
		reason = "source_unavailable"
	} else if j.SourceHash == nil || *j.SourceHash != fingerprint(next) {
		reason = "source_changed"
	} else if transitioned(snap, time.Now().UTC()) {
		reason = "calendar_transition"
	}
	return map[string]any{"is_stale": reason != "", "reason": reason, "version_id": j.ID, "revision": j.Revision, "as_of": j.AsOf, "exams_mode": snap.ExamsMode, "external_tests_mode": snap.ExternalTestsMode}
}
func (s *Server) list(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	j, rows, e := s.readPublished(r, a, d)
	if e != nil {
		fail(w, e)
		return
	}
	n, o, e := queryLimit(r)
	if e != nil {
		fail(w, e)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	filtered := []publishedRow{}
	for _, v := range rows {
		if q == "" || strings.Contains(strings.ToLower(v.Account.FullName), q) || v.Account.GroupName != nil && strings.Contains(strings.ToLower(*v.Account.GroupName), q) {
			filtered = append(filtered, v)
		}
	}
	total := len(filtered)
	if o > total {
		o = total
	}
	end := o + n
	if end > total {
		end = total
	}
	reply(w, 200, map[string]any{"ratings": filtered[o:end], "total": total, "freshness": s.freshness(r.Context(), j)})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if a.Role != "student" {
		fail(w, denied)
		return
	}
	s.oneRating(w, r, a, d, a.AccountID)
}
func (s *Server) details(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	id, e := strconv.ParseInt(r.PathValue("account"), 10, 64)
	if e != nil || id < 1 {
		fail(w, bad("invalid_account"))
		return
	}
	s.oneRating(w, r, a, d, id)
}
func (s *Server) oneRating(w http.ResponseWriter, r *http.Request, a Actor, d, id int64) {
	if a.Role == "student" && id != a.AccountID {
		fail(w, missing)
		return
	}
	j, rows, e := s.readPublished(r, a, d)
	if e != nil {
		fail(w, e)
		return
	}
	for _, v := range rows {
		if v.Account.AccountID == id {
			reply(w, 200, map[string]any{"rating": v, "freshness": s.freshness(r.Context(), j)})
			return
		}
	}
	if j.ID == "" && a.Role == "student" {
		reply(w, 200, map[string]any{"rating": nil, "freshness": s.freshness(r.Context(), j)})
		return
	}
	fail(w, missing)
}
func (s *Server) versions(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !viewJobs(a) {
		fail(w, denied)
		return
	}
	r.URL.RawQuery = ""
	rows, e := s.Store.DB.QueryContext(r.Context(), "SELECT "+jobColumns+" FROM ratings_jobs_v1 WHERE organization_id=? AND division_id=? AND status='completed' ORDER BY revision DESC LIMIT 100", a.OrganizationID, d)
	if e != nil {
		fail(w, e)
		return
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			fail(w, e)
			return
		}
		out = append(out, j)
	}
	if e = rows.Err(); e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"versions": out})
}
func (s *Server) report(w http.ResponseWriter, r *http.Request, a Actor, d int64) {
	if !viewJobs(a) {
		fail(w, denied)
		return
	}
	j, rows, e := s.readPublished(r, a, d)
	if e != nil {
		fail(w, e)
		return
	}
	var period any
	if j.ID != "" {
		period = map[string]any{"date_from": j.DateFrom, "date_to": j.DateTo}
	}
	reply(w, 200, map[string]any{"period": period, "students": rows, "freshness": s.freshness(r.Context(), j)})
}
