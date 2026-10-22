package retrieval

import (
	"errors"
	"strings"
	"testing"
)

func TestCompileTermsAsQuotedPhrases(t *testing.T) {
	groups := Groups("回答 语言")
	all, err := Compile(groups, MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	if all != `("回答") AND ("语言")` {
		t.Fatalf("all expression = %q", all)
	}
	anyExpression, err := Compile(groups, MatchAny)
	if err != nil {
		t.Fatal(err)
	}
	if anyExpression != `("回答") OR ("语言")` {
		t.Fatalf("any expression = %q", anyExpression)
	}

	one, err := Compile(Groups("记"), MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	if one != `("记")` {
		t.Fatalf("one-character expression = %q", one)
	}

	long, err := Compile(Groups("记忆检索"), MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	if long != `("记忆") AND ("检索")` {
		t.Fatalf("long Chinese expression = %q", long)
	}
}

func TestCompileEmptyAndInvalidGroups(t *testing.T) {
	expression, err := Compile(nil, MatchAll)
	if err != nil || expression != "" {
		t.Fatalf("empty compile = %q, %v", expression, err)
	}
	if _, err := Compile([]Group{{Text: "bad"}}, MatchAll); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid group error = %v", err)
	}
}

func TestCompileQuotesUserOperators(t *testing.T) {
	groups := Groups(`记忆" OR * (NEAR)`)
	expression, err := Compile(groups, MatchAny)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expression, `("记忆")`) {
		t.Fatalf("Chinese term missing: %q", expression)
	}
	if !strings.Contains(expression, `("or")`) || !strings.Contains(expression, `("near")`) {
		t.Fatalf("operator words were not treated literally: %q", expression)
	}
	if strings.Contains(expression, "(*)") || strings.Contains(expression, "((") {
		t.Fatalf("raw punctuation leaked into expression: %q", expression)
	}
}

func TestNormalizeMatchDefaultsToAny(t *testing.T) {
	tests := []struct {
		match Match
		want  Match
	}{
		{match: "", want: MatchAny},
		{match: MatchAny, want: MatchAny},
		{match: "unknown", want: MatchAny},
		{match: MatchAll, want: MatchAll},
	}
	for _, test := range tests {
		if got := normalizeMatch(test.match); got != test.want {
			t.Fatalf("normalizeMatch(%q) = %q, want %q", test.match, got, test.want)
		}
	}
}

func TestCompileDefaultsToAny(t *testing.T) {
	expression, err := Compile(Groups("回答 语言"), "")
	if err != nil {
		t.Fatal(err)
	}
	if expression != `("回答") OR ("语言")` {
		t.Fatalf("default expression = %q, want OR", expression)
	}
}
