package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// traceContext 是 W3C Trace Context 在网关内的最小表示。
// X-Request-ID 与 trace-id 相同，保证前端拿到的编号可以直接检索日志。
type traceContext struct {
	TraceID string
	SpanID  string
}

type traceContextKey struct{}

var fallbackSequence atomic.Uint64

func newTraceContext() traceContext {
	return traceContext{TraceID: randomHex(16), SpanID: randomHex(8)}
}

// child 为下一次尝试生成新的 span-id，trace-id 保持不变。
func (t traceContext) child() traceContext {
	return traceContext{TraceID: t.TraceID, SpanID: randomHex(8)}
}

func (t traceContext) traceparent() string {
	return "00-" + t.TraceID + "-" + t.SpanID + "-01"
}

func withTraceContext(ctx context.Context, trace traceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, trace)
}

func traceContextFrom(ctx context.Context) traceContext {
	trace, _ := ctx.Value(traceContextKey{}).(traceContext)
	return trace
}

// randomHex 生成指定字节数的随机十六进制串；随机源不可用时退化为纳秒时间戳，
// 保证永远不会返回空值或全零的链路 ID。
func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err == nil {
		value := hex.EncodeToString(buf)
		if value != strings.Repeat("0", len(value)) {
			return value
		}
	}
	value := uint64(time.Now().UnixNano()) ^ fallbackSequence.Add(1)
	if value == 0 {
		value = 1
	}
	return fmt.Sprintf("%0*x", size*2, value)
}
