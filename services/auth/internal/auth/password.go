package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/scrypt"
	"strconv"
	"strings"
)

// Accept the Werkzeug hash formats used by the current backend, never plaintext.
func CheckPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || len(parts[1]) == 0 || len(parts[1]) > 128 {
		return false
	}
	expected, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	method := strings.Split(parts[0], ":")
	var got []byte
	switch {
	case len(method) == 3 && method[0] == "pbkdf2" && method[1] == "sha256" && len(expected) == 32:
		n, e := strconv.Atoi(method[2])
		if e != nil || n < 1 || n > 2000000 {
			return false
		}
		got = pbkdf2.Key([]byte(password), []byte(parts[1]), n, 32, sha256.New)
	case len(method) == 4 && method[0] == "scrypt" && len(expected) == 64:
		n, e1 := strconv.Atoi(method[1])
		r, e2 := strconv.Atoi(method[2])
		p, e3 := strconv.Atoi(method[3])
		if e1 != nil || e2 != nil || e3 != nil || n < 2 || n > 65536 || r < 1 || r > 8 || p < 1 || p > 4 {
			return false
		}
		got, err = scrypt.Key([]byte(password), []byte(parts[1]), n, r, p, 64)
		if err != nil {
			return false
		}
	default:
		return false
	}
	return subtle.ConstantTimeCompare(expected, got) == 1
}

func DummyPasswordCheck(password string) {
	// Same work as the existing dev hashes, so missing users don't skip password work.
	pbkdf2.Key([]byte(password), []byte("cpm-auth-missing-user"), 600000, 32, sha256.New)
}
