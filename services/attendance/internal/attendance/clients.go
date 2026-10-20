package attendance

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

type Member struct {
	AccountID       int64   `json:"account_id"`
	ProfileID       int64   `json:"profile_id"`
	Role            string  `json:"role"`
	Name            string  `json:"full_name"`
	GroupID         *int64  `json:"group_id"`
	GroupName       *string `json:"group_name"`
	Class           *int    `json:"class"`
	SchoolID        *int64  `json:"school_id"`
	SchoolName      *string `json:"school_name"`
	SchoolShortName *string `json:"school_short_name"`
}
type Group struct {
	Name      string `json:"name"`
	ID        int64  `json:"id"`
	ProctorID *int64 `json:"proctor_account_id"`
}
type References struct {
	OrganizationID int64    `json:"organization_id"`
	DivisionID     int64    `json:"division_id"`
	Members        []Member `json:"members"`
	Groups         []Group  `json:"groups"`
}
type Peers interface {
	Identify(context.Context, string) (Identity, error)
	Division(context.Context, int64, int64) (string, error)
	Refs(context.Context, int64, int64, []int64, *int64) (References, error)
	StudentProfile(context.Context, int64, int64, int64) (Member, error)
	School(context.Context, string, int64) error
	Schools(context.Context, string) ([]SchoolReference, error)
	OwnSchool(context.Context, string) (*int64, error)
	Guard(context.Context, int64, int64, int64, string) error
}
type HTTPPeers struct {
	AuthURL, CoreURL, Key string
	HTTP                  *http.Client
}

