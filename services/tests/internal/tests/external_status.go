package tests

import "net/http"

type ExternalDomainRequest struct {
	OrganizationID int64 `json:"organization_id"`
	DivisionID     int64 `json:"division_id"`
}

func (s *Server) externalStatus(r *http.Request) (any, int, error) {
	var in ExternalDomainRequest
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.OrganizationID < 1 || in.DivisionID < 1 {
		return nil, 0, bad("invalid_scope")
	}
	refs, e := s.Dependencies.References(r.Context(), in.OrganizationID, []int64{in.DivisionID}, nil)
	if e != nil {
		return nil, 0, e
	}
	if !refs.Divisions[0].Active {
		return nil, 0, missing
	}
	if e = externalDomainReady(r.Context(), s.Store); e != nil {
		return nil, 0, e
	}
	return makeExternalDomainStatus(in.OrganizationID, in.DivisionID), 200, nil
}
