package retrieval

import (
	"strings"
	"unicode/utf8"
)

// Snippet returns readable original content around the earliest matched term.
// It never returns tokenized index text, always operates on rune boundaries and
// does not add HTML.
func Snippet(content string, terms []string) string {
	if content == "" {
		return ""
	}
	runes := []rune(content)
	if len(runes) <= DefaultSnippetRunes {
		return content
	}

	position := earliestMatch(content, terms)
	start := 0
	if position > DefaultSnippetRunes/4 {
		start = position - DefaultSnippetRunes/4
	}
	end := start + DefaultSnippetRunes
	if end > len(runes) {
		end = len(runes)
		start = end - DefaultSnippetRunes
	}
	if start < 0 {
		start = 0
	}

	var builder strings.Builder
	if start > 0 {
		builder.WriteString("…")
	}
	builder.WriteString(string(runes[start:end]))
	if end < len(runes) {
		builder.WriteString("…")
	}
	return builder.String()
}

func earliestMatch(content string, terms []string) int {
	lowerContent := strings.ToLower(content)
	earliestByte := -1
	for _, term := range terms {
		if term == "" {
			continue
		}
		index := strings.Index(lowerContent, strings.ToLower(term))
		if index >= 0 && (earliestByte < 0 || index < earliestByte) {
			earliestByte = index
		}
	}
	if earliestByte < 0 {
		return -1
	}
	// strings.ToLower can change byte length for a few scripts. Fall back to
	// the start of the content when byte and rune mapping would be ambiguous.
	if len(lowerContent) != len(content) {
		return 0
	}
	return utf8.RuneCountInString(content[:earliestByte])
}
