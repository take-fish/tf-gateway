package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version 由构建时通过 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	cfg := LoadConfig()
	app := NewApp(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.Start(ctx)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           newRouter(app),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("level=warn component=gateway shutdown_error=%q", err.Error())
		}
	}()

	log.Printf("level=info component=gateway version=%s port=%s namespace=%s discovery_enabled=%t fallback_targets=%d",
		version, cfg.Port, cfg.NacosNamespace, cfg.DiscoveryEnabled, len(cfg.Static))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server exited: %v", err)
	}
	log.Printf("level=info component=gateway stopped")
}

func newRouter(app *App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		health := app.Health()
		status := http.StatusOK
		if health.Status == "failed" {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, health)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !app.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	// 保留原有伪装监控接口。
	mux.HandleFunc("/api/v1/monitor/metrics", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 0,
			"msg":  "success",
			"data": map[string]any{"qps": 128, "cpu_usage": 0.12, "memory_usage": 0.34},
		})
	})
	mux.Handle("/", app.proxy)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
