package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// version 由构建时通过 -ldflags "-X main.version=..." 注入
var version = "dev"

func setupRouter(cfg Config) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "tf-gateway", "version": version})
	})

	// 伪装路径示例：统一网关对外呈现为运维工具风格接口
	api := r.Group("/api/v1")
	{
		api.GET("/monitor/metrics", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": gin.H{"qps": 128, "cpu_usage": 0.12, "memory_usage": 0.34},
			})
		})
	}

	registerAPIProxy(r, "/api/v1/chat", cfg.ChatURL)
	registerAPIProxy(r, "/api/v1/reader", cfg.ReaderURL)
	registerAPIProxy(r, "/api/v1/game", cfg.GameURL)
	registerAPIProxy(r, "/api/v1/music", cfg.MusicURL)
	registerAPIProxy(r, "/api/v1/user", cfg.UserURL)
	registerProxy(r, "/ws/chat", cfg.ChatURL)

	return r
}

func main() {
	cfg := LoadConfig()

	gin.SetMode(gin.ReleaseMode)
	log.Printf("tf-gateway %s listening on :%s", version, cfg.Port)
	log.Printf(
		"upstreams: chat=%s reader=%s game=%s music=%s user=%s",
		cfg.ChatURL, cfg.ReaderURL, cfg.GameURL, cfg.MusicURL, cfg.UserURL)
	if err := setupRouter(cfg).Run(":" + cfg.Port); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
