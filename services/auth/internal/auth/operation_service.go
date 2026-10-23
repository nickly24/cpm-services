package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

type OperationGrant struct {
	ID, IssuanceKey, InputHash, Mode                              string
	ParentID                                                      *string
	AccountID, OrganizationID, DivisionID, ProfileID, AuthVersion int64
	Role                                                          string
	Binding                                                       OperationBinding
	IssuedAt, ExpiresAt                                           time.Time
	Revoked                                                       bool
}

type OperationStore interface {
	Issue(context.Context, OperationGrant) (OperationGrant, error)
	Get(context.Context, string) (OperationGrant, error)
	Revoke(context.Context, string) error
}
type OperationAccounts interface {
	LoadAccount(context.Context, int64) (*Account, error)
}

type OperationService struct {
	Store    OperationStore
	Accounts OperationAccounts
	TTL      time.Duration
	Now      func() time.Time
}

func (s *OperationService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (s *OperationService) ttl() time.Duration {
	if s.TTL == 0 {
		return 6 * time.Hour
	}
	return s.TTL
}

func operationCan(u UserDTO, section string) bool {
	if u.Role == "admin" && u.OrganizationAdmin {
		return true
	}
	if u.Role != "staff_admin" {
		return false
	}
	p, ok := u.Permissions[section]
	if !ok && section == "test-results" {
		p = u.Permissions["test_results"]
	}
	return p.View && p.Edit
}

func operationPermissions(a *Account, u UserDTO, b OperationBinding, stage string) error {
	t, ok := operationTypes[b.Type]
	if !ok || u.DivisionID == nil || !operationCan(u, "upload") || !operationCan(u, t.section) {
		return opError(403, "operation_access_denied")
	}
	if b.Type == "exams.questions" && stage != "receipt" {
		for _, id := range b.RequiredDivisionIDs {
			d, ok := operationDecimal(id)
			if !ok {
				return opError(503, "authorization_unavailable")
			}
			current, e := a.User(ContextDTO{OrganizationID: u.OrganizationID, DivisionID: &d})
			if e != nil || !operationCan(current, "exams") {
				return opError(403, "operation_access_denied")
			}
		}
	}
	return nil
}

func operationResponse(g OperationGrant, a *Account, u UserDTO) (OperationResponse, error) {
	contexts := []OperationDivisionPermissions{}
	for _, o := range a.Organizations {
		if o.ID != g.OrganizationID {
			continue
		}
		if len(o.Divisions) > 1000 {
			return OperationResponse{}, opError(403, "operation_access_denied")
		}
		for _, d := range o.Divisions {
			contexts = append(contexts, OperationDivisionPermissions{d.ID, d.Permissions})
		}
	}
	if u.DivisionID == nil {
		return OperationResponse{}, opError(403, "operation_access_denied")
	}
	return OperationResponse{Status: true, ContractVersion: 1, GrantID: g.ID, Mode: g.Mode, ExpiresAt: g.ExpiresAt.Format(time.RFC3339Nano),
		Actor:               OperationActor{ID: u.ID, AccountID: u.AccountID, Role: u.Role, OrganizationID: u.OrganizationID, DivisionID: *u.DivisionID, DivisionIDs: u.DivisionIDs, OrganizationAdmin: u.OrganizationAdmin, Permissions: u.Permissions},
		DivisionPermissions: contexts, Binding: g.Binding}, nil
}

func operationInputHash(mode, parent string, u UserDTO, b OperationBinding) string {
	raw, _ := json.Marshal(struct {
		Mode           string           `json:"mode"`
		Parent         string           `json:"parent"`
		AccountID      int64            `json:"account_id"`
		OrganizationID int64            `json:"organization_id"`
		DivisionID     *int64           `json:"division_id"`
		Binding        OperationBinding `json:"binding"`
	}{mode, parent, u.AccountID, u.OrganizationID, u.DivisionID, b})
	return operationDigest(raw)
}

func (s *OperationService) Issue(ctx context.Context, a *Account, u UserDTO, key string, b OperationBinding) (OperationResponse, error) {
	if a == nil || u.AccountID != a.UserID || u.ID != a.EntityID || u.Role != a.Role || u.DivisionID == nil || !operationUUID.MatchString(key) {
		return OperationResponse{}, opError(400, "invalid_operation_request")
	}
	// A full organization administrator may explicitly target another active
	// division of the same live organization for new user imports. No browser
	// organization or service credential can provide that authority.
	if b.Type == "users.credentials" && len(b.RequiredDivisionIDs) == 1 {
		if target, ok := operationDecimal(b.RequiredDivisionIDs[0]); ok {
			var e error
			u, e = operationUserImportTarget(a, u, target)
			if e != nil {
				return OperationResponse{}, e
			}
		}
	}
	if e := b.validate(*u.DivisionID); e != nil {
		return OperationResponse{}, e
	}
	if e := operationPermissions(a, u, b, "apply"); e != nil {
		return OperationResponse{}, e
	}
	now := s.now()
	expires := now.Add(s.ttl())
	uploadDeadline, _ := operationUTC(b.UploadExpiresAt)
	if uploadDeadline.Before(expires) {
		expires = uploadDeadline
	}
	if b.OwnerPreviewExpiresAt != nil {
		deadline, _ := operationUTC(*b.OwnerPreviewExpiresAt)
		if deadline.Before(expires) {
			expires = deadline
		}
	}
	// MySQL DATETIME(6) otherwise rounds finer source deadlines up. Never
	// return an authorization expiry later than its immutable owner deadline.
	expires = expires.UTC().Truncate(time.Microsecond)
	id, e := RandomToken(16)
	if e != nil {
		return OperationResponse{}, opError(503, "authorization_unavailable")
	}
	g := OperationGrant{ID: id, IssuanceKey: key, InputHash: operationInputHash("apply", "", u, b), Mode: "apply", AccountID: u.AccountID, OrganizationID: u.OrganizationID, DivisionID: *u.DivisionID, ProfileID: u.ID, AuthVersion: a.AuthVersion, Role: a.Role, Binding: b, IssuedAt: now, ExpiresAt: expires}
	// The store returns the original record on issuance replay, including expiry.
	// A new already-expired candidate is never inserted; a matching old record
	// remains readable for identity/recovery, without authorizing application.
	g, e = s.Store.Issue(ctx, g)
	if e != nil {
		return OperationResponse{}, e
	}
	if g.AccountID != a.UserID || g.OrganizationID != u.OrganizationID || g.DivisionID != *u.DivisionID {
		return OperationResponse{}, opError(409, "operation_issuance_conflict")
	}
	return operationResponse(g, a, u)
}

func (s *OperationService) current(ctx context.Context, g OperationGrant, epoch bool) (*Account, UserDTO, error) {
	a, e := s.Accounts.LoadAccount(ctx, g.AccountID)
	if e != nil {
		if errors.Is(e, ErrCredentials) || errors.Is(e, ErrForbidden) {
			return nil, UserDTO{}, opError(403, "operation_access_denied")
		}
		return nil, UserDTO{}, opError(503, "authorization_unavailable")
	}
	if a == nil {
		return nil, UserDTO{}, opError(403, "operation_access_denied")
	}
	if epoch && (a.AuthVersion != g.AuthVersion || a.Role != g.Role || a.EntityID != g.ProfileID) {
		return nil, UserDTO{}, opError(410, "operation_revoked")
	}
	u, e := a.User(ContextDTO{OrganizationID: g.OrganizationID, DivisionID: &g.DivisionID})
	if e != nil || u.AccountID != g.AccountID || u.OrganizationID != g.OrganizationID || u.DivisionID == nil || *u.DivisionID != g.DivisionID {
		return nil, UserDTO{}, opError(403, "operation_access_denied")
	}
	return a, u, nil
}

func (s *OperationService) Authorize(ctx context.Context, id, owner string, r OperationAuthorizeRequest) (OperationResponse, error) {
	if !operationHex32.MatchString(id) {
		return OperationResponse{}, opError(404, "operation_not_found")
	}
	g, e := s.Store.Get(ctx, id)
	if e != nil {
		return OperationResponse{}, e
	}
	audience := g.Binding.Owner
	if r.Stage == "core-direction" && g.Binding.Type == "cards.xlsx" {
		audience = "core"
	}
	if owner != audience {
		return OperationResponse{}, opError(404, "operation_not_found")
	}
	if e = g.Binding.validate(g.DivisionID); e != nil {
		return OperationResponse{}, opError(503, "authorization_unavailable")
	}
	if g.Mode != "apply" && g.Mode != "receipt" {
		return OperationResponse{}, opError(503, "authorization_unavailable")
	}
	if g.Mode == "receipt" && r.Stage != "receipt" && !(r.Stage == "core-direction" && g.Binding.Type == "cards.xlsx") {
		return OperationResponse{}, opError(403, "operation_access_denied")
	}
	if g.Revoked {
		return OperationResponse{}, opError(410, "operation_revoked")
	}
	if !g.ExpiresAt.After(s.now()) {
		return OperationResponse{}, opError(410, "operation_expired")
	}
	if !g.Binding.requestMatches(g.DivisionID, r) {
		return OperationResponse{}, opError(409, "operation_request_mismatch")
	}
	a, u, e := s.current(ctx, g, true)
	if e != nil {
		return OperationResponse{}, e
	}
	if e = operationPermissions(a, u, g.Binding, r.Stage); e != nil {
		return OperationResponse{}, e
	}
	return operationResponse(g, a, u)
}

func (s *OperationService) Recover(ctx context.Context, oldID, key string, a *Account, u UserDTO) (OperationResponse, error) {
	if !operationHex32.MatchString(oldID) || !operationUUID.MatchString(key) {
		return OperationResponse{}, opError(400, "invalid_operation_request")
	}
	old, e := s.Store.Get(ctx, oldID)
	if e != nil {
		return OperationResponse{}, e
	}
	if old.Binding.Type == "users.credentials" {
		u, e = operationUserImportTarget(a, u, old.DivisionID)
		if e != nil {
			return OperationResponse{}, e
		}
	}
	if a == nil || u.AccountID != old.AccountID || a.UserID != old.AccountID || u.ID != a.EntityID || u.Role != a.Role || u.OrganizationID != old.OrganizationID || u.DivisionID == nil || *u.DivisionID != old.DivisionID {
		return OperationResponse{}, opError(404, "operation_not_found")
	}
	if e = old.Binding.validate(old.DivisionID); e != nil {
		return OperationResponse{}, opError(503, "authorization_unavailable")
	}
	if e = operationPermissions(a, u, old.Binding, "receipt"); e != nil {
		return OperationResponse{}, e
	}
	now := s.now()
	id, e := RandomToken(16)
	if e != nil {
		return OperationResponse{}, opError(503, "authorization_unavailable")
	}
	g := OperationGrant{ID: id, IssuanceKey: key, InputHash: operationInputHash("receipt", oldID, u, old.Binding), Mode: "receipt", ParentID: &oldID, AccountID: old.AccountID, OrganizationID: old.OrganizationID, DivisionID: old.DivisionID, ProfileID: u.ID, AuthVersion: a.AuthVersion, Role: a.Role, Binding: old.Binding, IssuedAt: now, ExpiresAt: now.Add(s.ttl())}
	g, e = s.Store.Issue(ctx, g)
	if e != nil {
		return OperationResponse{}, e
	}
	return operationResponse(g, a, u)
}

func (s *OperationService) Revoke(ctx context.Context, id string, a *Account, u UserDTO) error {
	if !operationHex32.MatchString(id) {
		return opError(404, "operation_not_found")
	}
	g, e := s.Store.Get(ctx, id)
	if e != nil {
		return e
	}
	if a == nil || a.UserID != g.AccountID || u.AccountID != g.AccountID || u.ID != a.EntityID || u.Role != a.Role || u.OrganizationID != g.OrganizationID || u.DivisionID == nil || *u.DivisionID != g.DivisionID {
		return opError(404, "operation_not_found")
	}
	if e = operationPermissions(a, u, g.Binding, "receipt"); e != nil {
		return e
	}
	return s.Store.Revoke(ctx, id)
}

func operationScopeHash(g OperationGrant) string {
	return strconv.FormatInt(g.AccountID, 10) + ":" + g.IssuanceKey
}

func operationUserImportTarget(a *Account, u UserDTO, target int64) (UserDTO, error) {
	if a == nil || u.AccountID != a.UserID || u.ID != a.EntityID || u.Role != a.Role {
		return u, opError(403, "operation_access_denied")
	}
	if u.DivisionID != nil && *u.DivisionID == target {
		return u, nil
	}
	if u.Role != "admin" || !u.OrganizationAdmin {
		return u, opError(403, "operation_access_denied")
	}
	v, e := a.User(ContextDTO{OrganizationID: u.OrganizationID, DivisionID: &target})
	if e != nil || !v.OrganizationAdmin || v.OrganizationID != u.OrganizationID {
		return u, opError(403, "operation_access_denied")
	}
	return v, nil
}
