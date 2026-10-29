package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"runtime/debug"
	"strconv"
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
	attemptKey
	identityHeadersKey
)

// attemptCounter 记录当前请求已经重试的次数，访问日志与重试日志共用同一个计数。
type attemptCounter struct {
	value atomic.Int32
}

func (c *attemptCounter) add() int {
	return int(c.value.Add(1))
}

func (c *attemptCounter) load() int {
	return int(c.value.Load())
}

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
		Rewrite:        p.rewrite,
		ModifyResponse: stripUpstreamTraceHeaders,
		Transport:      p,
		FlushInterval:  -1,
		ErrorHandler:   p.handleError,
	}
	return p
}

// stripUpstreamTraceHeaders 丢弃上游回写的链路响应头。
// ReverseProxy 复制响应头时使用 Add 而不是 Set，若不剔除，网关与上游各写一份，客户端会看到重复的 X-Trace-ID。
func stripUpstreamTraceHeaders(resp *http.Response) error {
	resp.Header.Del("X-Trace-ID")
	resp.Header.Del("X-Request-ID")
	return nil
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
	trace := newTraceContext()
	r = r.WithContext(withTraceContext(r.Context(), trace))

	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	attemptsState := &attemptCounter{}
	w.Header().Set("X-Request-ID", trace.TraceID)
	w.Header().Set("X-Trace-ID", trace.TraceID)

	var (
		routeID       string
		targetService string
		instance      string
		serviceVer    string
		fallback      bool
		attempts      int
	)

	base := []any{
		"component", "gateway",
		"trace_id", trace.TraceID,
		"span_id", trace.SpanID,
		"request_id", trace.TraceID,
		"method", r.Method,
		"path", r.URL.Path,
	}

	// 最后统一输出访问日志；发生 panic 时也保证有记录并返回可检索的错误编号。
	defer func() {
		attempts = attemptsState.load()
		if recovered := recover(); recovered != nil {
			slog.Error("gateway panic recovered",
				append(base,
					"event", "http.panic",
					"route", routeID,
					"target_service", targetService,
					"instance", instance,
					"fallback", fallback,
					"attempts", attempts,
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)...)
			if !recorder.wroteHeader {
				writeError(recorder, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			}
		}

		level := slog.LevelInfo
		switch {
		case recorder.status >= 500:
			level = slog.LevelError
		case recorder.status >= 400:
			level = slog.LevelWarn
		}
		slog.Log(context.Background(), level, "request completed",
			append(base,
				"event", "http.access",
				"route", routeID,
				"target_service", targetService,
				"instance", instance,
				"service_version", serviceVer,
				"fallback", fallback,
				"attempts", attempts,
				"status", recorder.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)...)
	}()

	snapshot := p.app.Snapshot()
	matched, ok := snapshot.Match(r.URL.Path)
	if !ok {
		writeError(recorder, r, http.StatusNotFound, "NOT_FOUND", "no route matched")
		return
	}
	routeID = matched.ID
	targetService = matched.Service

	// 无论是否鉴权，都先剥离客户端伪造的内部身份头。
	stripInboundIdentity(r.Header)

	var signedHeaders map[string]string
	if p.app.cfg.AuthEnabled && !matched.Websocket &&
		!isPublicPath(r.URL.Path, p.app.cfg.AuthPublicPaths) &&
		!strings.EqualFold(r.Method, http.MethodOptions) {
		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			writeError(recorder, r, http.StatusUnauthorized, "UNAUTHORIZED", "请先登录")
			return
		}
		identity, authErr := p.app.authenticate(r.Context(), token)
		if authErr != nil {
			if errors.Is(authErr, errUnauthorized) {
				writeError(recorder, r, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效，请重新登录")
			} else {
				writeError(recorder, r, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "登录校验服务暂不可用，请稍后重试")
			}
			return
		}
		headers, signErr := signIdentity(p.app.cfg.AuthSecret, identity,
			time.Now().Add(identityTTLSeconds*time.Second).Unix())
		if signErr != nil {
			slog.Error("identity signing failed", "event", "gateway.auth_config", "component", "gateway", "error", signErr.Error())
			writeError(recorder, r, http.StatusServiceUnavailable, "AUTH_CONFIG_MISSING", "网关身份签名未配置")
			return
		}
		signedHeaders = headers
	}

	result, err := p.app.resolver.Resolve(matched.Service)
	if err != nil {
		slog.Warn("no healthy instance",
			append(base,
				"event", "gateway.resolve",
				"route", routeID,
				"target_service", targetService,
				"error", err.Error(),
			)...)
		writeError(recorder, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "no healthy instance for "+matched.Service)
		return
	}
	instance = result.Instance
	serviceVer = versionOf(result)
	fallback = result.Fallback

	ctx := context.WithValue(r.Context(), targetKey, result)
	ctx = context.WithValue(ctx, routeKey, matched)
	ctx = context.WithValue(ctx, attemptKey, attemptsState)
	if signedHeaders != nil {
		ctx = context.WithValue(ctx, identityHeadersKey, signedHeaders)
	}
	out := r.WithContext(ctx)
	out.Header = r.Header.Clone()

	p.reverse.ServeHTTP(recorder, out)
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

	// 网关是链路起点：无论客户端传入什么，都覆盖为本次请求生成的链路信息。
	trace := traceContextFrom(pr.In.Context())
	if trace.TraceID == "" {
		trace = newTraceContext()
	}
	attempts := 0
	if counter, ok := pr.In.Context().Value(attemptKey).(*attemptCounter); ok {
		attempts = counter.load()
	}
	pr.Out.Header.Set("traceparent", trace.traceparent())
	pr.Out.Header.Set("X-Request-ID", trace.TraceID)
	pr.Out.Header.Set("X-Trace-ID", trace.TraceID)
	pr.Out.Header.Del("tracestate")
	pr.Out.Header.Set("X-Gateway-Attempt", strconv.Itoa(attempts))

	if headers, ok := pr.In.Context().Value(identityHeadersKey).(map[string]string); ok {
		for name, value := range headers {
			pr.Out.Header.Set(name, value)
		}
	}
}

func (p *Proxy) handleError(w http.ResponseWriter, r *http.Request, err error) {
	snapshot := p.app.Snapshot()
	counter, _ := r.Context().Value(attemptKey).(*attemptCounter)
	if counter == nil {
		counter = &attemptCounter{}
	}
	attempts := counter.load()
	trace := traceContextFrom(r.Context())
	canRetry := snapshot.RetryEnabled() &&
		attempts+1 < snapshot.Retry().MaxAttempts &&
		retryableMethod(r.Method, snapshot.Retry().Methods) &&
		(r.Body == nil || r.ContentLength == 0)

	if canRetry {
		matched, ok := r.Context().Value(routeKey).(route.Route)
		if ok {
			if result, resolveErr := p.app.resolver.Resolve(matched.Service); resolveErr == nil {
				// 同一 trace-id 下开新 span，重试过程仍可按一个编号查全。
				nextTrace := trace.child()
				nextAttempt := counter.add()
				ctx := context.WithValue(r.Context(), targetKey, result)
				ctx = withTraceContext(ctx, nextTrace)
				slog.Warn("upstream attempt failed, retrying",
					"event", "gateway.retry",
					"component", "gateway",
					"trace_id", trace.TraceID,
					"span_id", trace.SpanID,
					"next_span_id", nextTrace.SpanID,
					"request_id", trace.TraceID,
					"route", matched.ID,
					"target_service", matched.Service,
					"instance", result.Instance,
					"attempt", attempts,
					"next_attempt", nextAttempt,
					"error", err.Error(),
				)
				p.reverse.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
	}

	result, _ := r.Context().Value(targetKey).(resolver.Result)
	matched, _ := r.Context().Value(routeKey).(route.Route)
	slog.Error("upstream request failed",
		"event", "gateway.upstream_error",
		"component", "gateway",
		"trace_id", trace.TraceID,
		"span_id", trace.SpanID,
		"request_id", trace.TraceID,
		"route", matched.ID,
		"target_service", matched.Service,
		"instance", result.Instance,
		"fallback", result.Fallback,
		"attempt", attempts,
		"error", err.Error(),
	)
	writeError(w, r, http.StatusBadGateway, "BAD_GATEWAY", "upstream unavailable")
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

func versionOf(result resolver.Result) string {
	if result.Version == "" {
		return "unknown"
	}
	return result.Version
}

type errorBody struct {
	Error     string    `json:"error"`
	Message   string    `json:"message"`
	TraceID   string    `json:"traceId"`
	RequestID string    `json:"requestId"`
	Path      string    `json:"path"`
	Timestamp time.Time `json:"timestamp"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	trace := traceContextFrom(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if trace.TraceID != "" {
		w.Header().Set("X-Trace-ID", trace.TraceID)
		w.Header().Set("X-Request-ID", trace.TraceID)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{
		Error:     code,
		Message:   message,
		TraceID:   trace.TraceID,
		RequestID: trace.TraceID,
		Path:      r.URL.Path,
		Timestamp: time.Now().UTC(),
	})
}

// statusRecorder 记录状态码，同时保留 Flush/Hijack，保证 WebSocket 正常升级。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
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
