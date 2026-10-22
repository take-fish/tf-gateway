package main

import (
	"os"
	"strconv"
)

// Config 是网关启动配置。Nacos 只负责路由与实例发现，失败时静态地址仍可用。
type Config struct {
	Port string

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
}

func LoadConfig() Config {
	return Config{
		Port:             getenv("PORT", "8080"),
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
		Static: map[string]string{
			"tf-chat":   getenv("TF_CHAT_URL", "http://127.0.0.1:8102"),
			"tf-reader": getenv("TF_READER_URL", "http://127.0.0.1:8103"),
			"tf-game":   getenv("TF_GAME_URL", "http://127.0.0.1:8104"),
			"tf-music":  getenv("TF_MUSIC_URL", "http://127.0.0.1:8105"),
			"tf-user":   getenv("TF_USER_URL", "http://127.0.0.1:8101"),
		},
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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
