package tests

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Store        *Store
	Dependencies *Dependencies
	Config       Config
	metricsOnce  sync.Once
	metrics      *serviceMetrics
}
type handler func(*http.Request, Actor, int64) (any, int, error)
type ErrorDTO struct {
	Status bool   `json:"status"`
	Error  string `json:"error"`
}
type Envelope struct {
	Status bool `json:"status"`
	Data   any  `json:"data"`
}

func reply(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, e error) {
	var api *APIError
	if errors.As(e, &api) {
		reply(w, api.Code, ErrorDTO{false, api.Message})
		return
	}
	// Error text may contain database addresses or authentication configuration.
	slog.Error("tests request failed", "error_type", "database_or_runtime")
	reply(w, 500, ErrorDTO{false, "internal_error"})
}
func decode(r *http.Request, dto any) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return &APIError{415, "json_required"}
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(dto) != nil || dec.Decode(new(any)) != io.EOF {
		return bad("invalid_request")
	}
	return nil
}
func numericID(v string) (int64, error) {
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 1 {
		return 0, bad("invalid_id")
	}
	return n, nil
}
func testID(r *http.Request) (bson.ObjectID, error) {
	id, e := bson.ObjectIDFromHex(r.PathValue("test"))
	if e != nil {
		return id, bad("invalid_id")
	}
	return id, nil
}
func (s *Server) load(r *http.Request, a Actor) (Document, error) {
	id, e := testID(r)
	if e != nil {
		return Document{}, e
	}
	return s.Store.Get(r.Context(), a.OrganizationID, id)
}
func (s *Server) scope(r *http.Request, a Actor, d int64) error {
	if !a.InDivision(d) {
		return denied
	}
	// Auth/me loaded active organisation/division memberships for this request.
	// Only the selected cabinet attested by the BFF can reuse that check; direct
	// bearer calls and references to other divisions still consult Core.
	if trusted, _ := r.Context().Value(gatewayContextKey{}).(bool); trusted && a.DivisionID != nil && *a.DivisionID == d {
		return nil
	}
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{d}, nil)
	if e != nil {
		return e
	}
	if !refs.Divisions[0].Active {
		return missing
	}
	return nil
}
func (s *Server) route(mux *http.ServeMux, pattern string, fn handler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		a, e := s.Dependencies.Identify(r)
		if e != nil {
			fail(w, e)
			return
		}
		var d int64
		if raw := r.PathValue("division"); raw != "" {
			d, e = numericID(raw)
			if e == nil {
				e = s.scope(r, a, d)
			}
			if e != nil {
				fail(w, e)
				return
			}
		}
		body, code, e := fn(r, a, d)
		if e != nil {
			fail(w, e)
			return
		}
		if file, ok := body.(importDownload); ok {
			w.Header().Set("Content-Type", file.MIME)
			w.Header().Set("Content-Disposition", `attachment; filename="`+file.Filename+`"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(file.Body)))
			w.WriteHeader(code)
			_, _ = w.Write(file.Body)
			return
		}
		reply(w, code, Envelope{true, body})
	})
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	metrics := s.telemetry()
	mux.HandleFunc("GET /metrics", s.metricsHTTP)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"status": true}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		e := s.Store.Collection.Database().Client().Ping(ctx, nil)
		if e == nil {
			e = s.Dependencies.Ready(ctx)
		}
		if e != nil {
			fail(w, unavailable)
			return
		}
		reply(w, 200, map[string]bool{"status": true})
	})
	for _, spec := range s.specs() {
		// One generic catalog-read pattern avoids ServeMux ambiguity with
		// /attempts/{attempt} and /submissions/{submission}. Those static
		// prefixes remain more specific; OpenAPI keeps concrete catalog routes.
		if spec.Pattern == "GET /v1/tests/{test}/content" || spec.Pattern == "GET /v1/tests/{test}/history" {
			continue
		}
		s.route(mux, spec.Pattern, spec.Handle)
	}
	s.route(mux, "GET /v1/tests/{test}/{section}", func(r *http.Request, a Actor, d int64) (any, int, error) {
		switch r.PathValue("section") {
		case "content":
			return s.contentGet(r, a, d)
		case "history":
			return s.contentHistory(r, a, d)
		default:
			return nil, 0, missing
		}
	})
	s.internalRoutes(mux)
	s.externalInternalRoutes(mux)
	s.cardsRoutes(mux)
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, openAPI()) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/metrics" {
			_, pattern := mux.Handler(r)
			started := time.Now()
			measured := &measuredResponse{ResponseWriter: w}
			w = measured
			defer func() {
				status := measured.status
				if status == 0 {
					status = http.StatusOK
				}
				metrics.observeRequest(r.Method, pattern, status, time.Since(started).Seconds())
			}()
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, testBodyLimit)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		defer func() {
			if recover() != nil {
				fail(w, errors.New("panic"))
			}
		}()
		// Reject import aliases before ServeMux can clean or redirect a path.
		if hasImportCarrier(r.Header) || len(r.Header.Values("X-CPM-Import-Prepare-Key")) > 0 {
			wire, e := readImportWire(r)
			if e != nil {
				fail(w, e)
				return
			}
			if hasImportCarrier(r.Header) && wire.Stage == "prepare" || !hasImportCarrier(r.Header) && wire.Stage != "prepare" {
				fail(w, conflict("operation_request_mismatch"))
				return
			}
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
