package uploads

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string { return e.Message }
func api(c int, m string) error   { return &APIError{c, m} }
func data(raw []byte) json.RawMessage {
	var v struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &v) == nil && len(v.Data) > 0 {
		return v.Data
	}
	return raw
}
func (s *Server) call(ctx context.Context, method, base, path, token, key, ct string, body []byte) (json.RawMessage, error) {
	return s.callHeaders(ctx, method, base, path, token, key, ct, body, nil)
}
func (s *Server) callHeaders(ctx context.Context, method, base, path, token, key, ct string, body []byte, extra map[string]string) (json.RawMessage, error) {
	req, e := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if e != nil {
		return nil, api(503, "owner_unavailable")
	}
	req.Header.Set("Authorization", token)
	if base == s.Config.TestsURL && strings.HasPrefix(path, "/internal/tests/") && extra["X-CPM-Import-Prepare-Key"] == "" {
		req.Header.Set("X-Service-Key", s.Config.InternalKey)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for name, value := range extra {
		req.Header.Set(name, value)
	}
	res, e := s.HTTP.Do(req)
	if e != nil {
		return nil, api(503, "owner_outcome_unknown")
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if e != nil || len(raw) > 16<<20 {
		return nil, api(503, "owner_outcome_unknown")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := "owner_rejected"
		switch res.StatusCode {
		case 401:
			msg = "reauthorization_required"
		case 403:
			msg = "owner_access_denied"
		case 404:
			msg = "owner_not_found"
		case 409:
			msg = "owner_revision_conflict"
		case 410:
			msg = "owner_expired"
		case 503, 502, 500, 504:
			msg = "owner_outcome_unknown"
		}
		code := res.StatusCode
		if code >= 500 {
			code = 503
		}
		return nil, api(code, msg)
	}
	if !json.Valid(raw) {
		return nil, api(503, "owner_outcome_unknown")
	}
	return data(raw), nil
}
func (s *Server) identify(ctx context.Context, token string) (Actor, error) {
	p := strings.Fields(token)
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		return Actor{}, api(401, "invalid_token")
	}
	raw, e := s.call(ctx, "POST", s.Config.AuthURL, "/v1/auth/introspect", token, "", "", nil)
	if e != nil {
		return Actor{}, e
	}
	var out struct {
		Status bool  `json:"status"`
		Active bool  `json:"active"`
		User   Actor `json:"user"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.Status || !out.Active || out.User.AccountID < 1 || out.User.OrganizationID < 1 {
		return Actor{}, api(401, "invalid_token")
	}
	out.User.OrganizationAdmin = out.User.Role == "admin" && out.User.OrganizationAdmin
	return out.User, nil
}
func (s *Server) authorize(ctx context.Context, token string, d int64) (Actor, Scope, error) {
	a, e := s.identify(ctx, token)
	sc := Scope{a.OrganizationID, d, a.AccountID}
	if e != nil {
		return a, sc, e
	}
	// Live core access is checked even for org admins; URL target is never trusted by itself.
	raw, e := s.call(ctx, "GET", s.Config.CoreURL, "/v1/core/divisions", token, "", "", nil)
	if e != nil {
		return a, sc, e
	}
	var rows []struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return a, sc, api(503, "scope_unavailable")
	}
	exists := false
	for _, r := range rows {
		if r.ID == d {
			exists = true
		}
	}
	if !exists || (a.DivisionID == nil || *a.DivisionID != d) {
		return a, sc, api(403, "scope_access_denied")
	}
	return a, sc, nil
}
func permission(a Actor, t Type, edit bool) error {
	if !a.Can("upload", edit) || !a.Can(t.Permission, edit) {
		return api(403, "upload_and_subject_permission_required")
	}
	if t.ID == "cards.transform" && !a.Can("tests", true) {
		return api(403, "tests_permission_required")
	}
	return nil
}
func parseID(v string) (int64, error) {
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 1 {
		return 0, api(400, "invalid_id")
	}
	return n, nil
}
func sameScope(a, b Scope) bool { return a == b }
func newHTTP() *http.Client {
	return &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
