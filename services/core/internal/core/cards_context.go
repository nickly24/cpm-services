package core

import (
	"crypto/subtle"
	"net/http"
	"strconv"
)

// The organization direction catalog currently has no per-account direction
// assignments. Membership grants its active directions; no cross-org fallback.
func (s *Server) cardsContext(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{401, "invalid_service_key"})
		return
	}
	a, e := s.Auth.Identify(r)
	if e != nil {
		fail(w, e)
		return
	}
	if a.Role != "student" && !a.Can("cards", false) && !a.Can("training", false) && !a.Can("train", false) {
		fail(w, denied)
		return
	}
	rows, e := list(r.Context(), s.DB, "SELECT p.id,p.name FROM directions p JOIN core_direction_scope x ON x.direction_id=p.id WHERE x.organization_id=? AND x.is_active=1 ORDER BY p.id", a.OrganizationID)
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": map[string]any{"organization_id": a.OrganizationID, "directions": rows}})
}

// A Cards import gets only its pinned active direction. This route never
// accepts a user bearer and never falls back to the organization catalog.
func (s *Server) importDirection(w http.ResponseWriter, r *http.Request) {
	if !operationCarrier(r) {
		fail(w, &APIError{401, "invalid_operation_credential"})
		return
	}
	a, e := s.Auth.Identify(r)
	if e != nil {
		fail(w, e)
		return
	}
	ctx := requestOperation(r)
	if ctx == nil || ctx.Envelope == nil || ctx.Request.Stage != "core-direction" {
		fail(w, denied)
		return
	}
	direction, e := id(r, "direction")
	if e != nil {
		fail(w, e)
		return
	}
	var found int64
	e = s.DB.QueryRowContext(r.Context(), "SELECT p.id FROM directions p JOIN core_direction_scope x ON x.direction_id=p.id JOIN organizations o ON o.id=x.organization_id AND o.is_active=1 JOIN divisions d ON d.organization_id=o.id AND d.id=? AND d.is_active=1 WHERE p.id=? AND x.organization_id=? AND x.is_active=1", *a.DivisionID, direction, a.OrganizationID).Scan(&found)
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": map[string]any{"account_id": a.AccountID, "organization_id": a.OrganizationID, "division_id": *a.DivisionID, "job_id": ctx.Envelope.Binding.JobID, "owner_session_id": *ctx.Envelope.Binding.OwnerSessionID, "direction_id": strconv.FormatInt(found, 10), "active": true}})
}
