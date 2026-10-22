package route

import (
	"strings"
	"testing"
)

const validConfig = `
version: 1
gateway:
  discovery:
    enabled: true
    group: DEFAULT_GROUP
    clusters: [DEFAULT]
    cache-ttl-seconds: 10
    refresh-interval-seconds: 10
    fallback-enabled: true
  http:
    connect-timeout-ms: 2000
    response-header-timeout-ms: 30000
    idle-conn-timeout-ms: 90000
  retry:
    enabled: true
    max-attempts: 2
    methods: [GET, HEAD, OPTIONS]
  routes:
    - id: user
      prefix: /api/v1/user
      service: tf-user
    - id: chat-ws
      path: /ws/chat
      service: tf-chat
      websocket: true
`

func TestParseValidConfig(t *testing.T) {
	snapshot, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if snapshot.Count() != 2 {
		t.Fatalf("routes = %d, want 2", snapshot.Count())
	}
	if snapshot.RouteVersion() == "default" || len(snapshot.RouteVersion()) < 8 {
		t.Fatalf("unexpected route version: %s", snapshot.RouteVersion())
	}
	services := snapshot.Services()
	if len(services) != 2 || services[0] != "tf-user" || services[1] != "tf-chat" {
		t.Fatalf("services = %v", services)
	}
}

func TestParseDefaults(t *testing.T) {
	snapshot, err := Parse([]byte(`
version: 1
gateway:
  routes:
    - id: user
      prefix: /api/v1/user
      service: tf-user
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !snapshot.DiscoveryEnabled() || !snapshot.FallbackEnabled() || !snapshot.RetryEnabled() {
		t.Fatalf("defaults not applied: %+v", snapshot.Gateway)
	}
	if snapshot.Gateway.Discovery.Group != "DEFAULT_GROUP" {
		t.Fatalf("group = %q", snapshot.Gateway.Discovery.Group)
	}
	if snapshot.HTTP().ConnectTimeoutMS != 2000 || snapshot.Retry().MaxAttempts != 2 {
		t.Fatalf("http/retry defaults not applied: %+v %+v", snapshot.HTTP(), snapshot.Retry())
	}
}

func TestParseRejectsInvalidConfigs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown field",
			body: "version: 1\ngateway:\n  routez: []\n",
			want: "YAML 解析失败",
		},
		{
			name: "unsupported version",
			body: "version: 2\ngateway:\n  routes:\n    - id: user\n      prefix: /u\n      service: tf-user\n",
			want: "不支持的配置版本",
		},
		{
			name: "empty routes",
			body: "version: 1\ngateway: {}\n",
			want: "routes 不能为空",
		},
		{
			name: "duplicate id",
			body: "version: 1\ngateway:\n  routes:\n    - id: a\n      prefix: /a\n      service: tf-user\n    - id: a\n      prefix: /b\n      service: tf-chat\n",
			want: "id 重复",
		},
		{
			name: "path and prefix together",
			body: "version: 1\ngateway:\n  routes:\n    - id: a\n      path: /a\n      prefix: /a\n      service: tf-user\n",
			want: "必须二选一",
		},
		{
			name: "bad service",
			body: "version: 1\ngateway:\n  routes:\n    - id: a\n      prefix: /a\n      service: bad\n",
			want: "非法服务名",
		},
		{
			name: "websocket prefix",
			body: "version: 1\ngateway:\n  routes:\n    - id: a\n      prefix: /ws\n      service: tf-chat\n      websocket: true\n",
			want: "websocket 路由必须使用 path",
		},
		{
			name: "duplicate prefix",
			body: "version: 1\ngateway:\n  routes:\n    - id: a\n      prefix: /a\n      service: tf-user\n    - id: b\n      prefix: /a\n      service: tf-chat\n",
			want: "prefix 重复",
		},
		{
			name: "bad retry method",
			body: "version: 1\ngateway:\n  retry:\n    methods: [POST]\n  routes:\n    - id: a\n      prefix: /a\n      service: tf-user\n",
			want: "非法方法",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want contains %q", err, tt.want)
			}
		})
	}
}

func TestDefaultRoutes(t *testing.T) {
	snapshot := Default()
	if snapshot.Count() != 6 {
		t.Fatalf("routes = %d, want 6", snapshot.Count())
	}
	if snapshot.RouteVersion() != "default" {
		t.Fatalf("version = %q", snapshot.RouteVersion())
	}
	if _, ok := snapshot.Match("/ws/chat"); !ok {
		t.Fatal("/ws/chat should match")
	}
}
