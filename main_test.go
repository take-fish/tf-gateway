package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testApp(static map[string]string) *App {
	return NewApp(Config{
		Port:             "0",
		DiscoveryEnabled: false,
		AuthEnabled:      false,
		Static:           static,
	})
}

func TestHealthz(t *testing.T) {
	handler := newRouter(testApp(map[string]string{}))
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var health Health
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.Routes != 7 {
		t.Fatalf("routes = %d, want 7", health.Routes)
	}
	if health.Nacos.Config != "disabled" || health.Nacos.Discovery != "disabled" {
		t.Fatalf("unexpected nacos state: %+v", health.Nacos)
	}
}

func TestReadyz(t *testing.T) {
	handler := newRouter(testApp(map[string]string{}))
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", w.Code)
	}
}

func TestMonitorMetrics(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/monitor/metrics", nil)
	newRouter(testApp(map[string]string{})).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/monitor/metrics = %d, want 200", w.Code)
	}
}
