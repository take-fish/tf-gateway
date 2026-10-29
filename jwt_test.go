package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func newTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func encodeKey(pub ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub)
}

func signToken(t *testing.T, priv ed25519.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	signature := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func testConfig(pub ed25519.PublicKey) Config {
	return Config{
		AuthJWTEnabled:   true,
		AuthJWTPublicKey: encodeKey(pub),
		AuthJWTKeyID:     "k1",
		AuthJWTIssuer:    "fishmatrix",
	}
}

func validClaims() map[string]any {
	return map[string]any{
		"iss":   "fishmatrix",
		"sub":   7,
		"usr":   "tester",
		"nick":  "测试员",
		"role":  "USER",
		"roles": []string{"USER"},
		"perms": []string{"reader.public.upload"},
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
	}
}

func TestJWTVerifyAcceptsValidToken(t *testing.T) {
	pub, priv := newTestKey(t)
	verifier := newJWTVerifier(testConfig(pub))

	identity, err := verifier.Verify(signToken(t, priv, "k1", validClaims()))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if identity.ID != 7 || identity.Username != "tester" || identity.Nickname != "测试员" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if len(identity.Roles) != 1 || identity.Roles[0] != "USER" {
		t.Fatalf("unexpected roles: %+v", identity.Roles)
	}
	if len(identity.Permissions) != 1 || identity.Permissions[0] != "reader.public.upload" {
		t.Fatalf("unexpected permissions: %+v", identity.Permissions)
	}
}

func TestJWTRejectsExpiredToken(t *testing.T) {
	pub, priv := newTestKey(t)
	verifier := newJWTVerifier(testConfig(pub))
	claims := validClaims()
	claims["exp"] = time.Now().Add(-10 * time.Minute).Unix()

	if _, err := verifier.Verify(signToken(t, priv, "k1", claims)); err == nil {
		t.Fatal("expired token should be rejected")
	}
}

func TestJWTRejectsTamperedPayload(t *testing.T) {
	pub, priv := newTestKey(t)
	verifier := newJWTVerifier(testConfig(pub))
	token := signToken(t, priv, "k1", validClaims())
	parts := splitToken(t, token)
	forged := parts[0] + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":1,"exp":9999999999}`)) + "." + parts[2]

	if _, err := verifier.Verify(forged); err == nil {
		t.Fatal("tampered token should be rejected")
	}
}

func TestJWTRejectsUnsignedAlgorithm(t *testing.T) {
	pub, _ := newTestKey(t)
	verifier := newJWTVerifier(testConfig(pub))
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":7,"exp":9999999999}`))

	if _, err := verifier.Verify(header + "." + payload + "."); err == nil {
		t.Fatal("alg=none should be rejected")
	}
}

func TestJWTRejectsUnknownKeyID(t *testing.T) {
	pub, priv := newTestKey(t)
	verifier := newJWTVerifier(testConfig(pub))

	if _, err := verifier.Verify(signToken(t, priv, "unknown", validClaims())); err == nil {
		t.Fatal("unknown kid should be rejected")
	}
}

func TestJWTRejectsWrongIssuer(t *testing.T) {
	pub, priv := newTestKey(t)
	verifier := newJWTVerifier(testConfig(pub))
	claims := validClaims()
	claims["iss"] = "someone-else"

	if _, err := verifier.Verify(signToken(t, priv, "k1", claims)); err == nil {
		t.Fatal("wrong issuer should be rejected")
	}
}

func TestJWTRejectsMissingExpiry(t *testing.T) {
	pub, priv := newTestKey(t)
	verifier := newJWTVerifier(testConfig(pub))
	claims := validClaims()
	delete(claims, "exp")

	if _, err := verifier.Verify(signToken(t, priv, "k1", claims)); err == nil {
		t.Fatal("missing exp should be rejected")
	}
}

func TestJWTKeyRotationAcceptsBothKeys(t *testing.T) {
	oldPub, oldPriv := newTestKey(t)
	newPub, newPriv := newTestKey(t)
	cfg := testConfig(oldPub)
	cfg.AuthJWTKeys = map[string]string{"k2": encodeKey(newPub)}
	verifier := newJWTVerifier(cfg)

	if _, err := verifier.Verify(signToken(t, oldPriv, "k1", validClaims())); err != nil {
		t.Fatalf("old key rejected: %v", err)
	}
	if _, err := verifier.Verify(signToken(t, newPriv, "k2", validClaims())); err != nil {
		t.Fatalf("new key rejected: %v", err)
	}
}

func TestJWTUnconfiguredVerifier(t *testing.T) {
	verifier := newJWTVerifier(Config{AuthJWTEnabled: true})
	if verifier.Configured() {
		t.Fatal("verifier without keys should report unconfigured")
	}
	if _, err := verifier.Verify("a.b.c"); err == nil {
		t.Fatal("unconfigured verifier should reject")
	}
}

func TestLooksLikeJWT(t *testing.T) {
	if !looksLikeJWT("a.b.c") {
		t.Fatal("a.b.c should look like a jwt")
	}
	if looksLikeJWT("opaque-token") || looksLikeJWT("a.b.c.d") {
		t.Fatal("non-jwt strings should not match")
	}
}

func TestParseKeyMap(t *testing.T) {
	parsed := parseKeyMap(" k1:abc , k2:def ,bad, :empty")
	if len(parsed) != 2 || parsed["k1"] != "abc" || parsed["k2"] != "def" {
		t.Fatalf("unexpected key map: %+v", parsed)
	}
	if parseKeyMap("") != nil {
		t.Fatal("empty value should return nil")
	}
}

func splitToken(t *testing.T, token string) []string {
	t.Helper()
	parts := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape: %d parts", len(parts))
	}
	return parts
}
