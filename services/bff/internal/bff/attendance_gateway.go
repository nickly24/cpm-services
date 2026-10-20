package bff

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type attendanceIdentityKey struct{}

// Carry the current request's live Auth result, including grants for the journal
// division (which need not be the active cabinet). Keep unrelated grants out of
// headers. Oversized read scopes safely use ordinary bearer introspection.
func attendanceGatewayIdentity(r *http.Request) (json.RawMessage, bool) {
	raw, ok := r.Context().Value(attendanceIdentityKey{}).(json.RawMessage)
	if !ok {
		return nil, false
	}
	var identity struct {
		Status bool `json:"status"`
		Active bool `json:"active"`
		User   struct {
			AccountID         int64  `json:"account_id"`
			OrganizationID    int64  `json:"organization_id"`
			DivisionID        *int64 `json:"division_id"`
			Role              string `json:"role"`
			OrganizationAdmin bool   `json:"organization_admin"`
		} `json:"user"`
		Organizations []struct {
			ID        int64 `json:"id"`
			Divisions []struct {
				ID          int64  `json:"id"`
				GroupID     *int64 `json:"group_id"`
				Permissions map[string]struct {
					View bool `json:"view"`
					Edit bool `json:"edit"`
				} `json:"permissions"`
			} `json:"divisions"`
		} `json:"organizations"`
	}
	if json.Unmarshal(raw, &identity) != nil || !identity.Status || !identity.Active {
		return nil, false
	}
	chunks := strings.Split(r.URL.Path, "/")
	var division int64
	for i, chunk := range chunks {
		if chunk == "divisions" && i+1 < len(chunks) {
			division, _ = strconv.ParseInt(chunks[i+1], 10, 64)
			break
		}
	}
	organizations := identity.Organizations[:0]
	for _, org := range identity.Organizations {
		if org.ID != identity.User.OrganizationID {
			continue
		}
		divisions := org.Divisions[:0]
		for _, grant := range org.Divisions {
			if division == 0 || grant.ID == division {
				divisions = append(divisions, grant)
			}
		}
		org.Divisions = divisions
		organizations = append(organizations, org)
	}
	identity.Organizations = organizations
	result, err := json.Marshal(identity)
	return result, err == nil && len(result) <= 8192
}
