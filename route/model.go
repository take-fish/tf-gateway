package route

import (
	"strings"
	"time"
)

// Config 是 tf-gateway.yaml 的顶层结构。
type Config struct {
	Version int           `yaml:"version"`
	Gateway GatewayConfig `yaml:"gateway"`
}

// GatewayConfig 描述服务发现、代理、重试与路由。
type GatewayConfig struct {
	Discovery DiscoveryConfig `yaml:"discovery"`
	HTTP      HTTPConfig      `yaml:"http"`
	Retry     RetryConfig     `yaml:"retry"`
	Routes    []Route         `yaml:"routes"`
}

// DiscoveryConfig 控制 Nacos 实例发现行为。
type DiscoveryConfig struct {
	Enabled               *bool    `yaml:"enabled"`
	Group                 string   `yaml:"group"`
	Clusters              []string `yaml:"clusters"`
	CacheTTLSeconds       int      `yaml:"cache-ttl-seconds"`
	RefreshIntervalSecond int      `yaml:"refresh-interval-seconds"`
	FallbackEnabled       *bool    `yaml:"fallback-enabled"`
}

// HTTPConfig 是上游连接与响应超时，单位毫秒。
type HTTPConfig struct {
	ConnectTimeoutMS        int `yaml:"connect-timeout-ms"`
	ResponseHeaderTimeoutMS int `yaml:"response-header-timeout-ms"`
	IdleConnTimeoutMS       int `yaml:"idle-conn-timeout-ms"`
}

// RetryConfig 只对幂等方法生效，且仅重试连接建立失败。
type RetryConfig struct {
	Enabled     *bool    `yaml:"enabled"`
	MaxAttempts int      `yaml:"max-attempts"`
	Methods     []string `yaml:"methods"`
}

// Route 是一条访问路径到 Nacos 服务名的映射。
// Path 与 Prefix 二选一；WebSocket 路由必须使用 Path。
type Route struct {
	ID        string `yaml:"id"`
	Prefix    string `yaml:"prefix"`
	Path      string `yaml:"path"`
	Service   string `yaml:"service"`
	Websocket bool   `yaml:"websocket"`
}

// Snapshot 是一次成功校验后的不可变路由快照。
type Snapshot struct {
	ConfigVersion int
	Checksum      string
	LoadedAt      time.Time
	Gateway       GatewayConfig
	Routes        []Route

	exact    map[string]Route
	prefixes []Route
	services []string
}

// Match 按精确路径优先、最长前缀其次的顺序匹配路由。
func (s *Snapshot) Match(requestPath string) (Route, bool) {
	if s == nil {
		return Route{}, false
	}
	if r, ok := s.exact[requestPath]; ok {
		return r, true
	}
	var best Route
	bestLen := -1
	for _, r := range s.prefixes {
		if requestPath == r.Prefix || strings.HasPrefix(requestPath, r.Prefix+"/") {
			if len(r.Prefix) > bestLen {
				best = r
				bestLen = len(r.Prefix)
			}
		}
	}
	return best, bestLen >= 0
}

// Services 返回快照中出现的服务名（去重，保持首次出现顺序）。
func (s *Snapshot) Services() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.services...)
}

// RouteVersion 是写入健康检查与日志的配置版本。
func (s *Snapshot) RouteVersion() string {
	if s == nil {
		return "none"
	}
	if s.Checksum == "" {
		return "default"
	}
	return "md5-" + s.Checksum
}

// Count 返回路由数量。
func (s *Snapshot) Count() int {
	if s == nil {
		return 0
	}
	return len(s.Routes)
}

// DiscoveryEnabled 返回实例发现总开关。
func (s *Snapshot) DiscoveryEnabled() bool {
	return s != nil && boolValue(s.Gateway.Discovery.Enabled, true)
}

// FallbackEnabled 返回无健康实例时是否允许静态回退。
func (s *Snapshot) FallbackEnabled() bool {
	return s != nil && boolValue(s.Gateway.Discovery.FallbackEnabled, true)
}

// RetryEnabled 返回是否启用连接失败重试。
func (s *Snapshot) RetryEnabled() bool {
	return s != nil && boolValue(s.Gateway.Retry.Enabled, true)
}

// HTTP 返回代理超时配置。
func (s *Snapshot) HTTP() HTTPConfig {
	if s == nil {
		return HTTPConfig{}
	}
	return s.Gateway.HTTP
}

// Retry 返回重试配置。
func (s *Snapshot) Retry() RetryConfig {
	if s == nil {
		return RetryConfig{}
	}
	return s.Gateway.Retry
}

func boolValue(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}
