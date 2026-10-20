package retrieval

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// Normalize applies the single normalization rule shared by documents and
// queries: NFC first, then Unicode lowercase.
func Normalize(text string) string {
	return strings.ToLower(norm.NFC.String(text))
}

// Groups splits normalized text into query groups. Whitespace and punctuation
// are boundaries. Continuous Han characters become one token per character but
// stay in the same group, so "记忆" compiles to the adjacent phrase "记 忆".
// Letters and numbers from other scripts are kept as word tokens.
func Groups(text string) []Group {
	normalized := Normalize(text)
	groups := make([]Group, 0, 4)
	tokens := make([]string, 0, 4)
	var word []rune

	flushWord := func() {
		if len(word) == 0 {
			return
		}
		tokens = append(tokens, string(word))
		word = word[:0]
	}
	closeGroup := func() {
		flushWord()
		if len(tokens) == 0 {
			return
		}
		groups = append(groups, Group{
			Text:   strings.Join(tokens, ""),
			Tokens: append([]string(nil), tokens...),
		})
		tokens = tokens[:0]
	}

	for _, value := range normalized {
		switch {
		case unicode.IsSpace(value) || !isWordRune(value):
			closeGroup()
		case unicode.Is(unicode.Han, value):
			flushWord()
			tokens = append(tokens, string(value))
		default:
			word = append(word, value)
		}
	}
	closeGroup()
	return groups
}

// Tokens returns the flat FTS token sequence for a document or query.
func Tokens(text string) []string {
	groups := Groups(text)
	tokens := make([]string, 0, len(groups))
	for _, group := range groups {
		tokens = append(tokens, group.Tokens...)
	}
	return tokens
}

// IndexText renders normalized tokens for the FTS columns. The original title
// and content remain in the memory snapshot.
func IndexText(text string) string {
	return strings.Join(Tokens(text), " ")
}

// MatchedGroups reports which query groups occur as adjacent token sequences in
// the original title or content. It never returns tokenized single characters.
func MatchedGroups(groups []Group, title, content string) []string {
	titleTokens := Tokens(title)
	contentTokens := Tokens(content)
	seen := make(map[string]struct{}, len(groups))
	matched := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group.Tokens) == 0 {
			continue
		}
		if _, duplicate := seen[group.Text]; duplicate {
			continue
		}
		if containsSequence(titleTokens, group.Tokens) || containsSequence(contentTokens, group.Tokens) {
			seen[group.Text] = struct{}{}
			matched = append(matched, group.Text)
		}
	}
	return matched
}

// GroupTexts returns the readable normalized form of every group in order.
func GroupTexts(groups []Group) []string {
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		result = append(result, group.Text)
	}
	return result
}

func isWordRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsNumber(value) || unicode.IsMark(value)
}

func containsSequence(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		matched := true
		for offset := range needle {
			if haystack[start+offset] != needle[offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// DocumentBuilder is the production memory.DocumentBuilder. It uses the same
// Normalize/Groups rules as query compilation.
type DocumentBuilder struct{}

// NewDocumentBuilder returns the production builder for runtime assembly.
func NewDocumentBuilder() DocumentBuilder { return DocumentBuilder{} }

// BuildDocument implements memory.DocumentBuilder.
func (DocumentBuilder) BuildDocument(item memory.Memory) (memory.SearchDocument, error) {
	return memory.SearchDocument{
		MemoryID:     item.ID,
		TitleTerms:   IndexText(item.Title),
		ContentTerms: IndexText(item.Content),
	}, nil
}
