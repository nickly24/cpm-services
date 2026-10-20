package core

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	DB          *sql.DB
	Auth        *AuthClient
	InternalKey string
	Reports     *ReportClient
}
type handler func(*http.Request, Actor, int64) (any, int, error)

func reply(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, err error) {
	err = translate(err)
	var e *APIError
	if errors.As(err, &e) {
		reply(w, e.Code, map[string]any{"status": false, "error": e.Message})
		return
	}
	slog.Error("core request failed", "error_type", stringType(err))
	reply(w, 500, map[string]any{"status": false, "error": "internal_error"})
}
func stringType(err error) string {
	if err == nil {
		return "nil"
	}
	return "database_or_runtime"
} // never log SQL text, credentials or profile data
func decode(r *http.Request, dto any) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return &APIError{415, "json_required"}
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(dto) != nil {
		return bad("invalid_request")
	}
	if dec.Decode(new(any)) != io.EOF {
		return bad("invalid_request")
	}
	return nil
}
func id(r *http.Request, key string) (int64, error) {
	n, e := strconv.ParseInt(r.PathValue(key), 10, 64)
	if e != nil || n < 1 {
		return 0, bad("invalid_id")
	}
	return n, nil
}
func page(r *http.Request) (int, int, error) {
	limit, offset := 100, 0
	var e error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, e = strconv.Atoi(v)
		if e != nil {
			return 0, 0, bad("invalid_pagination")
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, e = strconv.Atoi(v)
		if e != nil {
			return 0, 0, bad("invalid_pagination")
		}
	}
	if limit < 1 || limit > 200 || offset < 0 || offset > 100000 {
		return 0, 0, bad("invalid_pagination")
	}
	return limit, offset, nil
}
func ok(body any) (any, int, error) { return body, 200, nil }
func (s *Server) route(mux *http.ServeMux, pattern string, fn handler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		a, e := s.Auth.Identify(r)
		if e != nil {
			fail(w, e)
			return
		}
		d := int64(0)
		if r.PathValue("division") != "" {
			d, e = id(r, "division")
			if e == nil {
				var found int64
				e = s.DB.QueryRowContext(r.Context(), "SELECT id FROM divisions WHERE id=? AND organization_id=? AND is_active=1", d, a.OrganizationID).Scan(&found)
			}
			if e != nil {
				fail(w, e)
				return
			}
			if !a.OrganizationAdmin && (a.DivisionID == nil || *a.DivisionID != d) {
				fail(w, denied)
				return
			}
		}
		body, code, e := fn(r, a, d)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, code, map[string]any{"status": true, "data": body})
	})
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"status": true}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), 3*time.Second)
		defer c()
		var n int
		if s.DB.PingContext(ctx) != nil || s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM core_group_scope").Scan(&n) != nil {
			fail(w, unavailable)
			return
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", s.Auth.URL+"/health/ready", nil)
		res, e := s.Auth.HTTP.Do(req)
		if e != nil {
			fail(w, unavailable)
			return
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			fail(w, unavailable)
			return
		}
		reply(w, 200, map[string]bool{"status": true})
	})
	s.routes(mux)
	mux.HandleFunc("POST /internal/core/attendance/history-reservations", s.attendanceHistory)
	mux.HandleFunc("POST /internal/core/attendance/cell-targets", s.attendanceCells)
	mux.HandleFunc("POST /internal/core/test-references", s.testReferences)
	mux.HandleFunc("POST /internal/core/exam-references", s.examReferences)
	mux.HandleFunc("GET /internal/core/cards-context", s.cardsContext)
	mux.HandleFunc("GET /internal/core/import-directions/{direction}", s.importDirection)
	mux.HandleFunc("GET /internal/core/organizations/{organization}/test-settings", s.internalOrganizationTestSettings)
	mux.HandleFunc("POST /internal/core/homework-references", s.homeworkReferences)
	mux.HandleFunc("POST /internal/core/ratings/snapshot", s.ratingSnapshot)
	mux.HandleFunc("POST /internal/core/rating-locks", func(w http.ResponseWriter, r *http.Request) {
		if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
			fail(w, &APIError{401, "invalid_service_key"})
			return
		}
		var in struct {
			AccountID  int64 `json:"account_id"`
			DivisionID int64 `json:"division_id"`
		}
		if e := decode(r, &in); e != nil {
			fail(w, e)
			return
		}
		if in.AccountID < 1 || in.DivisionID < 1 {
			fail(w, bad("invalid_id"))
			return
		}
		e := s.transact(r.Context(), func(tx *sql.Tx) error {
			var role string
			if e := tx.QueryRowContext(r.Context(), "SELECT role FROM auth_users WHERE id=? FOR UPDATE", in.AccountID).Scan(&role); e != nil {
				return e
			}
			if role != "student" {
				return bad("student_required")
			}
			_, e := tx.ExecContext(r.Context(), "INSERT INTO core_rating_locks(user_id,division_id) VALUES(?,?) ON DUPLICATE KEY UPDATE user_id=VALUES(user_id)", in.AccountID, in.DivisionID)
			return e
		})
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]bool{"status": true})
	})
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, openAPI()) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if e := validateOperationTransport(r); e != nil {
			fail(w, e)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		ctx, c := context.WithTimeout(r.Context(), 90*time.Second)
		defer c()
		defer func() {
			if recover() != nil {
				fail(w, errors.New("panic"))
			}
		}()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