func NewPeers(auth, core, key string) *HTTPPeers {
	return &HTTPPeers{auth, core, key, &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (p *HTTPPeers) call(ctx context.Context, method, url, token string, input, out any, internal bool) error {
	var b bytes.Buffer
	if input != nil {
		if json.NewEncoder(&b).Encode(input) != nil {
			return unavailable
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, url, &b)
	if e != nil {
		return unavailable
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	if internal {
		r.Header.Set("X-Service-Key", p.Key)
	}
	res, e := p.HTTP.Do(r)
	if e != nil {
		return unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		if internal {
			return unavailable
		}
		return &APIError{401, "invalid_token"}
	}
	if res.StatusCode == 404 {
		if internal {
			var failure struct {
				Status bool   `json:"status"`
				Error  string `json:"error"`
			}
			if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&failure) != nil || failure.Status || failure.Error != "not_found" {
				if strings.HasSuffix(url, "/internal/core/attendance/history-reservations") {
					return guardMissing
				}
				return unavailable
			}
		}
		return missing
	}
	if res.StatusCode != 200 {
		return unavailable
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out) != nil {
		return unavailable
	}
	return nil
}
func (p *HTTPPeers) Identify(ctx context.Context, token string) (Identity, error) {
	var a Identity
	if len(strings.Fields(token)) != 2 || !strings.EqualFold(strings.Fields(token)[0], "Bearer") {
		return a, &APIError{401, "invalid_token"}
	}
	e := p.call(ctx, "POST", p.AuthURL+"/v1/auth/introspect", token, nil, &a, false)
	if e != nil {
		return a, e
	}
	if !a.Status || !a.Active || a.User.AccountID < 1 || a.User.OrganizationID < 1 {
		return a, unavailable
	}
	found := false
	for _, o := range a.Organizations {
		if o.ID == a.User.OrganizationID {
			found = true
		}
	}
	if !found {
		return a, unavailable
	}
	return a, nil
}
func (p *HTTPPeers) Division(ctx context.Context, o, d int64) (string, error) {
	var out struct {
		Status bool `json:"status"`
		Data   struct {
			Organization struct {
				ID int64 `json:"id"`
			} `json:"organization"`
			Divisions []struct {
				ID     int64  `json:"id"`
				Name   string `json:"name"`
				Active bool   `json:"is_active"`
			} `json:"divisions"`
		} `json:"data"`
	}
	e := p.call(ctx, "POST", p.CoreURL+"/internal/core/test-references", "", map[string]any{"organization_id": o, "division_ids": []int64{d}}, &out, true)
	if e != nil {
		return "", e
	}
	if !out.Status || out.Data.Organization.ID != o || len(out.Data.Divisions) != 1 || out.Data.Divisions[0].ID != d {
		return "", unavailable
	}
	v := out.Data.Divisions[0]
	if !v.Active {
		return "", missing
	}
	return v.Name, nil
}
func (p *HTTPPeers) Refs(ctx context.Context, o, d int64, ids []int64, g *int64) (References, error) {
	var out struct {
		Status bool       `json:"status"`
		Data   References `json:"data"`
	}
	e := p.call(ctx, "POST", p.CoreURL+"/internal/core/homework-references", "", map[string]any{"organization_id": o, "division_id": d, "account_ids": ids, "group_id": g}, &out, true)
	if e != nil {
		return out.Data, e
	}
	if !out.Status || out.Data.OrganizationID != o || out.Data.DivisionID != d {
		return out.Data, unavailable
	}
	if len(ids) > 0 {
		for _, id := range ids {
			found := false
			for _, m := range out.Data.Members {
				if m.AccountID == id {
					found = true
				}
			}
			if !found {
				return out.Data, unavailable
			}
		}
	}
	return out.Data, nil
}
func (p *HTTPPeers) School(ctx context.Context, token string, id int64) error {
	var out struct {
		Status bool `json:"status"`
		Data   struct {
			ID     int64 `json:"id"`
			Active bool  `json:"is_active"`
		} `json:"data"`
	}
	if e := p.call(ctx, "GET", p.CoreURL+"/v1/core/schools/"+strconv.FormatInt(id, 10), token, nil, &out, false); e != nil {
		return e
	}
	if !out.Status {
		return unavailable
	}
	if out.Data.ID != id || !out.Data.Active {
		return missing
	}
	return nil
}
func (p *HTTPPeers) OwnSchool(ctx context.Context, token string) (*int64, error) {
	var out struct {
		Status bool `json:"status"`
		Data   struct {
			SchoolID *int64 `json:"school_id"`
		} `json:"data"`
	}
	if e := p.call(ctx, "GET", p.CoreURL+"/v1/core/me", token, nil, &out, false); e != nil {
		return nil, e
	}
	if !out.Status {
		return nil, unavailable
	}
	return out.Data.SchoolID, nil
}

// Required permanent guard: a positive response must mean core serialized with
// membership removal and durably prohibited removal, including if our commit fails.
func (p *HTTPPeers) Guard(ctx context.Context, o, d, u int64, k string) error {
	var out struct {
		Status bool `json:"status"`
		Data   struct {
			OrganizationID int64  `json:"organization_id"`
			DivisionID     int64  `json:"division_id"`
			AccountID      int64  `json:"account_id"`
			Permanent      bool   `json:"permanent"`
			ReservationID  string `json:"reservation_id"`
		} `json:"data"`
	}
	e := p.call(ctx, "POST", p.CoreURL+"/internal/core/attendance/history-reservations", "", map[string]any{"organization_id": o, "division_id": d, "account_id": u, "operation_key": k}, &out, true)
	if e != nil {
		return e
	}
	if !out.Status || !out.Data.Permanent || out.Data.ReservationID == "" || out.Data.OrganizationID != o || out.Data.DivisionID != d || out.Data.AccountID != u {
		return guardMissing
	}
	return nil
}

type SchoolReference struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"is_active"`
}

func (p *HTTPPeers) Schools(ctx context.Context, token string) ([]SchoolReference, error) {
	out := []SchoolReference{}
	for offset := 0; offset <= 1000; offset += 200 {
		var env struct {
			Status bool              `json:"status"`
			Data   []SchoolReference `json:"data"`
		}
		if e := p.call(ctx, "GET", p.CoreURL+"/v1/core/schools?limit=200&offset="+strconv.Itoa(offset), token, nil, &env, false); e != nil {
			return nil, e
		}
		if !env.Status {
			return nil, unavailable
		}
		for _, v := range env.Data {
			if v.Active {
				out = append(out, v)
			}
		}
		if len(env.Data) < 200 {
			return out, nil
		}
	}
	return nil, bad("reference_limit_exceeded")
}

// Resolve the public profile QR in the selected organization/division only.
func (p *HTTPPeers) StudentProfile(ctx context.Context, o, d, profile int64) (Member, error) {
	var out struct {
		Status bool       `json:"status"`
		Data   References `json:"data"`
	}
	e := p.call(ctx, "POST", p.CoreURL+"/internal/core/homework-references", "", map[string]any{"organization_id": o, "division_id": d, "profile_ids": []int64{profile}}, &out, true)
	if e != nil {
		if e == missing {
			return Member{}, &APIError{404, "student_not_in_division"}
		}
		return Member{}, e
	}
	if !out.Status || out.Data.OrganizationID != o || out.Data.DivisionID != d || len(out.Data.Members) != 1 {
		return Member{}, unavailable
	}
	m := out.Data.Members[0]
	if m.ProfileID != profile || m.AccountID < 1 || m.Role != "student" {
		return Member{}, unavailable
	}
	return m, nil
}
