package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyForwardsPathAndQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"path":%q,"query":%q,"requestId":%q}`,
			r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Request-ID"))
	}))
	defer upstream.Close()

	app := testApp(map[string]string{"tf-chat": upstream.URL})
	gateway := httptest.NewServer(newRouter(app))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/chat/groups?limit=3")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"path":"/api/v1/chat/groups"`) {
		t.Fatalf("path not preserved: %s", body)
	}
	if !strings.Contains(body, `"query":"limit=3"`) {
		t.Fatalf("query not preserved: %s", body)
	}
	if !strings.Contains(body, `"requestId":"`) {
		t.Fatalf("request id not forwarded: %s", body)
	}
}

func TestProxyReturnsNotFound(t *testing.T) {
	gateway := httptest.NewServer(newRouter(testApp(map[string]string{})))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/nope")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "NOT_FOUND") {
		t.Fatalf("status=%d body=%s, want 404 NOT_FOUND", resp.StatusCode, body)
	}
}

func TestProxyReturnsServiceUnavailable(t *testing.T) {
	gateway := httptest.NewServer(newRouter(testApp(map[string]string{"tf-chat": ""})))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/chat/groups")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "SERVICE_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s, want 503 SERVICE_UNAVAILABLE", resp.StatusCode, body)
	}
}

func TestProxyReturnsBadGateway(t *testing.T) {
	gateway := httptest.NewServer(newRouter(testApp(map[string]string{"tf-chat": "http://127.0.0.1:1"})))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/chat/groups")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "BAD_GATEWAY") {
		t.Fatalf("unexpected error body: %s", body)
	}
}

func TestProxyRetriesIdempotentRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app := testApp(map[string]string{"tf-chat": upstream.URL})
	app.proxy.setTransport(&flakyTransport{
		remainingFailures: 1,
		real:              http.DefaultTransport,
	})
	gateway := httptest.NewServer(newRouter(app))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/chat/groups")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after retry", resp.StatusCode)
	}
}

func TestProxyDoesNotRetryPost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app := testApp(map[string]string{"tf-chat": upstream.URL})
	flaky := &flakyTransport{remainingFailures: 1, real: http.DefaultTransport}
	app.proxy.setTransport(flaky)
	gateway := httptest.NewServer(newRouter(app))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL+"/api/v1/chat/messages", "application/json", strings.NewReader(`{"content":"x"}`))
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (no retry for POST)", resp.StatusCode)
	}
	if flaky.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", flaky.calls)
	}
}

func TestProxyWebSocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "upgrade required", http.StatusBadRequest)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream response writer does not support hijack")
			return
		}
		conn, buffer, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
		_, _ = buffer.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buffer.Flush()
		time.Sleep(80 * time.Millisecond)
	}))
	defer upstream.Close()

	app := testApp(map[string]string{"tf-chat": upstream.URL})
	gateway := httptest.NewServer(newRouter(app))
	defer gateway.Close()

	addr := strings.TrimPrefix(gateway.URL, "http://")
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(
		conn,
		"GET /ws/chat HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n",
		addr)
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(line, "101") {
		t.Fatalf("expected 101 upgrade, got %q", line)
	}
}

type flakyTransport struct {
	mu                sync.Mutex
	remainingFailures int
	calls             int
	real              http.RoundTripper
}

func (f *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	fail := f.remainingFailures > 0
	if fail {
		f.remainingFailures--
	}
	f.mu.Unlock()
	if fail {
		return nil, errors.New("dial failed")
	}
	return f.real.RoundTrip(r)
}

func (f *flakyTransport) CloseIdleConnections() {}
