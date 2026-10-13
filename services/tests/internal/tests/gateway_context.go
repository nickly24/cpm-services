package tests

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type gatewayContextKey struct{}
type gatewayClaims struct {
	Version   int             `json:"version"`
	Issuer    string          `json:"issuer"`
	Audience  string          `json:"audience"`
	IssuedAt  int64           `json:"issued_at"`
	ExpiresAt int64           `json:"expires_at"`
	Method    string          `json:"method"`
	URI       string          `json:"uri"`
	BodyHash  string          `json:"body_sha256"`
	User      json.RawMessage `json:"user"`
}

// An assertion is short-lived and bound to the exact method, URI and body. A
// malformed assertion never falls back to bearer Auth, so headers cannot change
// how the same request is authorised. Direct API callers still use introspect.
func (d *Dependencies) gatewayActor(r *http.Request) (Actor, bool, error) {
	encoded, signature := r.Header.Get("X-CPM-Gateway-Context"), r.Header.Get("X-CPM-Gateway-Signature")
	if len(r.Header.Values("X-CPM-Gateway-Context")) == 0 && len(r.Header.Values("X-CPM-Gateway-Signature")) == 0 {
		return Actor{}, false, nil
	}
	invalid := &APIError{401, "invalid_gateway_context"}
	if len(d.GatewayKey) < 32 || len(r.Header.Values("X-CPM-Gateway-Context")) != 1 || len(r.Header.Values("X-CPM-Gateway-Signature")) != 1 || len(encoded) > 16384 {
		return Actor{}, true, invalid
	}
	mac := hmac.New(sha256.New, []byte(d.GatewayKey))
	mac.Write([]byte(encoded))
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return Actor{}, true, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Actor{}, true, invalid
	}
	var claims gatewayClaims
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&claims) != nil || decoder.Decode(new(any)) != io.EOF {
		return Actor{}, true, invalid
	}
	now := time.Now().UTC().Unix()
	if claims.Version != 1 || claims.Issuer != "cpm-bff" || claims.Audience != "tests" || claims.IssuedAt > now+2 || claims.ExpiresAt <= now || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > 30 || claims.Method != r.Method || claims.URI != r.URL.RequestURI() {
		return Actor{}, true, invalid
	}
	var actor Actor
	if json.Unmarshal(claims.User, &actor) != nil || actor.AccountID < 1 || actor.OrganizationID < 1 || actor.DivisionID != nil && *actor.DivisionID < 1 {
		return Actor{}, true, invalid
	}
	switch actor.Role {
	case "admin", "staff_admin", "student", "proctor", "supervisor", "examinator":
	default:
		return Actor{}, true, invalid
	}
	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	}
	if err != nil || len(body) > 1<<20 {
		return Actor{}, true, invalid
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	hash := sha256.Sum256(body)
	if claims.BodyHash != hex.EncodeToString(hash[:]) {
		return Actor{}, true, invalid
	}
	actor.OrganizationAdmin = actor.Role == "admin" && actor.OrganizationAdmin
	*r = *r.WithContext(context.WithValue(r.Context(), gatewayContextKey{}, true))
	return actor, true, nil
}

// The old BFF preflight fenced individual attempts/submissions to the selected
// cabinet, including organisation admins. Keep that rule at the data owner.
func gatewaySelectedDivision(r *http.Request, actor Actor, division int64) error {
	if trusted, _ := r.Context().Value(gatewayContextKey{}).(bool); trusted && (actor.DivisionID == nil || *actor.DivisionID != division) {
		if !actor.InDivision(division) {
			return denied
		}
		return missing
	}
	return nil
}
