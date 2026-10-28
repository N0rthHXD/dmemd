package runtime

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if !strings.Contains(text, "status=ok") {
		t.Fatalf("log lost non-sensitive attribute: %s", text)
	}
	if !strings.Contains(text, "level=INFO") || strings.Contains(text, `"level":`) {
		t.Fatalf("log is not plain text: %s", text)
	}
}

func TestLoggerKeepsLocalTimestamp(t *testing.T) {
	local := time.FixedZone("local", 8*60*60)
	original := time.Date(2026, 10, 28, 15, 30, 0, 0, local)
	attr := RedactLogAttr(nil, slog.Time(slog.TimeKey, original))
	if attr.Value.Kind() != slog.KindTime {
		t.Fatalf("time attribute kind = %v", attr.Value.Kind())
	}
	got := attr.Value.Time()
	if !got.Equal(original) {
		t.Fatalf("timestamp = %v, want %v", got, original)
	}
	if _, offset := got.Zone(); offset != 8*60*60 {
		t.Fatalf("timestamp offset = %d, want 28800", offset)
	}
}

func TestOpenLogFileCreatesPrivateAppendFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "dmemd.log")
	file, err := OpenLogFile(path)
	if err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	if _, err := file.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	file, err = OpenLogFile(path)
	if err != nil {
		t.Fatalf("OpenLogFile append: %v", err)
	}
	if _, err := file.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("log file mode = %04o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "first\nsecond\n" {
		t.Fatalf("log file = %q", got)
	}
	if file, err := OpenLogFile(""); err != nil || file != nil {
		t.Fatalf("OpenLogFile(\"\") = %v, %v; want nil, nil", file, err)
	}
}

func TestDefaultLogPathUsesCacheDirectory(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	if got, want := DefaultLogPath(), filepath.Join(cache, "dmemd.log"); got != want {
		t.Fatalf("DefaultLogPath() = %q, want %q", got, want)
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
