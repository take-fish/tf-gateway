package main

import "os"

// Config 网关运行配置。上游地址全部可由环境变量覆盖，默认指向本机各服务端口。
type Config struct {
	Port      string
	ChatURL   string
	ReaderURL string
	GameURL   string
	MusicURL  string
	UserURL   string
}

func LoadConfig() Config {
	return Config{
		Port:      getenv("PORT", "8080"),
		ChatURL:   getenv("TF_CHAT_URL", "http://127.0.0.1:8102"),
		ReaderURL: getenv("TF_READER_URL", "http://127.0.0.1:8103"),
		GameURL:   getenv("TF_GAME_URL", "http://127.0.0.1:8104"),
		MusicURL:  getenv("TF_MUSIC_URL", "http://127.0.0.1:8105"),
		UserURL:   getenv("TF_USER_URL", "http://127.0.0.1:8101"),
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
