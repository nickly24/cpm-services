package attendance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type QRClaims struct {
	OrganizationID int64 `json:"organization_id"`
	DivisionID     int64 `json:"division_id"`
	DayID          int64 `json:"class_day_id"`
	AccountID      int64 `json:"account_id"`
	ExpiresAt      int64 `json:"expires_at"`
}

func (s *Server) signQR(c QRClaims) string {
	b, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(b)
	h := hmac.New(sha256.New, s.QRKey)
	h.Write([]byte("attendance-qr-v1." + payload))
	return "attendance-qr-v1." + payload + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Server) verifyQR(token string) (QRClaims, error) {
	var c QRClaims
	p := strings.Split(token, ".")
	if len(p) != 3 || p[0] != "attendance-qr-v1" {
		return c, bad("invalid_qr")
	}
	signature, e := base64.RawURLEncoding.DecodeString(p[2])
	if e != nil {
		return c, bad("invalid_qr")
	}
	h := hmac.New(sha256.New, s.QRKey)
	h.Write([]byte(p[0] + "." + p[1]))
	if !hmac.Equal(signature, h.Sum(nil)) {
		return c, bad("invalid_qr")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil || json.Unmarshal(b, &c) != nil || c.AccountID < 1 || c.DayID < 1 || c.DivisionID < 1 || c.OrganizationID < 1 || c.ExpiresAt <= time.Now().Unix() || c.ExpiresAt > time.Now().Add(5*time.Minute).Unix() {
		return c, bad("expired_or_invalid_qr")
	}
	return c, nil
}
