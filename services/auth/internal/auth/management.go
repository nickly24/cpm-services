package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"golang.org/x/crypto/pbkdf2"
	"net/http"
)

// Password hashing belongs to auth. Only the trusted core service can call it.
// No user token or forwarded identity headers can authorize this endpoint.
func (s *Server) hashPassword(w http.ResponseWriter, r *http.Request) {
	if len(s.Config.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.Config.InternalKey)) != 1 {
		failure(w, 401, "invalid_service_key")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if len(input.Password) < 8 || len(input.Password) > 1024 {
		failure(w, 400, "invalid_password")
		return
	}
	salt, err := RandomToken(16)
	if err != nil {
		failure(w, 500, "internal_error")
		return
	}
	hash := pbkdf2.Key([]byte(input.Password), []byte(salt), 600000, 32, sha256.New)
	respond(w, 200, struct {
		Hash string `json:"hash"`
	}{"pbkdf2:sha256:600000$" + salt + "$" + hex.EncodeToString(hash)})
}
