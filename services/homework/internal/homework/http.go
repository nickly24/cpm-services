package homework

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Server struct {
	DB       *sql.DB
	Deps     *Dependencies
	Storage  *Storage
	Config   Config
	BusReady atomic.Bool
	Workers  workerHealth
}
type handler func(*http.Request, Actor, int64, References) (any, int, error)

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	a := asError(translate(e))
	reply(w, a.Code, map[string]any{"status": false, "error": a.Message})
}
func decode(r *http.Request, v any) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return &APIError{415, "json_required"}
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return bad("invalid_request")
	}
	return nil
}
func id(r *http.Request, key string) (int64, error) {
	v, e := strconv.ParseInt(r.PathValue(key), 10, 64)
	if e != nil || v < 1 {
		return 0, bad("invalid_id")
	}
	return v, nil
}
func queryID(r *http.Request, key string) (int64, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return 0, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 1 {
		return 0, bad("invalid_filter")
	}
	return n, nil
}
func page(r *http.Request) (int, int64, error) {
	limit := 50
	after := int64(0)
	var e error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil {
			return 0, 0, bad("invalid_pagination")
		}
	}
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, e = strconv.ParseInt(raw, 10, 64)
		if e != nil {
			return 0, 0, bad("invalid_pagination")
		}
	}
	if limit < 1 || limit > 200 || after < 0 {
		return 0, 0, bad("invalid_pagination")
	}
	return limit, after, nil
}
func (s *Server) route(mux *http.ServeMux, pattern string, fn handler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		a, e := s.Deps.Identify(r)
		if e != nil {
			fail(w, e)
			return
		}
		d, e := id(r, "division")
		if e != nil {
			fail(w, e)
			return
		}
		if !a.OrganizationAdmin && (a.DivisionID == nil || *a.DivisionID != d) {
			fail(w, missing)
			return
		}
		ref, e := s.Deps.References(r.Context(), a.OrganizationID, d)
		if e != nil {
			fail(w, e)
			return
		}
		data, code, e := fn(r, a, d, ref)
		if e != nil {
			fail(w, e)
			return
		}
		if code == 0 {
			code = 200
		}
		reply(w, code, map[string]any{"status": true, "data": data})
	})
}
func (s *Server) internal(r *http.Request) bool {
	return len(s.Config.Key) >= 32 && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.Config.Key)) == 1
}
func (s *Server) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]any{"status": true}) })
	m.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if s.DB.PingContext(r.Context()) != nil {
			reply(w, 503, map[string]any{"status": false})
			return
		}
		reply(w, 200, map[string]any{"status": true, "rabbitmq": s.BusReady.Load()})
	})
	m.HandleFunc("GET /openapi.json", s.openapi)
	m.HandleFunc("POST /internal/homework/file-jobs/{job}/claim", s.claimJob)
	m.HandleFunc("POST /internal/homework/file-jobs/{job}/release", s.releaseJob)
	m.HandleFunc("POST /internal/homework/file-work/heartbeat", s.workerHeartbeat)
	m.HandleFunc("POST /internal/homework/ratings/snapshot", s.ratingSnapshot)
	routes := []struct {
		p  string
		fn handler
	}{
		{"GET /types", s.types}, {"POST /types", s.typeSave}, {"PUT /types/{type}", s.typeSave}, {"DELETE /types/{type}", s.typeArchive},
		{"GET /assignments", s.assignments}, {"POST /assignments", s.assignmentSave}, {"GET /assignments/{assignment}", s.assignmentGet}, {"PUT /assignments/{assignment}", s.assignmentSave}, {"DELETE /assignments/{assignment}", s.assignmentDelete}, {"GET /assignments/{assignment}/students", s.assignmentStudents},
		{"GET /settings", s.settingsGet}, {"PUT /settings", s.settingsPut}, {"GET /report", s.report}, {"GET /audit", s.auditList},
		{"POST /assignments/{assignment}/manual", s.manual}, {"PUT /assignments/{assignment}/manual/{account}", s.manual}, {"POST /assignments/{assignment}/manual/bulk", s.manualBulk},
		{"GET /assignments/{assignment}/workspace", s.workspace}, {"POST /assignments/{assignment}/uploads", s.createUpload}, {"POST /uploads/{job}/complete", s.completeUpload}, {"GET /jobs/active", s.activeJobs}, {"GET /jobs/{job}", s.jobGet}, {"POST /jobs/{job}/cancel", s.cancelJob}, {"POST /jobs/{job}/retry", s.retryJob},
		{"POST /assignments/{assignment}/submit", s.submit}, {"DELETE /assignments/{assignment}/draft", s.removeDraft}, {"GET /review-queue", s.reviewQueue}, {"GET /archive", s.archive}, {"GET /monitoring", s.monitoring}, {"GET /submissions/{submission}/file-url", s.fileURL},
	}
	for _, rt := range routes {
		s.route(m, ""+strings.Split(rt.p, " ")[0]+" /v1/homework/divisions/{division}"+strings.Split(rt.p, " ")[1], rt.fn)
	}
	for _, action := range []string{"claim", "takeover", "release", "request-revision", "grade", "edit-grade", "resubmit"} {
		s.route(m, "POST /v1/homework/divisions/{division}/submissions/{submission}/"+action, s.transition)
	}
	return m
}
func (s *Server) assignment(ctx context.Context, db DBTX, a Actor, d, id int64, lock bool) (map[string]any, error) {
	q := "SELECT h.*,t.name type_name FROM hw_assignments h JOIN hw_types t ON t.id=h.type_id WHERE h.id=? AND h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL"
	if lock {
		q += " FOR UPDATE"
	}
	h, e := one(ctx, db, q, id, a.OrganizationID, d)
	if e != nil {
		return nil, e
	}
	if a.Role == "student" && num(h["published"]) != 1 {
		return nil, &APIError{403, "homework_not_published"}
	}
	return h, nil
}
func (s *Server) submission(ctx context.Context, db DBTX, assignment, account int64, create, lock bool) (map[string]any, error) {
	if create {
		if _, e := db.ExecContext(ctx, "INSERT INTO hw_submissions(assignment_id,account_id,revision_comment) VALUES(?,?,'') ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id)", assignment, account); e != nil {
			return nil, e
		}
	}
	q := "SELECT * FROM hw_submissions WHERE assignment_id=? AND account_id=?"
	if lock {
		q += " FOR UPDATE"
	}
	return one(ctx, db, q, assignment, account)
}
