package tests

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
	AuthURL, CoreURL, Key string
	HTTP                  *http.Client
	GatewayKey            string
}
type Reference struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"is_active"`
}
type References struct {
	Organization Reference   `json:"organization"`
	Divisions    []Reference `json:"divisions"`
	Direction    *Reference  `json:"direction"`
	Directions   []Reference `json:"directions,omitempty"`
}

func NewDependencies(c Config) *Dependencies {
	return &Dependencies{AuthURL: c.AuthURL, CoreURL: c.CoreURL, Key: c.ServiceKey, GatewayKey: c.GatewayKey, HTTP: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *Dependencies) Identify(r *http.Request) (Actor, error) {
	if hasImportCarrier(r.Header) {
		return Actor{}, conflict("operation_request_mismatch")
	}
	if actor, present, err := d.gatewayActor(r); present {
		return actor, err
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	req, e := http.NewRequestWithContext(r.Context(), "POST", d.AuthURL+"/v1/auth/introspect", nil)
	if e != nil {
		return Actor{}, unavailable
	}
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
	var envelope struct {
		Status bool  `json:"status"`
		Active bool  `json:"active"`
		User   Actor `json:"user"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope) != nil || !envelope.Status || !envelope.Active || envelope.User.AccountID < 1 || envelope.User.OrganizationID < 1 {
		return Actor{}, unavailable
	}
	envelope.User.OrganizationAdmin = envelope.User.Role == "admin" && envelope.User.OrganizationAdmin
	return envelope.User, nil
}
func (d *Dependencies) References(ctx context.Context, org int64, divisions []int64, direction *int64) (References, error) {
	return d.references(ctx, org, divisions, direction, nil)
}

// ReferencesBatch keeps catalogue pages independent of the number of distinct
// directions. Core validates the organisation and returns IDs in request order.
func (d *Dependencies) ReferencesBatch(ctx context.Context, org int64, divisions, directions []int64) (References, error) {
	return d.references(ctx, org, divisions, nil, directions)
}

func (d *Dependencies) references(ctx context.Context, org int64, divisions []int64, direction *int64, directions []int64) (References, error) {
	in := struct {
		OrganizationID int64   `json:"organization_id"`
		DivisionIDs    []int64 `json:"division_ids"`
		DirectionID    *int64  `json:"direction_id"`
		DirectionIDs   []int64 `json:"direction_ids,omitempty"`
	}{org, divisions, direction, directions}
	body, _ := json.Marshal(in)
	req, e := http.NewRequestWithContext(ctx, "POST", d.CoreURL+"/internal/core/test-references", bytes.NewReader(body))
	if e != nil {
		return References{}, unavailable
	}
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
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || !out.Status || out.Data.Organization.ID != org || len(out.Data.Divisions) != len(divisions) {
		return References{}, unavailable
	}
	for i, id := range divisions {
		if out.Data.Divisions[i].ID != id {
			return References{}, unavailable
		}
	}
	if direction != nil && (out.Data.Direction == nil || out.Data.Direction.ID != *direction) {
		return References{}, unavailable
	}
	if len(out.Data.Directions) != len(directions) {
		return References{}, unavailable
	}
	for i, id := range directions {
		if out.Data.Directions[i].ID != id {
			return References{}, unavailable
		}
	}
	return out.Data, nil
}
func (d *Dependencies) Ready(ctx context.Context) error {
	for _, base := range []string{d.AuthURL, d.CoreURL} {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/health/ready", nil)
		res, e := d.HTTP.Do(req)
		if e != nil {
			return unavailable
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			return unavailable
		}
	}
	return nil
}
