package attendance

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

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
func (s *Server) gatewayIdentity(r *http.Request) (Identity, bool, error) {
	encoded, signature := r.Header.Get("X-CPM-Gateway-Context"), r.Header.Get("X-CPM-Gateway-Signature")
	if len(r.Header.Values("X-CPM-Gateway-Context")) == 0 && len(r.Header.Values("X-CPM-Gateway-Signature")) == 0 {
		return Identity{}, false, nil
	}
	invalid := &APIError{401, "invalid_gateway_context"}
	if len(s.GatewayKey) < 32 || len(r.Header.Values("X-CPM-Gateway-Context")) != 1 || len(r.Header.Values("X-CPM-Gateway-Signature")) != 1 || len(encoded) > 16384 || len(signature) != 43 {
		return Identity{}, true, invalid
	}
	mac := hmac.New(sha256.New, []byte(s.GatewayKey))
	mac.Write([]byte(encoded))
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return Identity{}, true, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Identity{}, true, invalid
	}
	var claims gatewayClaims
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !gatewayUniqueJSON(raw) || decoder.Decode(&claims) != nil || decoder.Decode(new(any)) != io.EOF {
		return Identity{}, true, invalid
	}
	now := time.Now().UTC().Unix()
	if claims.Version != 1 || claims.Issuer != "cpm-bff" || claims.Audience != "attendance" || claims.IssuedAt < now-30 || claims.IssuedAt > now+2 || claims.ExpiresAt <= now || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > 30 || claims.Method != r.Method || claims.URI != r.URL.RequestURI() {
		return Identity{}, true, invalid
	}
	var identity Identity
	if decodeJSON(claims.User, &identity) != nil || !validGatewayIdentity(identity) {
		return Identity{}, true, invalid
	}
	var body []byte
	if r.Body != nil {
		// Restore the exact bounded body for the ordinary Attendance parser.
		body, err = io.ReadAll(io.LimitReader(r.Body, (3<<20)+1))
		r.Body.Close()
	}
	if err != nil || len(body) > 3<<20 {
		return Identity{}, true, invalid
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	hash := sha256.Sum256(body)
	if claims.BodyHash != hex.EncodeToString(hash[:]) {
		return Identity{}, true, invalid
	}
	return identity, true, nil
}

// Reject duplicate object keys at every level, including permissions and user.
// encoding/json otherwise silently accepts the last occurrence.
func gatewayUniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				name = strings.ToLower(name)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func validGatewayIdentity(a Identity) bool {
	if !a.Status || !a.Active || a.User.AccountID < 1 || a.User.OrganizationID < 1 || a.User.DivisionID != nil && *a.User.DivisionID < 1 || a.User.OrganizationAdmin && a.User.Role != "admin" || len(a.Organizations) != 1 || a.Organizations[0].ID != a.User.OrganizationID {
		return false
	}
	switch a.User.Role {
	case "admin", "staff_admin", "student", "proctor", "supervisor", "examinator":
	default:
		return false
	}
	seen := map[int64]bool{}
	for _, d := range a.Organizations[0].Divisions {
		if d.ID < 1 || seen[d.ID] || d.GroupID != nil && *d.GroupID < 1 {
			return false
		}
		seen[d.ID] = true
	}
	return true
}
