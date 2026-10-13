package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/scrypt"
	"testing"
)

func TestWerkzeugPasswords(t *testing.T) {
	password := "synthetic-test-password"
	salt := "testSalt"
	hash := pbkdf2.Key([]byte(password), []byte(salt), 600000, 32, sha256.New)
	stored := "pbkdf2:sha256:600000$" + salt + "$" + hex.EncodeToString(hash)
	if !CheckPassword(stored, password) || CheckPassword(stored, "wrong") {
		t.Fatal("PBKDF2 compatibility failed")
	}
	hash, err := scrypt.Key([]byte(password), []byte(salt), 32768, 8, 1, 64)
	if err != nil {
		t.Fatal(err)
	}
	stored = "scrypt:32768:8:1$" + salt + "$" + hex.EncodeToString(hash)
	if !CheckPassword(stored, password) || CheckPassword(stored, "wrong") {
		t.Fatal("scrypt compatibility failed")
	}
	for _, stored := range []string{password, "", "pbkdf2:sha256:999999999$salt$00", "scrypt:999999999:8:1$salt$00", "pbkdf2:sha256:600000$$00", "broken$hash"} {
		if CheckPassword(stored, password) {
			t.Fatal("unsupported hash accepted")
		}
	}
}
