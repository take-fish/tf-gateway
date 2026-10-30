package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是网关启动配置。Nacos 只负责路由与实例发现，失败时静态地址仍可用。
type Config struct {
	Port string

	Environment string
	LogLevel    string

	NacosAddr      string
	NacosNamespace string
	NacosUsername  string
	NacosPassword  string
	NacosLogDir    string
	NacosCacheDir  string

	ConfigDataID     string
	ConfigGroup      string
	DiscoveryGroup   string
	DiscoveryEnabled bool

	Static map[string]string

	AuthEnabled        bool
	AuthSecret         string
	AuthCacheTTL       time.Duration
	AuthUserService    string
	AuthPublicPaths    []string
	AuthConnectTimeout time.Duration
	AuthReadTimeout    time.Duration

	// JWT 本地验签：配置公钥后优先本地校验，不再回调用户中台。
	AuthJWTEnabled   bool
	AuthJWTPublicKey string
	AuthJWTKeys      map[string]string
	AuthJWTKeyID     string
	AuthJWTIssuer    string
}

// DefaultAuthPublicPaths 是无需网关校验 token 的路径。
// 管理页静态资源必须放行，页面内的 API 仍由用户中台自行鉴权。
var DefaultAuthPublicPaths = []string{
	"/healthz",
	"/readyz",
	"/api/v1/monitor/metrics",
	"/api/v1/user/auth/login",
	"/api/v1/user/auth/register",
	// 刷新令牌换访问令牌：此刻手里的 access token 已过期，必须放行
	"/api/v1/user/auth/refresh",
	"/api/v1/user/meta",
	// 头像属于公开展示的身份信息，<img> 无法携带 Authorization
	"/api/v1/user/avatars/**",
	"/api/v1/*/health",
	"/api/v1/*/meta",
	"/api/v1/user/admin",
	"/api/v1/user/admin/",
	"/api/v1/user/admin/index.html",
	"/api/v1/user/admin/admin.js",
	"/api/v1/user/admin/admin.css",
	"/api/v1/user/admin/permissions.html",
	"/api/v1/user/admin/permissions.js",
}

func LoadConfig() Config {
	return Config{
		Port:             getenv("PORT", "8080"),
		Environment:      getenv("APP_ENV", "production"),
		LogLevel:         getenv("LOG_LEVEL", "info"),
		NacosAddr:        getenv("NACOS_ADDR", "127.0.0.1:8848"),
		NacosNamespace:   getenv("NACOS_NAMESPACE", "fm-prod"),
		NacosUsername:    firstNonEmpty(os.Getenv("GATEWAY_NACOS_USERNAME"), os.Getenv("NACOS_USERNAME"), "nacos"),
		NacosPassword:    firstNonEmpty(os.Getenv("GATEWAY_NACOS_PASSWORD"), os.Getenv("NACOS_PASSWORD")),
		NacosLogDir:      getenv("GATEWAY_NACOS_LOG_DIR", "/tmp/nacos/log"),
		NacosCacheDir:    getenv("GATEWAY_NACOS_CACHE_DIR", "/tmp/nacos/cache"),
		ConfigDataID:     getenv("GATEWAY_CONFIG_DATA_ID", "tf-gateway.yaml"),
		ConfigGroup:      getenv("GATEWAY_CONFIG_GROUP", "FM"),
		DiscoveryGroup:   getenv("GATEWAY_DISCOVERY_GROUP", "DEFAULT_GROUP"),
		DiscoveryEnabled: getenvBool("GATEWAY_DISCOVERY_ENABLED", true),
		// 静态兜底地址与 docker-compose 的 TF_*_URL 默认值保持一致：用 compose 服务名，
		// 而不是 127.0.0.1——容器内回环永远指向网关自己，一旦 Nacos 发现失效会把请求打到空。
		Static: map[string]string{
			"tf-chat":   getenv("TF_CHAT_URL", "http://fm-chat:8102"),
			"tf-reader": getenv("TF_READER_URL", "http://fm-reader:8103"),
			"tf-game":   getenv("TF_GAME_URL", "http://fm-game:8104"),
			"tf-music":  getenv("TF_MUSIC_URL", "http://fm-music:8105"),
			"tf-user":   getenv("TF_USER_URL", "http://fm-prod-user:8101"),
		},
		AuthEnabled:        getenvBool("GATEWAY_AUTH_ENABLED", true),
		AuthSecret:         os.Getenv("FM_IDENTITY_SECRET"),
		AuthCacheTTL:       time.Duration(getenvInt("GATEWAY_AUTH_CACHE_SECONDS", 30)) * time.Second,
		AuthUserService:    getenv("GATEWAY_AUTH_USER_SERVICE", "tf-user"),
		AuthPublicPaths:    splitCSV(os.Getenv("GATEWAY_AUTH_PUBLIC_PATHS")),
		AuthConnectTimeout: time.Duration(getenvInt("GATEWAY_AUTH_CONNECT_TIMEOUT_MS", 800)) * time.Millisecond,
		AuthReadTimeout:    time.Duration(getenvInt("GATEWAY_AUTH_READ_TIMEOUT_MS", 2000)) * time.Millisecond,

		AuthJWTEnabled:   getenvBool("GATEWAY_AUTH_JWT_ENABLED", true),
		AuthJWTPublicKey: getenv("FM_AUTH_JWT_PUBLIC_KEY", ""),
		AuthJWTKeys:      parseKeyMap(os.Getenv("FM_AUTH_JWT_KEYS")),
		AuthJWTKeyID:     getenv("FM_AUTH_JWT_KEY_ID", "default"),
		AuthJWTIssuer:    getenv("FM_AUTH_JWT_ISSUER", "fishmatrix"),
	}
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getenvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// parseKeyMap 解析 kid:key,kid2:key2 形式的公钥轮换表。
func parseKeyMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	result := make(map[string]string)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, value, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(kid) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		result[strings.TrimSpace(kid)] = strings.TrimSpace(value)
	}
	return result
}
