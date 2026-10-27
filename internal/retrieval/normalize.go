package retrieval

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
	"golang.org/x/text/unicode/norm"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

var (
	segmenterOnce sync.Once
	segmenter     *gse.Segmenter
)

// queryStopwords is a fixed, intentionally small list of question words,
// auxiliary words and generic terms. It is never applied to documents. Terms
// containing negation markers are always preserved even if they appear here.
var queryStopwords = buildQueryStopwords()

const chineseQueryStopwords = `
	的 了 着 过 地 得 是 在 有 和 与 及 或 而 就 都 也 还 又 很 太 更 最
	把 被 让 给 对 从 到 向 于 为 以 之 其 等
	我 你 您 他 她 它 我们 你们 他们 她们 它们
	这 那 这个 那个 这些 那些
	什么 什么时候 为什么 怎么 怎么样 怎样 如何 何时 哪里 哪儿 哪个 哪些
	谁 多少 几 是否 有什么
	吗 呢 吧 啊 呀 哦 嗯
	请 请问 可以 能 会 要 想 需要 应该 可能 已经 正在 将
	一些 一个 一种 以及 或者 但是 因为 所以 如果 虽然 然后 还是 只是 就是
	关于 对于 通过 根据 按照 进行 时候 一下 帮我 帮忙
`

const englishQueryStopwords = `
	a an the
	is are was were be been being am
	do does did doing done have has had having
	i me my mine you your yours he him his she her hers it its
	we us our ours they them their theirs
	this that these those
	what which who whom whose when where why how
	and or but if then so because as
	at by for from in into of on to with about over under
	again further once here there
	all any both each few more most other some such only own same than too very
	can could will would shall should may might must
	just now please much many
`

func buildQueryStopwords() map[string]struct{} {
	words := make(map[string]struct{}, 160)
	for _, list := range []string{chineseQueryStopwords, englishQueryStopwords} {
		for _, word := range strings.Fields(list) {
			words[word] = struct{}{}
		}
	}
	return words
}

// queryGroups is the shared preprocessing path for every text query. Documents
// retain their original terms; only queries are deduplicated and stopword-filtered.
func queryGroups(text string) []Group {
	return selectQueryTerms(dedupeGroups(Groups(text)))
}

// selectQueryTerms removes controlled question, auxiliary and generic terms.
// If no effective terms remain, callers must return empty results, not run an
// unconstrained query.
func selectQueryTerms(groups []Group) []Group {
	if len(groups) == 0 {
		return groups
	}
	selected := make([]Group, 0, len(groups))
	for _, group := range groups {
		if isQueryStopword(group) {
			continue
		}
		selected = append(selected, group)
	}
	return selected
}

func isQueryStopword(group Group) bool {
	text := strings.TrimSpace(group.Text)
	if text == "" {
		return false
	}
	if preservesNegation(text) {
		return false
	}
	_, found := queryStopwords[text]
	return found
}

func preservesNegation(text string) bool {
	switch text {
	case "not", "no", "nor", "never", "without", "none", "cannot",
		"can't", "don't", "doesn't", "didn't", "isn't", "aren't",
		"wasn't", "weren't", "won't", "wouldn't", "shouldn't", "couldn't",
		"haven't", "hasn't", "hadn't":
		return true
	}
	for _, marker := range []string{"不", "没", "无", "非", "未", "别", "勿", "否"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

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

// matchedGroupCount reports how many query groups actually occur in the title
// or content. Unlike display matched_terms it has no readable fallback, and it
// applies the same diacritic folding as the FTS tokenizer so coverage reflects
// real hits. The count is used by candidate reranking, never for display.
func matchedGroupCount(groups []Group, title, content string) int {
	titleTokens := foldedTokens(title)
	contentTokens := foldedTokens(content)
	seen := make(map[string]struct{}, len(groups))
	count := 0
	for _, group := range groups {
		if len(group.Tokens) == 0 {
			continue
		}
		if _, duplicate := seen[group.Text]; duplicate {
			continue
		}
		needle := foldTokenSequence(group.Tokens)
		if containsSequence(titleTokens, needle) || containsSequence(contentTokens, needle) {
			seen[group.Text] = struct{}{}
			count++
		}
	}
	return count
}

func foldedTokens(text string) []string {
	return foldTokenSequence(Tokens(text))
}

func foldTokenSequence(tokens []string) []string {
	folded := make([]string, len(tokens))
	for i, token := range tokens {
		folded[i] = foldDiacritics(token)
	}
	return folded
}

func foldDiacritics(text string) string {
	decomposed := norm.NFD.String(text)
	var builder strings.Builder
	builder.Grow(len(decomposed))
	for _, value := range decomposed {
		if unicode.Is(unicode.Mn, value) {
			continue
		}
		builder.WriteRune(value)
	}
	return builder.String()
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
		ContentHash:  embedding.DocumentHash(item),
	}, nil
}
