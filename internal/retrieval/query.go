package retrieval

import "strings"

// Compile turns normalized groups into an FTS5 MATCH expression. Each group is
// quoted as a phrase, so user punctuation, quotes, parentheses, stars and words
// such as OR/NEAR can never become FTS operators. Groups from Groups contain a
// single dictionary term each; the phrase form also keeps multi-token groups
// passed by other callers safe.
func Compile(groups []Group, match Match) (string, error) {
	if len(groups) == 0 {
		return "", nil
	}
	expressions := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group.Tokens) == 0 {
			return "", ErrInvalidQuery
		}
		for _, token := range group.Tokens {
			if token == "" {
				return "", ErrInvalidQuery
			}
		}
		expressions = append(expressions, "("+quotePhrase(group.Tokens)+")")
	}
	operator := " AND "
	if normalizeMatch(match) == MatchAny {
		operator = " OR "
	}
	return strings.Join(expressions, operator), nil
}

func quotePhrase(tokens []string) string {
	phrase := strings.Join(tokens, " ")
	// Tokens never contain quotes after normalization, but escaping keeps the
	// compiler safe if a token source changes later.
	phrase = strings.ReplaceAll(phrase, `"`, `""`)
	return `"` + phrase + `"`
}

func normalizeMatch(match Match) Match {
	if match == MatchAll {
		return MatchAll
	}
	return MatchAny
}
