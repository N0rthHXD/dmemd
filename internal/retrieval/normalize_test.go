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
			name:   "Chinese dictionary terms",
			input:  "记忆 回答",
			texts:  []string{"记忆", "回答"},
			tokens: [][]string{{"记忆"}, {"回答"}},
		},
		{
			name:   "mixed English and Chinese becomes separate terms",
			input:  "AI记忆，2026",
			texts:  []string{"ai", "记忆", "2026"},
			tokens: [][]string{{"ai"}, {"记忆"}, {"2026"}},
		},
		{
			name:   "English words and case",
			input:  "Hello, WORLD!\tNext",
			texts:  []string{"hello", "world", "next"},
			tokens: [][]string{{"hello"}, {"world"}, {"next"}},
		},
		{
			name:   "punctuation closes terms without whitespace",
			input:  "记忆,检索。回答",
			texts:  []string{"记忆", "检索", "回答"},
			tokens: [][]string{{"记忆"}, {"检索"}, {"回答"}},
		},
		{
			name:   "quotes stars and parentheses are boundaries",
			input:  `"记忆" OR * (NEAR)`,
			texts:  []string{"记忆", "or", "near"},
			tokens: [][]string{{"记忆"}, {"or"}, {"near"}},
		},
		{
			name:   "emoji is a boundary",
			input:  "记忆😀检索",
			texts:  []string{"记忆", "检索"},
			tokens: [][]string{{"记忆"}, {"检索"}},
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

func TestGroupsLongChineseQueryBecomesDictionaryTerms(t *testing.T) {
	groups := Groups("Jon是什么时候失去银行职员工作的？")
	got := GroupTexts(groups)
	want := []string{"jon", "是", "什么", "时候", "失去", "银行职员", "工作", "的"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("texts = %#v, want %#v", got, want)
	}
	for _, group := range groups {
		if len(group.Tokens) != 1 || group.Tokens[0] != group.Text {
			t.Fatalf("group is not a single dictionary term: %+v", group)
		}
	}
}

func TestIndexTextUsesDictionaryTerms(t *testing.T) {
	if got := IndexText("用户希望默认使用中文回答。"); got != "用户 希望 默认 使用 中文 回答" {
		t.Fatalf("IndexText = %q", got)
	}
	if got := IndexText("AI 记忆"); got != "ai 记忆" {
		t.Fatalf("IndexText mixed = %q", got)
	}
}

func TestSelectQueryTermsRemovesStopwords(t *testing.T) {
	groups := Groups("Jon是什么时候失去银行职员工作的？")
	got := GroupTexts(selectQueryTerms(groups))
	want := []string{"jon", "失去", "银行职员", "工作"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected terms = %#v, want %#v", got, want)
	}
}

func TestSelectQueryTermsRemovesEnglishStopwords(t *testing.T) {
	groups := Groups("How to configure the server?")
	got := GroupTexts(selectQueryTerms(groups))
	want := []string{"configure", "server"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected terms = %#v, want %#v", got, want)
	}
}

func TestSelectQueryTermsPreservesNegationNumbersAndTime(t *testing.T) {
	groups := []Group{
		{Text: "2026", Tokens: []string{"2026"}},
		{Text: "昨天", Tokens: []string{"昨天"}},
		{Text: "not", Tokens: []string{"not"}},
		{Text: "不", Tokens: []string{"不"}},
		{Text: "没", Tokens: []string{"没"}},
		{Text: "无", Tokens: []string{"无"}},
		{Text: "never", Tokens: []string{"never"}},
		{Text: "的", Tokens: []string{"的"}},
		{Text: "the", Tokens: []string{"the"}},
	}
	got := GroupTexts(selectQueryTerms(groups))
	want := []string{"2026", "昨天", "not", "不", "没", "无", "never"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected terms = %#v, want %#v", got, want)
	}
}

func TestSelectQueryTermsReturnsEmptyWhenAllTermsRemoved(t *testing.T) {
	for _, query := range []string{"", "，。！？", "的 是 什么", "what is the"} {
		if got := selectQueryTerms(Groups(query)); len(got) != 0 {
			t.Fatalf("selected terms for %q = %#v, want empty", query, got)
		}
	}
}

func TestQueryGroupsUsesSharedPreprocessing(t *testing.T) {
	for _, query := range []string{"我对什么过敏？", "过敏 过敏", "过敏"} {
		if got := GroupTexts(queryGroups(query)); !reflect.DeepEqual(got, []string{"过敏"}) {
			t.Fatalf("queryGroups(%q) = %#v", query, got)
		}
	}
}

func TestMatchedGroupCountUsesFoldedTerms(t *testing.T) {
	if got := matchedGroupCount(Groups("cafe"), "Café", ""); got != 1 {
		t.Fatalf("folded title match count = %d, want 1", got)
	}
	if got := matchedGroupCount(Groups("cafe 2026"), "Café", "记录 2026"); got != 2 {
		t.Fatalf("title and content match count = %d, want 2", got)
	}
	if got := matchedGroupCount(Groups("cafe"), "无关", "无关"); got != 0 {
		t.Fatalf("unmatched count = %d, want 0", got)
	}
}

func TestMatchedGroupsUsesDictionaryTerms(t *testing.T) {
	groups := Groups("回答 语言 记忆")
	matched := MatchedGroups(groups, "回答格式", "默认使用中文回答，并保留记忆。")
	want := []string{"回答", "记忆"}
	if !reflect.DeepEqual(matched, want) {
		t.Fatalf("matched = %#v, want %#v", matched, want)
	}
	if matched := MatchedGroups(Groups("记忆"), "中文记忆检索", "无关"); !reflect.DeepEqual(matched, []string{"记忆"}) {
		t.Fatalf("dictionary term should match: %#v", matched)
	}
	if matched := MatchedGroups(Groups("记"), "中文记忆检索", "无关"); len(matched) != 0 {
		t.Fatalf("substring of a dictionary term must not match: %#v", matched)
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
	if document.ContentTerms != "用户 希望 默认 使用 中文 回答" {
		t.Fatalf("content terms = %q", document.ContentTerms)
	}
}
