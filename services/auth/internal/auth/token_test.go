package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"github.com/golang-jwt/jwt/v5"
	"testing"
	"time"
)

func TestTokenValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokens := &Tokens{Key: key, KeyID: "test", Issuer: "test-issuer", Audience: "test-audience", TTL: time.Minute}
	id := int64(10)
	a := Account{UserID: 1, EntityID: 2, Role: "student", AuthVersion: 1}
	u := UserDTO{ID: 2, AccountID: 1, Role: "student", OrganizationID: 5, DivisionID: &id, DivisionIDs: []int64{10}}
	session := Session{ContextVersion: 7, ID: "session", ExpiresAt: time.Now().Add(time.Hour)}
	raw, err := tokens.Issue(a, u, session)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tokens.Parse(raw)
	if err != nil || c.ContextVersion != 7 || c.OrganizationID != 5 || c.UserDTO.ID != 2 {
		t.Fatal("valid token rejected", err)
	}
	for _, change := range []func(*Claims){
		func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute)) },
		func(c *Claims) { c.ExpiresAt = nil },
		func(c *Claims) { c.Issuer = "wrong" },
		func(c *Claims) { c.Audience = jwt.ClaimStrings{"wrong"} },
		func(c *Claims) { c.Subject = "account:999" },
		func(c *Claims) { c.ContextVersion = 0 },
		func(c *Claims) { c.ContextVersion = -1 },
	} {
		altered := *c
		change(&altered)
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, altered)
		token.Header["kid"] = "test"
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tokens.Parse(raw); err == nil {
			t.Fatal("invalid claims accepted")
		}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	token.Header["kid"] = "test"
	raw, _ = token.SignedString([]byte("wrong-key"))
	if _, err = tokens.Parse(raw); err == nil {
		t.Fatal("algorithm confusion accepted")
	}
}

func TestDivisionIsolation(t *testing.T) {
	id1, id2 := int64(11), int64(12)
	a := Account{Role: "staff_admin", Organizations: []Organization{{ID: 1, Divisions: []Division{{ID: id1, Permissions: map[string]Permission{"tests": {View: true}}}, {ID: id2, Permissions: map[string]Permission{"tests": {View: true, Edit: true}}}}}}}
	viewer, err := a.User(ContextDTO{1, &id1})
	if err != nil || viewer.Permissions["tests"].Edit {
		t.Fatal("viewer received editing rights")
	}
	editor, err := a.User(ContextDTO{1, &id2})
	if err != nil || !editor.Permissions["tests"].Edit {
		t.Fatal("editor lost rights")
	}
	if _, err = a.User(ContextDTO{2, &id1}); err == nil {
		t.Fatal("cross-organization access accepted")
	}
	if _, err = a.User(ContextDTO{1, nil}); err == nil {
		t.Fatal("delegated user got organization-wide context")
	}
}
