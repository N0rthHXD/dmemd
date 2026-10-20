package retrieval

import (
	"errors"
	"strings"
	"testing"
)

func TestCompileGroupsAsQuotedPhrases(t *testing.T) {
	groups := Groups("回答 语言")
	all, err := Compile(groups, MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	if all != `("回 答") AND ("语 言")` {
		t.Fatalf("all expression = %q", all)
	}
	anyExpression, err := Compile(groups, MatchAny)
	if err != nil {
		t.Fatal(err)
	}
	if anyExpression != `("回 答") OR ("语 言")` {
		t.Fatalf("any expression = %q", anyExpression)
	}

	one, err := Compile(Groups("记"), MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	if one != `("记")` {
		t.Fatalf("one-character expression = %q", one)
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
	if !strings.Contains(expression, `("记 忆")`) {
		t.Fatalf("Chinese phrase missing: %q", expression)
	}
	if !strings.Contains(expression, `("or")`) || !strings.Contains(expression, `("near")`) {
		t.Fatalf("operator words were not treated literally: %q", expression)
	}
	if strings.Contains(expression, "(*)") || strings.Contains(expression, "((") {
		t.Fatalf("raw punctuation leaked into expression: %q", expression)
	}
}

func TestNormalizeMatchDefaultsToAll(t *testing.T) {
	if normalizeMatch("unknown") != MatchAll {
		t.Fatalf("unknown match did not default to all")
	}
	if normalizeMatch(MatchAny) != MatchAny {
		t.Fatalf("any match changed")
	}
}
