package retrieval

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
	"golang.org/x/text/unicode/norm"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

var (
	segmenterOnce sync.Once
	segmenter     *gse.Segmenter
)

func dictionarySegmenter() *gse.Segmenter {
	segmenterOnce.Do(func() {
		loaded := &gse.Segmenter{SkipSubSeg: true}
		if err := loaded.LoadDictEmbed("zh"); err != nil {
			panic("retrieval: load gse dictionary: " + err.Error())
		}
		segmenter = loaded
	})
	return segmenter
}

// Normalize applies the single normalization rule shared by documents and
// queries: NFC first, then Unicode lowercase.
func Normalize(text string) string {
	return strings.ToLower(norm.NFC.String(text))
}

// Groups splits normalized text into one group per dictionary term. Whitespace
// and punctuation are boundaries; within each boundary-delimited word run the
// embedded gse dictionary is used in accurate mode. Every term therefore has
// the same representation on the document and query paths.
func Groups(text string) []Group {
	normalized := Normalize(text)
	groups := make([]Group, 0, 8)
	var word []rune

	flushWord := func() {
		if len(word) == 0 {
			return
		}
		for _, token := range dictionarySegmenter().Cut(string(word)) {
			if !isWordToken(token) {
				continue
			}
			groups = append(groups, Group{
				Text:   token,
				Tokens: []string{token},
			})
		}
		word = word[:0]
	}

	for _, value := range normalized {
		if unicode.IsSpace(value) || !isWordRune(value) {
			flushWord()
			continue
		}
		word = append(word, value)
	}
	flushWord()
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

// MatchedGroups reports which query groups occur in the original title or
// content using the same dictionary segmentation as indexing. It never returns
// internal index fragments.
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

func isWordToken(token string) bool {
	if token == "" {
		return false
	}
	for _, value := range token {
		if !isWordRune(value) {
			return false
		}
	}
	return true
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
// normalization and dictionary segmentation rules as query compilation.
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
