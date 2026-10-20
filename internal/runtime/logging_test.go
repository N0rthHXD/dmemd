package runtime

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerRedactsCredentialsAndOmitsContent(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(&output, slog.LevelInfo)
	logger.Info("provider call",
		"authorization", "Bearer top-secret",
		"api_key", "model-key",
		"content", "用户原始对话不得写入日志",
		"source_content", "更多原文",
		"model_response", `{"candidates":[]}`,
		"status", "ok",
	)

	text := output.String()
	for _, secret := range []string{"top-secret", "model-key", "原始对话", "更多原文", `candidates`} {
		if strings.Contains(text, secret) {
			t.Fatalf("log contains sensitive value %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("log did not contain redaction marker: %s", text)
	}
	if !strings.Contains(text, `"status":"ok"`) {
		t.Fatalf("log lost non-sensitive attribute: %s", text)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &entry); err != nil {
		t.Fatalf("log is not one JSON object: %v", err)
	}
	timestamp, _ := entry["time"].(string)
	if !strings.HasSuffix(timestamp, "Z") {
		t.Fatalf("log timestamp is not UTC: %q", timestamp)
	}
}

func TestParseLogLevel(t *testing.T) {
	for input, want := range map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"WARN":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	} {
		got, err := ParseLogLevel(input)
		if err != nil || got != want {
			t.Fatalf("ParseLogLevel(%q) = %v, %v; want %v", input, got, err, want)
		}
	}
	if _, err := ParseLogLevel("verbose"); err == nil {
		t.Fatal("ParseLogLevel accepted unknown level")
	}
}
