package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"github.com/golang-jwt/jwt/v5"
	"math/big"
	"os"
	"strconv"
	"time"
)

type Tokens struct {
	Key                     *rsa.PrivateKey
	KeyID, Issuer, Audience string
	TTL                     time.Duration
}

func LoadTokens(c Config) (*Tokens, error) {
	data, e := os.ReadFile(c.KeyFile)
	if e != nil {
		return nil, e
	}
	key, e := jwt.ParseRSAPrivateKeyFromPEM(data)
	if e != nil {
		return nil, e
	}
	if key.N.BitLen() < 2048 {
		return nil, ErrToken
	}
	raw, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		return nil, e
	}
	hash := sha256.Sum256(raw)
	return &Tokens{key, hex.EncodeToString(hash[:12]), c.Issuer, c.Audience, c.AccessTTL}, nil
}

func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func RefreshHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (t *Tokens) Issue(a Account, u UserDTO, s Session) (string, error) {
	now := time.Now().UTC()
	expiry := now.Add(t.TTL)
	if s.ExpiresAt.Before(expiry) {
		expiry = s.ExpiresAt
	}
	c := Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: t.Issuer, Subject: "account:" + strconv.FormatInt(a.UserID, 10), Audience: jwt.ClaimStrings{t.Audience}, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiry)}, UserDTO: u, SessionID: s.ID, ContextVersion: s.ContextVersion, AuthVersion: a.AuthVersion, SessionVersion: a.LegacySessionVersion}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	token.Header["kid"] = t.KeyID
	return token.SignedString(t.Key)
}

func (t *Tokens) Parse(raw string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Header["kid"] != t.KeyID {
			return nil, ErrToken
		}
		return &t.Key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(t.Issuer), jwt.WithAudience(t.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || claims.SessionID == "" || claims.ContextVersion < 1 || claims.AuthVersion < 1 || claims.AccountID < 1 || claims.OrganizationID < 1 || claims.Subject != "account:"+strconv.FormatInt(claims.AccountID, 10) {
		return nil, ErrToken
	}
	return claims, nil
}

func (t *Tokens) JWKS() any {
	exponent := big.NewInt(int64(t.Key.E)).Bytes()
	return map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": t.KeyID, "n": base64.RawURLEncoding.EncodeToString(t.Key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(exponent)}}}
}
