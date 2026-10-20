package retrieval

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSnippetKeepsShortContentUnchanged(t *testing.T) {
	content := "用户希望默认使用中文回答。"
	if got := Snippet(content, []string{"回答"}); got != content {
		t.Fatalf("Snippet = %q", got)
	}
}

func TestSnippetUsesReadableOriginalAroundMatch(t *testing.T) {
	content := strings.Repeat("前置说明。", 40) + "用户希望默认使用中文回答。" + strings.Repeat("后续说明。", 40)
	snippet := Snippet(content, []string{"回答"})
	if !strings.Contains(snippet, "回答") {
		t.Fatalf("snippet lost the match: %q", snippet)
	}
	if strings.Contains(snippet, "回 答") {
		t.Fatalf("snippet contains tokenized index text: %q", snippet)
	}
	if !utf8.ValidString(snippet) {
		t.Fatal("snippet is not valid UTF-8")
	}
	if count := utf8.RuneCountInString(strings.Trim(snippet, "…")); count > DefaultSnippetRunes {
		t.Fatalf("snippet runes = %d", count)
	}
}

func TestSnippetCaseInsensitiveAndFallback(t *testing.T) {
	content := strings.Repeat("x", 300) + " Alpha term " + strings.Repeat("y", 300)
	snippet := Snippet(content, []string{"alpha"})
	if !strings.Contains(snippet, "Alpha term") {
		t.Fatalf("case-insensitive snippet = %q", snippet)
	}
	fallback := Snippet(strings.Repeat("无", 300), []string{"missing"})
	if !strings.HasPrefix(fallback, "无") || !utf8.ValidString(fallback) {
		t.Fatalf("fallback snippet = %q", fallback)
	}
}
