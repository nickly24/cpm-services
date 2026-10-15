package homework

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type Dependencies struct {
	AuthURL, CoreURL, Key, GatewayKey string
	HTTP                              *http.Client
}

func NewDependencies(c Config) *Dependencies {
	return &Dependencies{AuthURL: c.AuthURL, CoreURL: c.CoreURL, Key: c.CoreKey, GatewayKey: c.GatewayKey, HTTP: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *Dependencies) Identify(r *http.Request) (Actor, error) {
	if actor, present, err := d.gatewayActor(r); present {
		return actor, err
	}
	p := strings.Fields(r.Header.Get("Authorization"))
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	req, _ := http.NewRequestWithContext(r.Context(), "POST", d.AuthURL+"/v1/auth/introspect", nil)
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	res, e := d.HTTP.Do(req)
	if e != nil {
		return Actor{}, unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	if res.StatusCode != 200 {
		return Actor{}, unavailable
	}
	var out struct {
		Status bool  `json:"status"`
		Active bool  `json:"active"`
		User   Actor `json:"user"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || !out.Status || !out.Active || out.User.AccountID < 1 || out.User.OrganizationID < 1 {
		return Actor{}, unavailable
	}
	out.User.OrganizationAdmin = out.User.Role == "admin" && out.User.OrganizationAdmin
	return out.User, nil
}
func (d *Dependencies) References(ctx context.Context, org, div int64) (References, error) {
	body, _ := json.Marshal(map[string]any{"organization_id": org, "division_id": div})
	req, _ := http.NewRequestWithContext(ctx, "POST", d.CoreURL+"/internal/core/homework-references", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", d.Key)
	res, e := d.HTTP.Do(req)
	if e != nil {
		return References{}, unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return References{}, missing
	}
	if res.StatusCode != 200 {
		return References{}, unavailable
	}
	var out struct {
		Status bool       `json:"status"`
		Data   References `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out) != nil || !out.Status || out.Data.OrganizationID != org || out.Data.DivisionID != div {
		return References{}, unavailable
	}
	return out.Data, nil
}
