package main

import (
	"os"
	"testing"

	"github.com/take-fish/tf-gateway/route"
)

// TestInteropJavaIssuedToken 用 tf-user（Java）真实签发的令牌验证网关验签逻辑，
// 覆盖跨语言的 base64url、声明命名、Ed25519 编码差异。
//
// 令牌由 D:\tf\compose\tools\JwtInterop.java 生成，未设置环境变量时跳过：
//
//	FM_INTEROP_TOKEN=<token> FM_INTEROP_PUBKEY=<publicKey> go test -run TestInteropJavaIssuedToken
func TestInteropJavaIssuedToken(t *testing.T) {
	token := os.Getenv("FM_INTEROP_TOKEN")
	publicKey := os.Getenv("FM_INTEROP_PUBKEY")
	if token == "" || publicKey == "" {
		t.Skip("FM_INTEROP_TOKEN / FM_INTEROP_PUBKEY 未设置，跳过跨语言互通验证")
	}

	verifier := newJWTVerifier(Config{
		AuthJWTEnabled:   true,
		AuthJWTPublicKey: publicKey,
		AuthJWTKeyID:     "k1",
		AuthJWTIssuer:    "fishmatrix",
	}, route.JWTConfig{})

	identity, err := verifier.Verify(token)
	if err != nil {
		t.Fatalf("java-issued token rejected by gateway verifier: %v", err)
	}
	if identity.ID != 7 || identity.Username != "tester" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if len(identity.Permissions) != 1 || identity.Permissions[0] != "reader.public.upload" {
		t.Fatalf("unexpected permissions: %+v", identity.Permissions)
	}
	t.Logf("java -> go 验签通过：id=%d user=%s perms=%v", identity.ID, identity.Username, identity.Permissions)
}
