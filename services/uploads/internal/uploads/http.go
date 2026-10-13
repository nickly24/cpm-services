package uploads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	var ae *APIError
	if errors.Is(e, sql.ErrNoRows) {
		ae = &APIError{404, "not_found"}
	} else if !errors.As(e, &ae) {
		ae = &APIError{500, "internal_error"}
	}
	reply(w, ae.Code, map[string]any{"status": false, "error": ae.Message})
}
func decode(r *http.Request, v any) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return api(415, "json_required")
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return api(400, "invalid_request")
	}
	return nil
}

type handler func(*http.Request, Actor, Scope) (any, int, error)

func (s *Server) route(m *http.ServeMux, pattern string, fn handler) {
	m.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		d, e := parseID(r.PathValue("division"))
		if e != nil {
			fail(w, e)
			return
		}
		a, sc, e := s.authorize(r.Context(), r.Header.Get("Authorization"), d)
		if e != nil {
			fail(w, e)
			return
		}
		out, code, e := fn(r, a, sc)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, code, map[string]any{"status": true, "data": out})
	})
}
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	p := "/v1/uploads/divisions/{division}"
	m.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"status": true}) })
	m.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), 3*time.Second)
		defer c()
		var n int
		if s.DB.PingContext(ctx) != nil || s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM uploads_jobs_v1").Scan(&n) != nil {
			fail(w, api(503, "database_unavailable"))
			return
		}
		if s.Broker == nil || !s.Broker.Ready() {
			fail(w, api(503, "rabbit_unavailable"))
			return
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", s.Config.AuthURL+"/health/ready", nil)
		res, e := s.HTTP.Do(req)
		if e != nil {
			fail(w, api(503, "auth_unavailable"))
			return
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			fail(w, api(503, "auth_unavailable"))
			return
		}
		reply(w, 200, map[string]any{"status": true, "owner_capabilities": "/v1/uploads/divisions/{division}/catalog"})
	})
	s.route(m, "GET "+p+"/catalog", func(r *http.Request, a Actor, sc Scope) (any, int, error) {
		out := []Type{}
		for _, t := range Catalog() {
			if fileAccess(a, t, false) == nil {
				if fileAccess(a, t, true) != nil {
					t.Create = false
					t.Edit = false
					t.Commit = false
					if t.ID == "users.legacy" {
						t.Template = false // Core protects its legacy template with edit rights.
					}
				}
				if t.Commit && (!s.Config.OperationsEnabled || t.ID == "cards.transform") {
					t.Commit = false
					if t.Dependency != "" {
						t.Dependency += "; "
					}
					t.Dependency += "background_authorization_pending: queue API available; owner apply disabled"
				}
				out = append(out, t)
			}
		}
		execution, authorization := "awaiting_authorization", "background_authorization_pending"
		if s.Config.OperationsEnabled {
			execution, authorization = "background", "operation_grant_v1"
		}
		return map[string]any{"scope": sc, "types": out, "delivery": "rabbit", "job_execution": execution, "job_authorization": authorization, "log_scope": "actor_in_selected_division"}, 200, nil
	})
	s.route(m, "POST "+p+"/sessions", s.create)
	s.route(m, "GET "+p+"/sessions", s.listSessions)
	s.route(m, "GET "+p+"/sessions/{session}", s.sessionGet)
	s.route(m, "PUT "+p+"/sessions/{session}", s.edit)
	s.route(m, "POST "+p+"/sessions/{session}/reconcile", s.reconcile)
	s.route(m, "POST "+p+"/sessions/{session}/commit", s.commit)
	s.route(m, "POST "+p+"/jobs/{job}/recover", s.recoverJob)
	s.route(m, "POST "+p+"/jobs/{job}/revoke", s.revokeJob)
	s.route(m, "GET "+p+"/jobs", s.listJobs)
	s.route(m, "GET "+p+"/jobs/{job}", s.jobGet)
	s.route(m, "GET "+p+"/jobs/{job}/report", func(r *http.Request, a Actor, sc Scope) (any, int, error) {
		r.SetPathValue("report", "report")
		return s.jobGet(r, a, sc)
	})
	s.route(m, "POST "+p+"/jobs/{job}/retry", s.retry)
	s.route(m, "POST "+p+"/files/register", s.register)
	s.route(m, "GET "+p+"/files/{file}", s.fileGet)
	m.HandleFunc("GET "+p+"/files/{file}/download", func(w http.ResponseWriter, r *http.Request) {
		d, e := parseID(r.PathValue("division"))
		if e != nil {
			fail(w, e)
			return
		}
		a, sc, e := s.authorize(r.Context(), r.Header.Get("Authorization"), d)
		if e == nil {
			e = s.download(w, r, a, sc)
		}
		if e != nil {
			fail(w, e)
		}
	})
	s.route(m, "GET "+p+"/receipts/{command}", s.getReceipt)
	s.route(m, "GET "+p+"/events", s.events)
	m.HandleFunc("GET "+p+"/templates/{type}", func(w http.ResponseWriter, r *http.Request) {
		d, e := parseID(r.PathValue("division"))
		if e != nil {
			fail(w, e)
			return
		}
		a, sc, e := s.authorize(r.Context(), r.Header.Get("Authorization"), d)
		if e == nil {
			e = s.template(w, r, a, sc)
		}
		if e != nil {
			fail(w, e)
		}
	})
	m.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, OpenAPI()) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, (8<<20)+(64<<10))
		ctx, c := context.WithTimeout(r.Context(), 110*time.Second)
		defer c()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *Server) getReceipt(r *http.Request, a Actor, sc Scope) (any, int, error) {
	var action, id, state string
	e := s.DB.QueryRowContext(r.Context(), "SELECT action,resource_id,state FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=?", sc.OrganizationID, sc.DivisionID, sc.ActorID, r.PathValue("command")).Scan(&action, &id, &state)
	if e != nil {
		return nil, 0, e
	}
	var kind string
	if strings.HasPrefix(action, "session.commit:") || strings.HasPrefix(action, "job.retry:") || strings.HasPrefix(action, "job.recover:") || strings.HasPrefix(action, "job.revoke:") {
		e = s.DB.QueryRowContext(r.Context(), "SELECT type FROM uploads_jobs_v1 WHERE id=?", id).Scan(&kind)
	} else if strings.HasPrefix(action, "file.") {
		e = s.DB.QueryRowContext(r.Context(), "SELECT type FROM uploads_files_v1 WHERE id=?", id).Scan(&kind)
	} else {
		e = s.DB.QueryRowContext(r.Context(), "SELECT type FROM uploads_sessions_v1 WHERE id=?", id).Scan(&kind)
	}
	if e != nil {
		return nil, 0, api(409, "command_outcome_unknown")
	}
	t, _ := findType(kind)
	if e = fileAccess(a, t, false); e != nil {
		return nil, 0, e
	}
	if strings.HasPrefix(action, "file.") {
		r.SetPathValue("file", id)
		if _, _, e = s.fileGet(r, a, sc); e != nil {
			return nil, 0, e
		}
	}
	return map[string]any{"command_id": r.PathValue("command"), "action": action, "resource_id": id, "state": state, "scope": sc, "type": kind, "owner": t.Owner}, 200, nil
}
func (s *Server) events(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if !a.Can("upload", false) {
		return nil, 0, api(403, "access_denied")
	}
	n, e := limit(r)
	if e != nil {
		return nil, 0, e
	}
	rows, e := s.DB.QueryContext(r.Context(), "SELECT id,type,owner,resource_id,action,revision,created_at FROM uploads_events_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? ORDER BY id DESC LIMIT ?", sc.OrganizationID, sc.DivisionID, sc.ActorID, n)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, rev int64
		var kind, owner, resource, action string
		var created time.Time
		if e = rows.Scan(&id, &kind, &owner, &resource, &action, &rev, &created); e != nil {
			return nil, 0, e
		}
		t, _ := findType(kind)
		if permission(a, t, false) == nil {
			out = append(out, map[string]any{"id": id, "scope": sc, "type": kind, "owner": owner, "resource_id": resource, "action": action, "revision": rev, "created_at": created})
		}
	}
	return out, 200, rows.Err()
}
