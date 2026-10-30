package main

import "testing"

// 刷新令牌接口必须匿名可达：此刻客户端手里的访问令牌已经过期，
// 网关若要求 token，刷新就永远换不出新令牌（前端会直接掉回登录页）。
func TestRefreshEndpointIsPublic(t *testing.T) {
	cases := []struct {
		path   string
		public bool
	}{
		{"/api/v1/user/auth/login", true},
		{"/api/v1/user/auth/refresh", true},
		{"/api/v1/user/auth/register", true},
		{"/api/v1/user/auth/me", false},
		{"/api/v1/user/auth/logout", false},
		{"/api/v1/chat/groups", false},
	}

	for _, testCase := range cases {
		if got := isPublicPath(testCase.path, nil); got != testCase.public {
			t.Errorf("isPublicPath(%q) = %v, want %v", testCase.path, got, testCase.public)
		}
	}
}
