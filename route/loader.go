package route

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const supportedVersion = 1

var servicePattern = regexp.MustCompile(`^tf-[a-z0-9-]+$`)

var allowedRetryMethods = map[string]bool{
	"GET":     true,
	"HEAD":    true,
	"OPTIONS": true,
}

// Parse 解析并校验 tf-gateway.yaml，成功时返回不可变快照。
func Parse(data []byte) (*Snapshot, error) {
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("配置为空")
		}
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return nil, err
	}
	sum := md5.Sum(data)
	return buildSnapshot(cfg, hex.EncodeToString(sum[:])), nil
}

// Default 返回内置兜底路由，用于 Nacos 不可用或配置无效时继续服务。
func Default() *Snapshot {
	cfg := DefaultConfig()
	return buildSnapshot(cfg, "")
}

// DefaultConfig 与当前网关的静态路由保持一致。
func DefaultConfig() Config {
	enabled := true
	fallback := true
	retry := true
	return Config{
		Version: supportedVersion,
		Gateway: GatewayConfig{
			Discovery: DiscoveryConfig{
				Enabled:               &enabled,
				Group:                 "DEFAULT_GROUP",
				Clusters:              []string{"DEFAULT"},
				CacheTTLSeconds:       10,
				RefreshIntervalSecond: 10,
				FallbackEnabled:       &fallback,
			},
			HTTP: HTTPConfig{
				ConnectTimeoutMS:        2000,
				ResponseHeaderTimeoutMS: 30000,
				IdleConnTimeoutMS:       90000,
			},
			Retry: RetryConfig{
				Enabled:     &retry,
				MaxAttempts: 2,
				Methods:     []string{"GET", "HEAD", "OPTIONS"},
			},
			Routes: []Route{
				{ID: "user", Prefix: "/api/v1/user", Service: "tf-user"},
				{ID: "chat", Prefix: "/api/v1/chat", Service: "tf-chat"},
				{ID: "chat-ws", Path: "/ws/chat", Service: "tf-chat", Websocket: true},
				{ID: "reader", Prefix: "/api/v1/reader", Service: "tf-reader"},
				{ID: "game", Prefix: "/api/v1/game", Service: "tf-game"},
				{ID: "music", Prefix: "/api/v1/music", Service: "tf-music"},
			},
		},
	}
}

func applyDefaults(cfg *Config) {
	g := &cfg.Gateway
	if g.Discovery.Enabled == nil {
		v := true
		g.Discovery.Enabled = &v
	}
	if g.Discovery.Group == "" {
		g.Discovery.Group = "DEFAULT_GROUP"
	}
	if len(g.Discovery.Clusters) == 0 {
		g.Discovery.Clusters = []string{"DEFAULT"}
	}
	if g.Discovery.CacheTTLSeconds == 0 {
		g.Discovery.CacheTTLSeconds = 10
	}
	if g.Discovery.RefreshIntervalSecond == 0 {
		g.Discovery.RefreshIntervalSecond = 10
	}
	if g.Discovery.FallbackEnabled == nil {
		v := true
		g.Discovery.FallbackEnabled = &v
	}
	if g.HTTP.ConnectTimeoutMS == 0 {
		g.HTTP.ConnectTimeoutMS = 2000
	}
	if g.HTTP.ResponseHeaderTimeoutMS == 0 {
		g.HTTP.ResponseHeaderTimeoutMS = 30000
	}
	if g.HTTP.IdleConnTimeoutMS == 0 {
		g.HTTP.IdleConnTimeoutMS = 90000
	}
	if g.Retry.Enabled == nil {
		v := true
		g.Retry.Enabled = &v
	}
	if g.Retry.MaxAttempts == 0 {
		g.Retry.MaxAttempts = 2
	}
	if len(g.Retry.Methods) == 0 {
		g.Retry.Methods = []string{"GET", "HEAD", "OPTIONS"}
	}
}

func validate(cfg Config) error {
	if cfg.Version != supportedVersion {
		return fmt.Errorf("不支持的配置版本: %d", cfg.Version)
	}
	if len(cfg.Gateway.Routes) == 0 {
		return fmt.Errorf("routes 不能为空")
	}
	g := cfg.Gateway
	if g.HTTP.ConnectTimeoutMS <= 0 || g.HTTP.ResponseHeaderTimeoutMS <= 0 || g.HTTP.IdleConnTimeoutMS <= 0 {
		return fmt.Errorf("http 超时必须为正数")
	}
	if g.Retry.MaxAttempts < 1 {
		return fmt.Errorf("retry.max-attempts 必须为正数")
	}
	for _, method := range g.Retry.Methods {
		if !allowedRetryMethods[strings.ToUpper(method)] {
			return fmt.Errorf("retry.methods 包含非法方法: %s", method)
		}
	}

	ids := make(map[string]bool, len(g.Routes))
	paths := make(map[string]bool, len(g.Routes))
	prefixes := make(map[string]bool, len(g.Routes))
	for i, r := range g.Routes {
		label := r.ID
		if label == "" {
			label = fmt.Sprintf("routes[%d]", i)
		}
		if r.ID == "" {
			return fmt.Errorf("%s: id 不能为空", label)
		}
		if ids[r.ID] {
			return fmt.Errorf("%s: id 重复", label)
		}
		ids[r.ID] = true

		if (r.Path == "") == (r.Prefix == "") {
			return fmt.Errorf("%s: path 与 prefix 必须二选一", label)
		}
		if r.Path != "" {
			if !strings.HasPrefix(r.Path, "/") {
				return fmt.Errorf("%s: path 必须以 / 开头", label)
			}
			if paths[r.Path] {
				return fmt.Errorf("%s: path 重复", label)
			}
			paths[r.Path] = true
		}
		if r.Prefix != "" {
			if !strings.HasPrefix(r.Prefix, "/") {
				return fmt.Errorf("%s: prefix 必须以 / 开头", label)
			}
			if prefixes[r.Prefix] {
				return fmt.Errorf("%s: prefix 重复", label)
			}
			prefixes[r.Prefix] = true
		}
		if !servicePattern.MatchString(r.Service) {
			return fmt.Errorf("%s: 非法服务名 %q", label, r.Service)
		}
		if r.Websocket && r.Path == "" {
			return fmt.Errorf("%s: websocket 路由必须使用 path 精确匹配", label)
		}
	}
	return nil
}

func buildSnapshot(cfg Config, checksum string) *Snapshot {
	s := &Snapshot{
		ConfigVersion: cfg.Version,
		Checksum:      checksum,
		LoadedAt:      time.Now(),
		Gateway:       cfg.Gateway,
		Routes:        append([]Route(nil), cfg.Gateway.Routes...),
		exact:         make(map[string]Route),
	}
	seen := make(map[string]bool)
	for _, r := range s.Routes {
		if r.Path != "" {
			s.exact[r.Path] = r
		} else {
			s.prefixes = append(s.prefixes, r)
		}
		if !seen[r.Service] {
			seen[r.Service] = true
			s.services = append(s.services, r.Service)
		}
	}
	return s
}
