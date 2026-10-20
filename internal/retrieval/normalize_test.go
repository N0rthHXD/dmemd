package retrieval

import (
	"reflect"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestNormalizeUsesNFCAndLowercase(t *testing.T) {
	composed := "Caf\u00e9 \u8bb0\u5fc6"
	decomposed := "Cafe\u0301 \u8bb0\u5fc6"
	if Normalize(composed) != Normalize(decomposed) {
		t.Fatalf("normalization differs: %q vs %q", Normalize(composed), Normalize(decomposed))
	}
	if got := Normalize("HELLO \u8bb0\u5fc6"); got != "hello \u8bb0\u5fc6" {
		t.Fatalf("Normalize = %q", got)
	}
}

func TestGroupsChineseEnglishNumbersAndPunctuation(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		texts  []string
		tokens [][]string
	}{
		{
			name:   "one and two character Chinese phrases",
			input:  "记忆 回答",
			texts:  []string{"记忆", "回答"},
			tokens: [][]string{{"记", "忆"}, {"回", "答"}},
		},
		{
			name:   "mixed English and Chinese stays one group",
			input:  "AI记忆，2026",
			texts:  []string{"ai记忆", "2026"},
			tokens: [][]string{{"ai", "记", "忆"}, {"2026"}},
		},
		{
			name:   "English words and case",
			input:  "Hello, WORLD!\tNext",
			texts:  []string{"hello", "world", "next"},
			tokens: [][]string{{"hello"}, {"world"}, {"next"}},
		},
		{
			name:   "punctuation closes groups without whitespace",
			input:  "记忆,检索。回答",
			texts:  []string{"记忆", "检索", "回答"},
			tokens: [][]string{{"记", "忆"}, {"检", "索"}, {"回", "答"}},
		},
		{
			name:   "quotes stars and parentheses are boundaries",
			input:  `"记忆" OR * (NEAR)`,
			texts:  []string{"记忆", "or", "near"},
			tokens: [][]string{{"记", "忆"}, {"or"}, {"near"}},
		},
		{
			name:   "emoji is a boundary",
			input:  "记忆😀检索",
			texts:  []string{"记忆", "检索"},
			tokens: [][]string{{"记", "忆"}, {"检", "索"}},
		},
		{
			name:   "punctuation only",
			input:  "，。！？ * \" ( )",
			texts:  []string{},
			tokens: [][]string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			groups := Groups(test.input)
			gotTexts := GroupTexts(groups)
			if !reflect.DeepEqual(gotTexts, test.texts) {
				t.Fatalf("texts = %#v, want %#v", gotTexts, test.texts)
			}
			gotTokens := make([][]string, len(groups))
			for i, group := range groups {
				gotTokens[i] = group.Tokens
			}
			if !reflect.DeepEqual(gotTokens, test.tokens) {
				t.Fatalf("tokens = %#v, want %#v", gotTokens, test.tokens)
			}
		})
	}
}

func TestIndexTextKeepsChineseCharactersAsSingleTokens(t *testing.T) {
	if got := IndexText("用户希望默认使用中文回答。"); got != "用 户 希 望 默 认 使 用 中 文 回 答" {
		t.Fatalf("IndexText = %q", got)
	}
	if got := IndexText("AI 记忆"); got != "ai 记 忆" {
		t.Fatalf("IndexText mixed = %q", got)
	}
}

func TestMatchedGroupsUsesAdjacentSequences(t *testing.T) {
	groups := Groups("回答 语言 记忆")
	matched := MatchedGroups(groups, "回答格式", "默认使用中文回答，并保留记忆。")
	want := []string{"回答", "记忆"}
	if !reflect.DeepEqual(matched, want) {
		t.Fatalf("matched = %#v, want %#v", matched, want)
	}
	if matched := MatchedGroups(Groups("记忆"), "无关", "记 忆"); len(matched) != 1 {
		t.Fatalf("spaced adjacent characters should match: %#v", matched)
	}
	if matched := MatchedGroups(Groups("记忆"), "无关", "记 录 忆"); len(matched) != 0 {
		t.Fatalf("non-adjacent characters must not match the phrase: %#v", matched)
	}
}

func TestDocumentBuilderProducesNormalizedTerms(t *testing.T) {
	item := memory.Memory{
		ID:        "memory-1",
		State:     memory.StateActive,
		Title:     "Answer Language",
		Content:   "用户希望默认使用中文回答。",
		UpdatedAt: time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC),
	}
	document, err := NewDocumentBuilder().BuildDocument(item)
	if err != nil {
		t.Fatal(err)
	}
	if document.MemoryID != item.ID {
		t.Fatalf("document identity = %+v", document)
	}
	if document.TitleTerms != "answer language" {
		t.Fatalf("title terms = %q", document.TitleTerms)
	}
	if document.ContentTerms != "用 户 希 望 默 认 使 用 中 文 回 答" {
		t.Fatalf("content terms = %q", document.ContentTerms)
	}
}
