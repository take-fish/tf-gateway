package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// registerAPIProxy 将 /api/v1/<service>/** 原样转发到上游，保留路径、query 与请求头。
func registerAPIProxy(router *gin.Engine, prefix, target string) {
	proxy, err := newUpstreamProxy(target)
	if err != nil {
		log.Printf("跳过上游 %s：%v", target, err)
		return
	}
	router.Any(prefix+"/*any", gin.WrapH(proxy))
}

// registerProxy 注册精确路径代理（例如 WebSocket 握手路径）。
func registerProxy(router *gin.Engine, path, target string) {
	proxy, err := newUpstreamProxy(target)
	if err != nil {
		log.Printf("跳过上游 %s：%v", target, err)
		return
	}
	router.Any(path, gin.WrapH(proxy))
}

func newUpstreamProxy(target string) (*httputil.ReverseProxy, error) {
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("upstream URL is empty")
	}
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	// 流式响应/长连接不缓冲；WebSocket Upgrade 由标准库原样透传。
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("上游不可用 %s: %v", target, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"BAD_GATEWAY","message":"upstream unavailable"}`))
	}
	return proxy, nil
}
