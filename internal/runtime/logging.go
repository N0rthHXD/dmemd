package runtime

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// NewLogger returns a human-readable text logger whose default policy drops raw
// content/request/response bodies and redacts credentials or bearer headers.
// Timestamps use the process's local time zone.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: RedactLogAttr,
	})
	return slog.New(handler)
}

// OpenLogFile opens path in append mode with 0600 permissions, creating parent
// directories as needed. An empty path returns nil so callers can log to stderr
// only.
func OpenLogFile(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建日志目录 %s: %w", dir, err)
		}
	}
	return OpenPrivateFile(path, os.O_WRONLY|os.O_APPEND)
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
// Timestamps pass through unchanged so they stay in the local time zone.
func RedactLogAttr(_ []string, attr slog.Attr) slog.Attr {
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
