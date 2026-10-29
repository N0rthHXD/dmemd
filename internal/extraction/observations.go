package extraction

import (
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// BuiltObservation is one persisted-able L0 observation together with the
// code-generated evidence that supports it.
type BuiltObservation struct {
	Observation observation.Observation
	Evidence    []source.Evidence
}

// BuildStats is a source-free summary of deterministic draft conversion.
type BuildStats struct {
	Normalized int
	Pending    int
	Invalid    int
	Duplicates int
}

// BuildObservations converts model draft selections into L0 observations. The
// same source span set with the same body yields one observation; different
// bodies remain separate even when they cite the same segment. No existing
// memory, relation or conflict state is read.
func BuildObservations(src source.Source, drafts []observation.Draft, version string, now time.Time) ([]BuiltObservation, BuildStats) {
	view := source.BuildView(src.Content)
	records := make([]BuiltObservation, 0, len(drafts))
	seen := make(map[string]struct{}, len(drafts))
	stats := BuildStats{}
	for _, draft := range drafts {
		item, evidence, err := observation.BuildFromDraft(src, view, draft, version, now)
		if err != nil && item.Content == "" {
			stats.Invalid++
			continue
		}
		key := observationDedupKey(item.Content, evidence)
		if _, duplicate := seen[key]; duplicate {
			stats.Duplicates++
			continue
		}
		seen[key] = struct{}{}
		if item.ContentMode == observation.ContentNormalized {
			stats.Normalized++
		}
		if item.Status == observation.StatusPending {
			stats.Pending++
		}
		records = append(records, BuiltObservation{Observation: item, Evidence: evidence})
	}
	return records, stats
}

func observationDedupKey(content string, evidence []source.Evidence) string {
	if len(evidence) == 0 {
		return "pending\x00" + content
	}
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		ids = append(ids, item.SegmentID)
	}
	sort.Strings(ids)
	return strings.Join(ids, "\x00") + "\x01" + content
}
