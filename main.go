package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// version 由构建时通过 -ldflags "-X main.version=..." 注入
var version = "dev"

func setupRouter() *gin.Engine {
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
	return r
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	gin.SetMode(gin.ReleaseMode)
	log.Printf("tf-gateway %s listening on :%s", version, port)
	if err := setupRouter().Run(":" + port); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
