package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type ExternalPerson struct {
	AccountID int64
	StudentID int64
	FullName  string
	Active    bool
}

// A small Core-owned projection provides current participation and the distinct
// account/profile identities. No Tests operation reads another service's DB.
func (s *Server) externalPeople(ctx context.Context, org, division int64) ([]ExternalPerson, error) {
	body, _ := json.Marshal(map[string]any{"organization_id": org, "division_ids": []int64{division}, "direction_id": nil, "include_students": true})
	req, e := http.NewRequestWithContext(ctx, "POST", s.Dependencies.CoreURL+"/internal/core/test-references", bytes.NewReader(body))
	if e != nil {
		return nil, unavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", s.Config.ServiceKey)
	res, e := s.Dependencies.HTTP.Do(req)
	if e != nil {
		return nil, unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, missing
	}
	if res.StatusCode != 200 {
		return nil, unavailable
	}
	var payload struct {
		Status bool `json:"status"`
		Data   struct {
			References
			AccountsComplete         bool `json:"accounts_complete"`
			AccountProjectionVersion int  `json:"account_projection_version"`
			Accounts                 []struct {
				AccountID int64  `json:"account_id"`
				ProfileID int64  `json:"profile_id"`
				FullName  string `json:"full_name"`
				Active    bool   `json:"is_active"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&payload) != nil || !payload.Status || payload.Data.Organization.ID != org || len(payload.Data.Divisions) != 1 || payload.Data.Divisions[0].ID != division || !payload.Data.Divisions[0].Active || !payload.Data.AccountsComplete || payload.Data.AccountProjectionVersion != 1 || len(payload.Data.Accounts) > 2000 {
		return nil, unavailable
	}
	out := []ExternalPerson{}
	seen := map[int64]bool{}
	for _, p := range payload.Data.Accounts {
		if p.AccountID < 1 || p.ProfileID < 1 || !validText(p.FullName, 500, true) || seen[p.AccountID] {
			return nil, unavailable
		}
		seen[p.AccountID] = true
		out = append(out, ExternalPerson{p.AccountID, p.ProfileID, p.FullName, p.Active})
	}
	return out, nil
}
