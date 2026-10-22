package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
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

func TestGatewayOverridesInboundTraceHeaders(t *testing.T) {
	type forwarded struct {
		Traceparent string `json:"traceparent"`
		RequestID   string `json:"requestId"`
		TraceID     string `json:"traceId"`
		Attempt     string `json:"attempt"`
	}
	received := make(chan forwarded, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- forwarded{
			Traceparent: r.Header.Get("traceparent"),
			RequestID:   r.Header.Get("X-Request-ID"),
			TraceID:     r.Header.Get("X-Trace-ID"),
			Attempt:     r.Header.Get("X-Gateway-Attempt"),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(newRouter(testApp(map[string]string{"tf-chat": upstream.URL})))
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodGet, gateway.URL+"/api/v1/chat/groups", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	req.Header.Set("tracestate", "vendor=forged")
	req.Header.Set("X-Request-ID", "attacker-chosen-id")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-ID") == "attacker-chosen-id" {
		t.Fatal("gateway must not trust inbound X-Request-ID")
	}

	got := <-received
	if got.RequestID == "attacker-chosen-id" || got.RequestID == "" {
		t.Fatalf("request id not regenerated: %q", got.RequestID)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got.RequestID) {
		t.Fatalf("request id is not 32 hex chars: %q", got.RequestID)
	}
	if got.TraceID != got.RequestID || resp.Header.Get("X-Trace-ID") != got.RequestID {
		t.Fatalf("trace id mismatch: upstream=%q request=%q response=%q", got.TraceID, got.RequestID, resp.Header.Get("X-Trace-ID"))
	}
	traceparent := regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`).FindStringSubmatch(got.Traceparent)
	if traceparent == nil {
		t.Fatalf("invalid traceparent: %q", got.Traceparent)
	}
	if traceparent[1] != got.RequestID {
		t.Fatalf("traceparent trace id = %q, want %q", traceparent[1], got.RequestID)
	}
	if got.Attempt != "0" {
		t.Fatalf("gateway attempt = %q, want 0", got.Attempt)
	}
}

func TestErrorResponseIncludesTraceableIDs(t *testing.T) {
	gateway := httptest.NewServer(newRouter(testApp(map[string]string{})))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/does-not-exist?token=secret")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Error     string    `json:"error"`
		Message   string    `json:"message"`
		TraceID   string    `json:"traceId"`
		RequestID string    `json:"requestId"`
		Path      string    `json:"path"`
		Timestamp time.Time `json:"timestamp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound || body.Error != "NOT_FOUND" {
		t.Fatalf("status=%d error=%q, want 404 NOT_FOUND", resp.StatusCode, body.Error)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(body.TraceID) {
		t.Fatalf("traceId is not 32 hex chars: %q", body.TraceID)
	}
	if body.RequestID != body.TraceID || resp.Header.Get("X-Trace-ID") != body.TraceID {
		t.Fatalf("ids mismatch: body=%+v header=%q", body, resp.Header.Get("X-Trace-ID"))
	}
	if body.Path != "/api/v1/does-not-exist" {
		t.Fatalf("path = %q, want path without query string", body.Path)
	}
	if body.Timestamp.IsZero() {
		t.Fatal("timestamp is missing")
	}
}

func TestRetryKeepsTraceIDAndChangesSpanID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	transport := &tracingTransport{remainingFailures: 1, real: http.DefaultTransport}
	app := testApp(map[string]string{"tf-chat": upstream.URL})
	app.proxy.setTransport(transport)
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

	transport.mu.Lock()
	traceparents := append([]string(nil), transport.traceparents...)
	attempts := append([]string(nil), transport.attempts...)
	transport.mu.Unlock()
	if len(traceparents) != 2 {
		t.Fatalf("attempts = %d, want 2", len(traceparents))
	}
	pattern := regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`)
	first := pattern.FindStringSubmatch(traceparents[0])
	second := pattern.FindStringSubmatch(traceparents[1])
	if first == nil || second == nil {
		t.Fatalf("invalid traceparent values: %v", traceparents)
	}
	if first[1] != second[1] {
		t.Fatalf("trace id changed across retry: %q -> %q", first[1], second[1])
	}
	if first[2] == second[2] {
		t.Fatalf("span id reused across retry: %q", first[2])
	}
	if attempts[0] != "0" || attempts[1] != "1" {
		t.Fatalf("attempt headers = %v, want [0 1]", attempts)
	}
}

type tracingTransport struct {
	mu                sync.Mutex
	traceparents      []string
	attempts          []string
	remainingFailures int
	real              http.RoundTripper
}

func (t *tracingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.traceparents = append(t.traceparents, r.Header.Get("traceparent"))
	t.attempts = append(t.attempts, r.Header.Get("X-Gateway-Attempt"))
	fail := t.remainingFailures > 0
	if fail {
		t.remainingFailures--
	}
	t.mu.Unlock()
	if fail {
		return nil, errors.New("dial failed")
	}
	return t.real.RoundTrip(r)
}

func (t *tracingTransport) CloseIdleConnections() {}

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

func TestGatewayDoesNotDuplicateTraceHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟 Java 服务回写链路响应头。
		w.Header().Set("X-Trace-ID", r.Header.Get("X-Trace-ID"))
		w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(newRouter(testApp(map[string]string{"tf-chat": upstream.URL})))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/api/v1/chat/groups")
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	for _, name := range []string{"X-Trace-ID", "X-Request-ID"} {
		if values := resp.Header.Values(name); len(values) != 1 {
			t.Fatalf("%s values = %v, want exactly one", name, values)
		}
	}
}
