package runtime

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

var redactedLogKeys = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"api_key":             {},
	"apikey":              {},
	"token":               {},
	"password":            {},
	"secret":              {},
	"client_secret":       {},
}

var omittedLogKeys = map[string]struct{}{
	"content":        {},
	"raw_content":    {},
	"body":           {},
	"request_body":   {},
	"response_body":  {},
	"model_response": {},
	"raw_response":   {},
}

// NewLogger returns a structured JSON logger whose default policy drops raw
// content/request/response bodies and redacts credentials or bearer headers.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: RedactLogAttr,
	})
	return slog.New(handler)
}

// ParseLogLevel maps configuration text to slog levels.
func ParseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("未知日志级别 %q（支持 debug、info、warn、error）", value)
	}
}

// RedactLogAttr is the shared slog ReplaceAttr policy. Sensitive values are
// replaced with a fixed marker; raw payload fields are omitted entirely.
// Log timestamps are normalized to UTC.
func RedactLogAttr(_ []string, attr slog.Attr) slog.Attr {
	if attr.Key == slog.TimeKey && attr.Value.Kind() == slog.KindTime {
		return slog.Time(slog.TimeKey, attr.Value.Time().UTC())
	}
	key := strings.ToLower(attr.Key)
	if _, omit := omittedLogKeys[key]; omit {
		return slog.Attr{}
	}
	if _, redact := redactedLogKeys[key]; redact {
		return slog.String(attr.Key, "[REDACTED]")
	}
	if strings.HasSuffix(key, "_body") || strings.HasSuffix(key, "_content") && key != "content_type" {
		return slog.Attr{}
	}
	return attr
}
