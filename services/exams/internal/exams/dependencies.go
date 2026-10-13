package exams

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type Dependencies struct {
	Config ServiceConfig
	HTTP   *http.Client
}
type ReferenceSet struct {
	OrganizationID int64 `json:"organization_id"`
	Directions     []struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Active bool   `json:"is_active"`
	} `json:"directions"`
	Divisions []struct {
		ID     int64 `json:"id"`
		Active bool  `json:"is_active"`
	} `json:"divisions"`
	Members []struct {
		AccountID   int64   `json:"account_id"`
		ProfileID   int64   `json:"profile_id"`
		Role        string  `json:"role"`
		FullName    string  `json:"full_name"`
		Active      bool    `json:"is_active"`
		Login       string  `json:"login"`
		DivisionIDs []int64 `json:"division_ids"`
	} `json:"members"`
}

func NewDependencies(c ServiceConfig) *Dependencies {
	return &Dependencies{c, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *Dependencies) call(r *http.Request, method, path string, input, out any) error {
	var b bytes.Buffer
	if input != nil {
		if json.NewEncoder(&b).Encode(input) != nil {
			return &APIError{503, "dependency_unavailable"}
		}
	}
	req, e := http.NewRequestWithContext(r.Context(), method, path, &b)
	if e != nil {
		return &APIError{503, "dependency_unavailable"}
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", d.Config.Key)
	res, e := d.HTTP.Do(req)
	if e != nil {
		return &APIError{503, "dependency_unavailable"}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		switch res.StatusCode {
		case 401:
			return &APIError{401, "invalid_token"}
		case 403:
			return Denied
		case 404:
			return Missing
		default:
			return &APIError{503, "dependency_unavailable"}
		}
	}
	if json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out) != nil {
		return &APIError{503, "dependency_unavailable"}
	}
	return nil
}
func (d *Dependencies) Identify(r *http.Request) (Actor, error) {
	var out struct {
		Status bool  `json:"status"`
		Active bool  `json:"active"`
		User   Actor `json:"user"`
	}
	h := strings.Fields(r.Header.Get("Authorization"))
	if len(h) != 2 || !strings.EqualFold(h[0], "Bearer") {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	e := d.call(r, "POST", d.Config.AuthURL+"/v1/auth/introspect", nil, &out)
	if e != nil {
		return Actor{}, e
	}
	a := out.User
	a.OrganizationAdmin = a.Role == "admin" && a.OrganizationAdmin
	if !out.Status || !out.Active || a.AccountID < 1 || a.OrganizationID < 1 {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	return a, nil
}
func (d *Dependencies) Fetch(r *http.Request, org int64, divs []int64, direction int64, ids []int64, logins []string) (ReferenceSet, error) {
	input := map[string]any{"organization_id": org, "division_ids": divs}
	if direction > 0 {
		input["direction_id"] = direction
	}
	if len(ids) > 0 {
		input["account_ids"] = ids
	}
	if len(logins) > 0 {
		input["logins"] = logins
	}
	var out struct {
		Status bool         `json:"status"`
		Data   ReferenceSet `json:"data"`
	}
	e := d.call(r, "POST", d.Config.CoreURL+"/internal/core/exam-references", input, &out)
	if e == nil && (!out.Status || out.Data.OrganizationID != org) {
		e = &APIError{503, "invalid_core_response"}
	}
	return out.Data, e
}
func (d *Dependencies) References(r *http.Request, org int64, divs []int64, direction int64, ids []int64) (map[int64]Member, error) {
	set, e := d.Fetch(r, org, divs, direction, ids, nil)
	if e != nil {
		return nil, e
	}
	out := map[int64]Member{}
	for _, v := range set.Members {
		out[v.AccountID] = Member{AccountID: v.AccountID, Role: v.Role, FullName: v.FullName, Active: v.Active, Login: v.Login, DivisionIDs: v.DivisionIDs}
	}
	return out, nil
}
func (d *Dependencies) Ready(r *http.Request) error {
	for _, u := range []string{d.Config.AuthURL, d.Config.CoreURL} {
		var out map[string]any
		if e := d.call(r, "GET", u+"/health/ready", nil, &out); e != nil {
			return e
		}
	}
	return nil
}
