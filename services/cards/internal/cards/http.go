package cards

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Server struct {
	DB   *sql.DB
	Deps *Dependencies
}
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string { return e.Message }
func bad(s string) error          { return &APIError{400, s} }

var denied = &APIError{403, "access_denied"}
var missing = &APIError{404, "not_found"}
var conflict = &APIError{409, "conflict"}
var unavailable = &APIError{503, "dependency_unavailable"}

func reply(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, e error) {
	var api *APIError
	var my *mysql.MySQLError
	if errors.Is(e, sql.ErrNoRows) {
		e = missing
	}
	if errors.As(e, &my) && my.Number == 1062 {
		e = conflict
	}
	if errors.Is(e, context.DeadlineExceeded) {
		e = unavailable
	}
	if !errors.As(e, &api) {
		api = &APIError{500, "internal_error"}
	}
	reply(w, api.Code, map[string]any{"status": false, "error": api.Message})
}
func decode(r *http.Request, out any) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return &APIError{415, "json_required"}
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return bad("invalid_request")
	}
	return nil
}
func validText(s string, n int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= n && utf8.ValidString(s)
}
func number(raw string) (int64, error) {
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n < 1 {
		return 0, bad("invalid_id")
	}
	return n, nil
}

type handler func(*http.Request, Actor, Scope) (any, error)
type route struct {
	Pattern string
	Fn      handler
	Input   any
}

func (s *Server) routes() []route {
	return []route{
		{"GET /v1/cards/admin/catalog", s.adminCatalog, nil},
		{"GET /v1/cards/catalog", s.catalogDirections, nil},
		{"GET /v1/cards/directions/{direction}/catalog", s.catalogMetadata, nil},
		{"POST /v1/cards/catalog/stats", s.catalogStats, CatalogStatsRequest{}},
		{"GET /v1/cards/tree", s.tree, nil}, {"GET /v1/cards/directions/{direction}/sections", s.sections, nil},
		{"GET /v1/cards/sections/{kind}/{section}", s.study, nil}, {"GET /v1/cards/sections/{kind}/{section}/batches/{batch}", s.batch, nil},
		{"PUT /v1/cards/sections/{kind}/{section}/settings", s.settings, Settings{}}, {"POST /v1/cards/sections/{kind}/{section}/learned", s.mark, MarkDTO{}}, {"DELETE /v1/cards/sections/{kind}/{section}/learned", s.unmark, MarkDTO{}},
		{"POST /v1/cards/themes", s.themeSave, ThemeDTO{}}, {"PUT /v1/cards/themes/{theme}", s.themeSave, ThemeDTO{}}, {"DELETE /v1/cards/themes/{theme}", s.themeDelete, RevisionDTO{}},
		{"GET /v1/cards/themes/{theme}/cards", s.adminCards, nil}, {"POST /v1/cards/themes/{theme}/cards", s.cardSave, CardDTO{}}, {"PUT /v1/cards/themes/{theme}/cards/{card}", s.cardSave, CardDTO{}}, {"DELETE /v1/cards/themes/{theme}/cards/{card}", s.cardDelete, RevisionDTO{}},
		{"GET /v1/cards/imports/targets", s.importTargets, nil},
		{"GET /v1/cards/imports/contract", s.importContract, nil},
		{"GET /v1/cards/receipts/{command}", s.receiptGet, nil},
		{"GET /v1/cards/sessions/{session}/source", s.sessionSource, nil},
		{"POST /v1/cards/imports/parse", s.parseImport, nil}, {"GET /v1/cards/sessions/{session}", s.sessionGet, nil}, {"PUT /v1/cards/sessions/{session}", s.sessionUpdate, SessionEdit{}}, {"POST /v1/cards/sessions/{session}/commit", s.sessionCommit, RevisionDTO{}}, {"POST /v1/cards/transforms", s.transform, TransformDTO{}},
	}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/cards/imports/template", s.importTemplate)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"status": true}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if e := s.DB.PingContext(r.Context()); e != nil {
			fail(w, unavailable)
			return
		}
		for _, base := range []string{s.Deps.Config.AuthURL, s.Deps.Config.CoreURL, s.Deps.Config.TestsURL} {
			req, _ := http.NewRequestWithContext(r.Context(), "GET", base+"/health/ready", nil)
			res, e := s.Deps.HTTP.Do(req)
			if e != nil {
				fail(w, unavailable)
				return
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				fail(w, unavailable)
				return
			}
		}
		reply(w, 200, map[string]bool{"status": true})
	})
	for _, spec := range s.routes() {
		fn := spec.Fn
		mux.HandleFunc(spec.Pattern, func(w http.ResponseWriter, r *http.Request) {
			a, scope, e := s.identify(r)
			if e != nil {
				fail(w, e)
				return
			}
			out, e := fn(r, a, scope)
			if e != nil {
				fail(w, e)
				return
			}
			reply(w, 200, map[string]any{"status": true, "data": out})
		})
	}
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, s.openAPI()) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		op, e := parseOperationRequest(r, s.Deps.Config)
		if e != nil {
			fail(w, e)
			return
		}
		if op != nil {
			a, scope, binding, err := s.Deps.authorizeOperation(r, *op)
			if err != nil {
				fail(w, err)
				return
			}
			r = withOperationAuthorization(r, operationAuthorization{Request: *op, Binding: binding})
			r = r.WithContext(context.WithValue(r.Context(), operationIdentityKey{}, operationIdentity{a, scope}))
		}
		mux.ServeHTTP(w, r)
	})
}
