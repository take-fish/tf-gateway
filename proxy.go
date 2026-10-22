package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync/atomic"
	"time"

	"github.com/take-fish/tf-gateway/resolver"
	"github.com/take-fish/tf-gateway/route"
)

type contextKey int

const (
	targetKey contextKey = iota
	routeKey
	requestIDKey
	attemptKey
)

// Proxy 按路由快照把请求转发到 Nacos 健康实例或静态回退地址。
type Proxy struct {
	app       *App
	transport atomic.Value
	reverse   *httputil.ReverseProxy
}

// transportHolder 保证 atomic.Value 始终存储同一种具体类型。
type transportHolder struct {
	roundTripper http.RoundTripper
}

func NewProxy(app *App) *Proxy {
	p := &Proxy{app: app}
	p.setTransport(newTransport(app.Snapshot().HTTP()))
	p.reverse = &httputil.ReverseProxy{
		Rewrite:       p.rewrite,
		Transport:     p,
		FlushInterval: -1,
		ErrorHandler:  p.handleError,
	}
	return p
}

// RoundTrip 始终使用当前快照对应的 Transport。
func (p *Proxy) RoundTrip(r *http.Request) (*http.Response, error) {
	holder, _ := p.transport.Load().(transportHolder)
	if holder.roundTripper == nil {
		return nil, http.ErrNotSupported
	}
	return holder.roundTripper.RoundTrip(r)
}

// UpdateTransport 在路由配置变化时重建连接池参数。
func (p *Proxy) UpdateTransport(cfg route.HTTPConfig) {
	old, _ := p.transport.Load().(transportHolder)
	p.setTransport(newTransport(cfg))
	if closer, ok := old.roundTripper.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (p *Proxy) setTransport(roundTripper http.RoundTripper) {
	p.transport.Store(transportHolder{roundTripper: roundTripper})
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := requestID(r)
	snapshot := p.app.Snapshot()
	matched, ok := snapshot.Match(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no route matched")
		log.Printf("level=info request_id=%s route=- service=- instance=- fallback=false status=%d duration_ms=%d",
			requestID, http.StatusNotFound, time.Since(start).Milliseconds())
		return
	}

	result, err := p.app.resolver.Resolve(matched.Service)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "no healthy instance for "+matched.Service)
		log.Printf("level=warn request_id=%s route=%s service=%s instance=- fallback=false status=%d duration_ms=%d error=%q",
			requestID, matched.ID, matched.Service, http.StatusServiceUnavailable, time.Since(start).Milliseconds(), err.Error())
		return
	}

	ctx := context.WithValue(r.Context(), targetKey, result)
	ctx = context.WithValue(ctx, routeKey, matched)
	ctx = context.WithValue(ctx, requestIDKey, requestID)
	out := r.WithContext(ctx)
	out.Header = r.Header.Clone()
	out.Header.Set("X-Request-ID", requestID)

	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	p.reverse.ServeHTTP(recorder, out)

	log.Printf("level=info request_id=%s route=%s service=%s instance=%s version=%s fallback=%t status=%d duration_ms=%d",
		requestID, matched.ID, matched.Service, result.Instance, versionOf(result), result.Fallback,
		recorder.status, time.Since(start).Milliseconds())
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	result, ok := pr.In.Context().Value(targetKey).(resolver.Result)
	if !ok || result.Target == nil {
		return
	}
	pr.Out.URL.Scheme = result.Target.Scheme
	pr.Out.URL.Host = result.Target.Host
	pr.Out.Host = pr.In.Host
	pr.SetXForwarded()
	if requestID, ok := pr.In.Context().Value(requestIDKey).(string); ok {
		pr.Out.Header.Set("X-Request-ID", requestID)
	}
}

func (p *Proxy) handleError(w http.ResponseWriter, r *http.Request, err error) {
	snapshot := p.app.Snapshot()
	attempts, _ := r.Context().Value(attemptKey).(int)
	canRetry := snapshot.RetryEnabled() &&
		attempts+1 < snapshot.Retry().MaxAttempts &&
		retryableMethod(r.Method, snapshot.Retry().Methods) &&
		(r.Body == nil || r.ContentLength == 0)
	if canRetry {
		matched, ok := r.Context().Value(routeKey).(route.Route)
		if ok {
			if result, err := p.app.resolver.Resolve(matched.Service); err == nil {
				ctx := context.WithValue(r.Context(), targetKey, result)
				ctx = context.WithValue(ctx, attemptKey, attempts+1)
				p.reverse.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
	}

	result, _ := r.Context().Value(targetKey).(resolver.Result)
	matched, _ := r.Context().Value(routeKey).(route.Route)
	requestID, _ := r.Context().Value(requestIDKey).(string)
	log.Printf("level=warn request_id=%s route=%s service=%s instance=%s fallback=%t status=502 error=%q",
		requestID, matched.ID, matched.Service, result.Instance, result.Fallback, err.Error())
	writeError(w, http.StatusBadGateway, "BAD_GATEWAY", "upstream unavailable")
}

func retryableMethod(method string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(method, candidate) {
			return true
		}
	}
	return false
}

func newTransport(cfg route.HTTPConfig) *http.Transport {
	connectTimeout := time.Duration(cfg.ConnectTimeoutMS) * time.Millisecond
	headerTimeout := time.Duration(cfg.ResponseHeaderTimeoutMS) * time.Millisecond
	idleTimeout := time.Duration(cfg.IdleConnTimeoutMS) * time.Millisecond
	if connectTimeout <= 0 {
		connectTimeout = 2 * time.Second
	}
	if headerTimeout <= 0 {
		headerTimeout = 30 * time.Second
	}
	if idleTimeout <= 0 {
		idleTimeout = 90 * time.Second
	}
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: headerTimeout,
		IdleConnTimeout:       idleTimeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		ExpectContinueTimeout: time.Second,
	}
}

func requestID(r *http.Request) string {
	if existing := r.Header.Get("X-Request-ID"); existing != "" && len(existing) <= 128 {
		return existing
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return time.Now().Format("20060102150405.000000000")
}

func versionOf(result resolver.Result) string {
	if result.Version == "" {
		return "unknown"
	}
	return result.Version
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

// statusRecorder 记录状态码，同时保留 Flush/Hijack，保证 WebSocket 正常升级。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.status = http.StatusSwitchingProtocols
	w.wroteHeader = true
	return hijacker.Hijack()
}

func (w *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

func (w *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}
