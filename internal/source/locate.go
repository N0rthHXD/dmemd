package source

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
)

// NewEvidence builds one evidence entry for a source. It resolves the
// model-facing segment reference against the deterministic source view. An
// unresolvable reference keeps only the source association: the quote is
// cleared because it may be fabricated. A redacted source never yields a new
// quote or location copy.
func NewEvidence(src Source, reference EvidenceReference, modelName string, at time.Time) Evidence {
	at = at.UTC()
	item := Evidence{
		ID:        idgen.NewUUID(),
		SourceID:  src.ID,
		Quote:     reference.Quote,
		ModelName: modelName,
		CreatedAt: at,
	}
	if src.Redacted {
		item.Quote = ""
		item.Redacted = true
		redactedAt := at
		if src.RedactedAt != nil {
			redactedAt = src.RedactedAt.UTC()
		}
		item.RedactedAt = &redactedAt
		return item
	}
	resolved, err := BuildView(src.Content).Resolve(reference)
	if err != nil {
		// Validation owns the hard boundary. This branch only avoids persisting a
		// quote that cannot be located in the current source.
		item.Quote = ""
		return item
	}
	item.MessageID = resolved.Segment.MessageID
	item.Location = &resolved.Location
	return item
}

// NewSourceEvidence keeps a source association without a quote. It can support
// provenance and deduplication, but never automatic correction or convergence.
func NewSourceEvidence(src Source, modelName string, at time.Time) Evidence {
	return NewEvidence(src, EvidenceReference{}, modelName, at)
}

// ContainsQuote reports whether the source contains the quote verbatim inside a
// single visible segment.
func ContainsQuote(content json.RawMessage, quote string) bool {
	return BuildView(content).ContainsQuote(quote)
}

func orderedKeys(value map[string]any, preferred []string) []string {
	seen := make(map[string]struct{}, len(value))
	keys := make([]string, 0, len(value))
	for _, key := range preferred {
		if _, ok := value[key]; ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	remaining := make([]string, 0, len(value)-len(keys))
	for key := range value {
		if _, ok := seen[key]; !ok {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	return append(keys, remaining...)
}

func appendPath(path []string, part string) []string {
	result := make([]string, len(path)+1)
	copy(result, path)
	result[len(path)] = part
	return result
}
