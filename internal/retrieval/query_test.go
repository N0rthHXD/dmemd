package retrieval

import (
	"errors"
	"strings"
	"testing"
)

func TestCompileTermsAsQuotedPhrases(t *testing.T) {
	groups := Groups("回答 语言")
	anyExpression, err := Compile(groups)
	if err != nil {
		t.Fatal(err)
	}
	if anyExpression != `("回答") OR ("语言")` {
		t.Fatalf("any expression = %q", anyExpression)
	}

	one, err := Compile(Groups("记"))
	if err != nil {
		t.Fatal(err)
	}
	if one != `("记")` {
		t.Fatalf("one-character expression = %q", one)
	}

	long, err := Compile(Groups("记忆检索"))
	if err != nil {
		t.Fatal(err)
	}
	if long != `("记忆") OR ("检索")` {
		t.Fatalf("long Chinese expression = %q", long)
	}
}

func TestCompileEmptyAndInvalidGroups(t *testing.T) {
	expression, err := Compile(nil)
	if err != nil || expression != "" {
		t.Fatalf("empty compile = %q, %v", expression, err)
	}
	if _, err := Compile([]Group{{Text: "bad"}}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid group error = %v", err)
	}
}

func TestCompileQuotesUserOperators(t *testing.T) {
	groups := Groups(`记忆" OR * (NEAR)`)
	expression, err := Compile(groups)
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
