package bff

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type gatewayIdentityKey struct{}

// This snapshot is created only after this request's successful Auth /me call.
// Browser headers are never copied into the trusted service context.
func withGatewayIdentity(ctx context.Context, user json.RawMessage) context.Context {
	return context.WithValue(ctx, gatewayIdentityKey{}, append(json.RawMessage(nil), user...))
}

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

func (s *Server) attachGatewayContext(r *http.Request, body []byte) error {
	key, audience := "", ""
	base := r.URL.Scheme + "://" + r.URL.Host
	if s.config.Tests != "" && base == s.config.Tests {
		key, audience = s.config.GatewayKey, "tests"
	} else if s.config.Homework != "" && base == s.config.Homework {
		key, audience = s.config.HomeworkGatewayKey, "homework"
	} else if s.config.Attendance != "" && base == s.config.Attendance {
		key, audience = s.config.AttendanceGatewayKey, "attendance"
	}
	if key == "" {
		return nil
	}
	user, ok := r.Context().Value(gatewayIdentityKey{}).(json.RawMessage)
	if audience == "attendance" {
		user, ok = attendanceGatewayIdentity(r)
		if !ok {
			return nil
		}
	}
	if !ok || !json.Valid(user) || len(user) > 8192 || len(key) < 32 {
		return errors.New("gateway context unavailable")
	}
	now := time.Now().UTC().Unix()
	hash := sha256.Sum256(body)
	claims := gatewayClaims{Version: 1, Issuer: "cpm-bff", Audience: audience, IssuedAt: now, ExpiresAt: now + 30, Method: r.Method, URI: r.URL.RequestURI(), BodyHash: hex.EncodeToString(hash[:]), User: user}
	raw, err := json.Marshal(claims)
	if err != nil {
		return errors.New("gateway context unavailable")
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(encoded))
	r.Header.Set("X-CPM-Gateway-Context", encoded)
	r.Header.Set("X-CPM-Gateway-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	return nil
}
