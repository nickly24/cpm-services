package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type AuthClient struct {
	URL, Key                                                   string
	HTTP                                                       *http.Client
	OperationOwnerKey, OperationDeliveryKey, CardsOperationKey string
}

func NewAuthClient(c Config) *AuthClient {
	return &AuthClient{URL: c.AuthURL, Key: c.InternalKey, HTTP: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, OperationOwnerKey: c.OperationOwnerKey, OperationDeliveryKey: c.OperationDeliveryKey, CardsOperationKey: c.CardsOperationKey}
}
func (a *AuthClient) Identify(r *http.Request) (Actor, error) {
	if operationCarrier(r) {
		return a.operationIdentity(r)
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", a.URL+"/v1/auth/introspect", nil)
	if err != nil {
		return Actor{}, unavailable
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	res, err := a.HTTP.Do(req)
	if err != nil {
		return Actor{}, unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	if res.StatusCode != 200 {
		return Actor{}, unavailable
	}
	var identity Identity
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&identity)
	if err != nil || !identity.Status || !identity.Active || identity.User.AccountID < 1 || identity.User.OrganizationID < 1 {
		return Actor{}, unavailable
	}
	// Even a malformed auth response cannot promote an ordinary role to admin.
	identity.User.OrganizationAdmin = identity.User.Role == "admin" && identity.User.OrganizationAdmin
	return identity.User, nil
}
func (a *AuthClient) Hash(ctx context.Context, password string) (string, error) {
	body, _ := json.Marshal(struct {
		Password string `json:"password"`
	}{password})
	req, err := http.NewRequestWithContext(ctx, "POST", a.URL+"/internal/auth/password-hash", bytes.NewReader(body))
	if err != nil {
		return "", unavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", a.Key)
	res, err := a.HTTP.Do(req)
	if err != nil {
		return "", unavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", unavailable
	}
	var dto struct {
		Hash string `json:"hash"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&dto) != nil || !strings.HasPrefix(dto.Hash, "pbkdf2:sha256:600000$") {
		return "", unavailable
	}
	return dto.Hash, nil
}
func (a Actor) Can(section string, edit bool) bool {
	if a.OrganizationAdmin {
		return true
	}
	if a.Role != "staff_admin" {
		return false
	}
	p := a.Permissions[section]
	return p.View && (!edit || p.Edit)
}
func (a Actor) OwnGroup(groupID *int64) bool {
	return a.Role == "proctor" && a.GroupID != nil && groupID != nil && *a.GroupID == *groupID
}
func sameID(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
