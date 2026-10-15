package cards

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type Dependencies struct {
	Config Config
	HTTP   *http.Client
}

func NewDependencies(c Config) *Dependencies {
	return &Dependencies{c, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *Dependencies) call(r *http.Request, method, path string, body any, out any, key string) error {
	var b bytes.Buffer
	if body != nil {
		if e := json.NewEncoder(&b).Encode(body); e != nil {
			return unavailable
		}
	}
	req, e := http.NewRequestWithContext(r.Context(), method, path, &b)
	if e != nil {
		return unavailable
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", d.Config.Key)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, e := d.HTTP.Do(req)
	if e != nil {
		return unavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		switch res.StatusCode {
		case 401:
			return &APIError{401, "invalid_token"}
		case 403:
			return denied
		case 404:
			return missing
		case 409:
			return conflict
		case 400, 413, 422:
			return bad("dependency_rejected_request")
		default:
			return unavailable
		}
	}
	if json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out) != nil {
		return unavailable
	}
	return nil
}
func (d *Dependencies) Identify(r *http.Request) (Actor, Scope, error) {
	var out struct {
		Status bool  `json:"status"`
		Active bool  `json:"active"`
		User   Actor `json:"user"`
	}
	if p := strings.Fields(r.Header.Get("Authorization")); len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		return Actor{}, Scope{}, &APIError{401, "invalid_token"}
	}
	e := d.call(r, "POST", d.Config.AuthURL+"/v1/auth/introspect", nil, &out, "")
	if e != nil {
		return Actor{}, Scope{}, e
	}
	a := out.User
	a.OrganizationAdmin = a.Role == "admin" && a.OrganizationAdmin
	if !out.Status || !out.Active || a.AccountID < 1 || a.OrganizationID < 1 {
		return a, Scope{}, unavailable
	}
	if a.Role != "student" && !a.Can(false) {
		return a, Scope{}, denied
	}
	var scope struct {
		Status bool  `json:"status"`
		Data   Scope `json:"data"`
	}
	e = d.call(r, "GET", d.Config.CoreURL+"/internal/core/cards-context", nil, &scope, "")
	if e == nil && (!scope.Status || scope.Data.OrganizationID != a.OrganizationID) {
		e = unavailable
	}
	return a, scope.Data, e
}
func (d *Dependencies) projection(r *http.Request, id string) (Projection, error) {
	var out struct {
		Status bool       `json:"status"`
		Data   Projection `json:"data"`
	}
	e := d.call(r, "GET", d.Config.TestsURL+"/internal/tests/cards/tests/"+id, nil, &out, "")
	if e == nil && (!out.Status || out.Data.TestID != id || out.Data.Version == "") {
		e = unavailable
	}
	return out.Data, e
}
func (d *Dependencies) catalog(r *http.Request) ([]Projection, error) {
	var out struct {
		Status bool         `json:"status"`
		Data   []Projection `json:"data"`
	}
	e := d.call(r, "GET", d.Config.TestsURL+"/internal/tests/cards/catalog?include_questions=true", nil, &out, "")
	if e == nil && !out.Status {
		e = unavailable
	}
	if e == nil {
		for _, p := range out.Data {
			if p.TestID == "" || p.Version == "" || !p.Visible || len(p.Questions) != p.CardsCount {
				return nil, unavailable
			}
		}
	}
	return out.Data, e
}
