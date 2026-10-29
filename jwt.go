package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// jwtVerifier 在网关本地验签 EdDSA（Ed25519）访问令牌，不再回调用户中台。
// 公钥由部署配置下发；支持多把 kid 并存，便于轮换。
type jwtVerifier struct {
	keys       map[string]ed25519.PublicKey
	defaultKid string
	issuer     string
	skew       int64
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss   string          `json:"iss"`
	Sub   json.RawMessage `json:"sub"`
	Usr   string          `json:"usr"`
	Nick  string          `json:"nick"`
	Role  string          `json:"role"`
	Roles json.RawMessage `json:"roles"`
	Perms json.RawMessage `json:"perms"`
	Iat   int64           `json:"iat"`
	Exp   int64           `json:"exp"`
	Nbf   int64           `json:"nbf"`
}

func newJWTVerifier(cfg Config) *jwtVerifier {
	if !cfg.AuthJWTEnabled {
		return &jwtVerifier{}
	}
	keys := make(map[string]ed25519.PublicKey, len(cfg.AuthJWTKeys)+1)
	if cfg.AuthJWTPublicKey != "" {
		if key, err := parseEd25519PublicKey(cfg.AuthJWTPublicKey); err == nil {
			keys[cfg.AuthJWTKeyID] = key
		}
	}
	for kid, value := range cfg.AuthJWTKeys {
		if key, err := parseEd25519PublicKey(value); err == nil {
			keys[kid] = key
		}
	}
	return &jwtVerifier{
		keys:       keys,
		defaultKid: cfg.AuthJWTKeyID,
		issuer:     cfg.AuthJWTIssuer,
		skew:       30,
	}
}

// Configured 表示至少配置了一把可用公钥。
func (v *jwtVerifier) Configured() bool {
	return v != nil && len(v.keys) > 0
}

// Verify 校验令牌并返回身份；任何异常都返回 errUnauthorized，不降级为匿名。
func (v *jwtVerifier) Verify(token string) (userIdentity, error) {
	if !v.Configured() {
		return userIdentity{}, errUnauthorized
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return userIdentity{}, errUnauthorized
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return userIdentity{}, errUnauthorized
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return userIdentity{}, errUnauthorized
	}
	if header.Alg != "EdDSA" {
		return userIdentity{}, errUnauthorized
	}
	kid := header.Kid
	if kid == "" {
		kid = v.defaultKid
	}
	key, ok := v.keys[kid]
	if !ok {
		return userIdentity{}, errUnauthorized
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return userIdentity{}, errUnauthorized
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return userIdentity{}, errUnauthorized
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return userIdentity{}, errUnauthorized
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return userIdentity{}, errUnauthorized
	}

	now := time.Now().Unix()
	if claims.Exp == 0 || claims.Exp+v.skew < now {
		return userIdentity{}, errUnauthorized
	}
	if claims.Nbf > 0 && claims.Nbf-v.skew > now {
		return userIdentity{}, errUnauthorized
	}
	if v.issuer != "" && claims.Iss != v.issuer {
		return userIdentity{}, errUnauthorized
	}

	id := parseSubject(claims.Sub)
	if id <= 0 {
		return userIdentity{}, errUnauthorized
	}

	roles := claimList(claims.Roles)
	permissions := claimList(claims.Perms)
	return userIdentity{
		ID:          id,
		Username:    claims.Usr,
		Nickname:    claims.Nick,
		Role:        claims.Role,
		Roles:       roles,
		Permissions: permissions,
	}, nil
}

// looksLikeJWT 粗判是否 JWT：恰好两段点分隔。
func looksLikeJWT(token string) bool {
	return strings.Count(token, ".") == 2
}

func parseSubject(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		var parsed int64
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

// claimList 兼容数组和逗号/空格分隔的字符串两种写法。
func claimList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err == nil {
		return items
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil
	}
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' '
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// parseEd25519PublicKey 解析 32 字节裸公钥（base64url 或 base64）。
func parseEd25519PublicKey(value string) (ed25519.PublicKey, error) {
	normalized := strings.NewReplacer("-", "+", "_", "/", "\n", "", " ", "", "\r", "").Replace(value)
	if pad := len(normalized) % 4; pad > 0 {
		normalized += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(normalized)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errUnauthorized
	}
	return ed25519.PublicKey(raw), nil
}
