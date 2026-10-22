package main

import (
	"log/slog"
	"os"
	"strings"
)

// newLogger 输出结构化 JSON 日志到 stdout，字段与 Java 服务的 JSON 日志保持一致，
// 便于 Loki 用 trace_id / request_id 统一检索。
func newLogger(cfg Config) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
		// 与 Java 服务的 logstash 字段对齐，便于 Loki 用同一套标签和字段检索。
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			switch attr.Key {
			case slog.TimeKey:
				attr.Key = "@timestamp"
			case slog.MessageKey:
				attr.Key = "message"
			case slog.LevelKey:
				if level, ok := attr.Value.Any().(slog.Level); ok {
					attr.Value = slog.StringValue(strings.ToLower(level.String()))
				}
			}
			return attr
		},
	})
	logger := slog.New(handler).With(
		"service", "tf-gateway",
		"env", cfg.Environment,
		"version", version,
	)
	slog.SetDefault(logger)
	return logger
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
