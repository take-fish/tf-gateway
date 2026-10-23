package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	pathpkg "path"
	"strings"
	"sync"
	"time"

	"github.com/take-fish/tf-gateway/resolver"
)

var (
	errUnauthorized    = errors.New("unauthorized")
	errAuthUnavailable = errors.New("auth unavailable")
)

const identityTTLSeconds = 120

type userIdentity struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Nickname    string   `json:"nickname"`
	Role        string   `json:"role"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// Authenticator 抽象用户中台 introspection，便于测试替换。
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (userIdentity, error)
}

type authCacheEntry struct {
	identity userIdentity
	expires  time.Time
}

// gatewayAuth 用用户中台确认 token，并把身份快照短期缓存在内存。
// 缓存只保存成功结果；用户停用后最迟一个 TTL 生效。
type gatewayAuth struct {
	resolver *resolver.Resolver
	service  string
	client   *http.Client
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]authCacheEntry
}

func newGatewayAuth(cfg Config, r *resolver.Resolver) *gatewayAuth {
	return &gatewayAuth{
		resolver: r,
		service:  cfg.AuthUserService,
		ttl:      cfg.AuthCacheTTL,
		client: &http.Client{
			Timeout: cfg.AuthReadTimeout,
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: cfg.AuthConnectTimeout}).DialContext,
				MaxIdleConns:          20,
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       60 * time.Second,
				ResponseHeaderTimeout: cfg.AuthReadTimeout,
			},
		},
		cache: make(map[string]authCacheEntry),
	}
}

func (a *gatewayAuth) Authenticate(ctx context.Context, token string) (userIdentity, error) {
	if token == "" {
		return userIdentity{}, errUnauthorized
	}
	key := sha256Hex(token)
	if identity, ok := a.cached(key); ok {
		return identity, nil
	}

	result, err := a.resolver.Resolve(a.service)
	if err != nil {
		return userIdentity{}, errAuthUnavailable
	}
	target := *result.Target
	target.Path = strings.TrimRight(target.Path, "/") + "/api/v1/user/auth/me"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return userIdentity{}, errAuthUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	trace := traceContextFrom(ctx)
	if trace.TraceID != "" {
		request.Header.Set("traceparent", trace.traceparent())
		request.Header.Set("X-Request-ID", trace.TraceID)
	}

	response, err := a.client.Do(request)
	if err != nil {
		return userIdentity{}, errAuthUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))

	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return userIdentity{}, errUnauthorized
	case response.StatusCode/100 != 2:
		return userIdentity{}, errAuthUnavailable
	}

	var identity userIdentity
	if err := json.Unmarshal(body, &identity); err != nil || identity.ID <= 0 {
		return userIdentity{}, errAuthUnavailable
	}
	if identity.Roles == nil {
		identity.Roles = []string{}
	}
	if identity.Permissions == nil {
		identity.Permissions = []string{}
	}
	a.remember(key, identity)
	return identity, nil
}

func (a *gatewayAuth) cached(key string) (userIdentity, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.cache[key]
	if !ok {
		return userIdentity{}, false
	}
	if time.Now().After(entry.expires) {
		delete(a.cache, key)
		return userIdentity{}, false
	}
	return entry.identity, true
}

func (a *gatewayAuth) remember(key string, identity userIdentity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, entry := range a.cache {
		if now.After(entry.expires) {
			delete(a.cache, k)
		}
	}
	if len(a.cache) >= 10_000 {
		a.cache = make(map[string]authCacheEntry)
	}
	a.cache[key] = authCacheEntry{identity: identity, expires: now.Add(a.ttl)}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// signIdentity 生成与 tf-common IdentitySigner 完全一致的规范串和签名。
func signIdentity(secret string, identity userIdentity, expiresAt int64) (map[string]string, error) {
	if secret == "" {
		return nil, fmt.Errorf("FM_IDENTITY_SECRET 未配置")
	}
	roles := strings.Join(identity.Roles, ",")
	permissions := strings.Join(identity.Permissions, ",")
	canonical := fmt.Sprintf("%d|%s|%s|%s|%s|%s|%d",
		identity.ID, b64url(identity.Username), b64url(identity.Nickname), b64url(identity.Role),
		roles, permissions, expiresAt)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return map[string]string{
		"X-FM-User-Id":     fmt.Sprintf("%d", identity.ID),
		"X-FM-Username":    b64url(identity.Username),
		"X-FM-Nickname":    b64url(identity.Nickname),
		"X-FM-Role":        b64url(identity.Role),
		"X-FM-Roles":       roles,
		"X-FM-Permissions": permissions,
		"X-FM-Exp":         fmt.Sprintf("%d", expiresAt),
		"X-FM-Signature":   base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}, nil
}

func b64url(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

var identityHeaderNames = []string{
	"X-FM-User-Id",
	"X-FM-Username",
	"X-FM-Nickname",
	"X-FM-Role",
	"X-FM-Roles",
	"X-FM-Permissions",
	"X-FM-Exp",
	"X-FM-Signature",
}

// stripInboundIdentity 阻止客户端伪造服务间身份头。
func stripInboundIdentity(header http.Header) {
	for _, name := range identityHeaderNames {
		header.Del(name)
	}
}

func isPublicPath(requestPath string, extra []string) bool {
	for _, pattern := range append(DefaultAuthPublicPaths, extra...) {
		if matchPath(pattern, requestPath) {
			return true
		}
	}
	return false
}

func matchPath(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return name == prefix || strings.HasPrefix(name, prefix+"/")
	}
	matched, err := pathpkg.Match(pattern, name)
	return err == nil && matched
}

func bearerToken(header string) string {
	value := strings.TrimSpace(header)
	if len(value) < 7 || !strings.EqualFold(value[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}
