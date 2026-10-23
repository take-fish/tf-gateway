package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/take-fish/tf-gateway/resolver"
)

type fakeAuthenticator struct {
	identity userIdentity
	err      error
	calls    atomic.Int32
}

func (f *fakeAuthenticator) Authenticate(context.Context, string) (userIdentity, error) {
	f.calls.Add(1)
	return f.identity, f.err
}

func testAuthApp(static map[string]string, auth Authenticator) *App {
	app := NewApp(Config{
		Port:               "0",
		DiscoveryEnabled:   false,
		AuthEnabled:        true,
		AuthSecret:         "test-secret",
		AuthCacheTTL:       time.Minute,
		AuthUserService:    "tf-user",
		AuthConnectTimeout: time.Second,
		AuthReadTimeout:    time.Second,
		Static:             static,
	})
	app.auth = auth
	return app
}

func TestAuthRejectsMissingToken(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer upstream.Close()

	auth := &fakeAuthenticator{identity: sampleIdentity()}
	gateway := httptest.NewServer(newRouter(testAuthApp(map[string]string{"tf-chat": upstream.URL}, auth)))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/chat/groups")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("unauthenticated request must not reach upstream")
	}
	if auth.calls.Load() != 0 {
		t.Fatal("missing token must not trigger introspection")
	}
}

func TestAuthInjectsSignedIdentityAndStripsForgedHeaders(t *testing.T) {
	type captured struct {
		UserID      string
		Username    string
		Nickname    string
		Roles       string
		Permissions string
		ExpiresAt   string
		Signature   string
	}
	received := make(chan captured, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- captured{
			UserID:      r.Header.Get("X-FM-User-Id"),
			Username:    r.Header.Get("X-FM-Username"),
			Nickname:    r.Header.Get("X-FM-Nickname"),
			Roles:       r.Header.Get("X-FM-Roles"),
			Permissions: r.Header.Get("X-FM-Permissions"),
			ExpiresAt:   r.Header.Get("X-FM-Exp"),
			Signature:   r.Header.Get("X-FM-Signature"),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	auth := &fakeAuthenticator{identity: sampleIdentity()}
	gateway := httptest.NewServer(newRouter(testAuthApp(map[string]string{"tf-chat": upstream.URL}, auth)))
	defer gateway.Close()

	req, _ := http.NewRequest(http.MethodGet, gateway.URL+"/api/v1/chat/groups", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	req.Header.Set("X-FM-User-Id", "999")
	req.Header.Set("X-FM-Permissions", "library.private.read.any")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	got := <-received
	if got.UserID != "7" {
		t.Fatalf("user id = %q, want 7 (forged header must be stripped)", got.UserID)
	}
	if got.Roles != "USER,EDITOR" || got.Permissions != "reader.public.upload,music.private.manage" {
		t.Fatalf("roles/permissions not preserved: %+v", got)
	}
	if decodeB64(t, got.Nickname) != "小明" {
		t.Fatalf("nickname = %q", decodeB64(t, got.Nickname))
	}
	canonical := fmt.Sprintf("7|%s|%s|%s|USER,EDITOR|reader.public.upload,music.private.manage|%s",
		got.Username, got.Nickname, b64url("USER"), got.ExpiresAt)
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, _ = mac.Write([]byte(canonical))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if got.Signature != expected {
		t.Fatalf("signature mismatch: got %q want %q", got.Signature, expected)
	}
}

func TestAuthPublicLoginBypassesIntrospection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-FM-User-Id") != "" {
			t.Errorf("public request must not carry identity headers")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	auth := &fakeAuthenticator{err: errUnauthorized}
	gateway := httptest.NewServer(newRouter(testAuthApp(map[string]string{"tf-user": upstream.URL}, auth)))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL+"/api/v1/user/auth/login", "application/json",
		strings.NewReader(`{"username":"a","password":"b"}`))
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if auth.calls.Load() != 0 {
		t.Fatal("public path must not introspect")
	}
}

func TestAuthUnavailableReturns503(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	auth := &fakeAuthenticator{err: errAuthUnavailable}
	gateway := httptest.NewServer(newRouter(testAuthApp(map[string]string{"tf-chat": upstream.URL}, auth)))
	defer gateway.Close()

	req, _ := http.NewRequest(http.MethodGet, gateway.URL+"/api/v1/chat/groups", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body[:n]), "AUTH_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body[:n])
	}
}

func TestWebSocketRouteBypassesHTTPAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	auth := &fakeAuthenticator{err: errUnauthorized}
	gateway := httptest.NewServer(newRouter(testAuthApp(map[string]string{"tf-chat": upstream.URL}, auth)))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/ws/chat?ticket=one-time")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want websocket route forwarded without gateway auth", resp.StatusCode)
	}
}

func TestSignIdentityMatchesSharedCrossLanguageVector(t *testing.T) {
	// 与 tf-common IdentitySignerTest 共用同一向量，防止两端规范串漂移。
	headers, err := signIdentity("test-identity-secret", userIdentity{
		ID:          7,
		Username:    "小明",
		Nickname:    "小 明|昵称",
		Role:        "USER",
		Roles:       []string{"USER"},
		Permissions: []string{"reader.public.upload", "music.private.manage"},
	}, 2000000000)
	if err != nil {
		t.Fatalf("sign identity: %v", err)
	}
	if headers["X-FM-Signature"] != "bQuQX5sq2E1D8T4lGCmktRCtRvJgtcicp6i4iRfUHPY" {
		t.Fatalf("signature = %q", headers["X-FM-Signature"])
	}
}

func TestGatewayAuthCachesIntrospection(t *testing.T) {
	var calls atomic.Int32
	userService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/user/auth/me" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer good-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sampleIdentity())
	}))
	defer userService.Close()

	r := resolver.New(nil, resolver.StaticTargets(map[string]string{"tf-user": userService.URL}))
	auth := newGatewayAuth(Config{
		AuthUserService:    "tf-user",
		AuthCacheTTL:       time.Minute,
		AuthConnectTimeout: time.Second,
		AuthReadTimeout:    time.Second,
	}, r)

	first, err := auth.Authenticate(context.Background(), "good-token")
	if err != nil {
		t.Fatalf("first authenticate: %v", err)
	}
	second, err := auth.Authenticate(context.Background(), "good-token")
	if err != nil {
		t.Fatalf("second authenticate: %v", err)
	}
	if first.ID != 7 || second.ID != 7 {
		t.Fatalf("unexpected identities: %+v %+v", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("introspection calls = %d, want 1 (cache hit)", calls.Load())
	}
}

func TestGatewayAuthMapsUnauthorized(t *testing.T) {
	userService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer userService.Close()
	r := resolver.New(nil, resolver.StaticTargets(map[string]string{"tf-user": userService.URL}))
	auth := newGatewayAuth(Config{
		AuthUserService:    "tf-user",
		AuthCacheTTL:       time.Minute,
		AuthConnectTimeout: time.Second,
		AuthReadTimeout:    time.Second,
	}, r)

	if _, err := auth.Authenticate(context.Background(), "bad-token"); err != errUnauthorized {
		t.Fatalf("err = %v, want errUnauthorized", err)
	}
}

func sampleIdentity() userIdentity {
	return userIdentity{
		ID:          7,
		Username:    "小明",
		Nickname:    "小明",
		Role:        "USER",
		Roles:       []string{"USER", "EDITOR"},
		Permissions: []string{"reader.public.upload", "music.private.manage"},
	}
}

func decodeB64(t *testing.T, value string) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return string(decoded)
}
