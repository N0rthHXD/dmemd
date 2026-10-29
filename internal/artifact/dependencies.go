package artifact

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// DependencyTx is the transaction-scoped derived-dependency capability. Input
// selection and counting, exact-edge linking, dependent enumeration and
// immediate stale marking all belong to the same short transaction as the
// derived revision.
type DependencyTx interface {
	ListDerivedInputs(ctx context.Context, target Target, limit int) ([]InputRef, bool, error)
	CountDerivedInputs(ctx context.Context, target Target) (int, error)
	// CountDerivedInputCandidates reports lower-layer objects that could become
	// eligible after a rebuild. It includes stale lower-layer versions, so an
	// upper layer is marked stale instead of disabled while its input layer is
	// still scheduled to rebuild.
	CountDerivedInputCandidates(ctx context.Context, target Target) (int, error)
	LinkDerivedDependencies(ctx context.Context, derivedID string, version int, refs []DependencyRef) error
	ListDependents(ctx context.Context, input DependencyRef) ([]DerivedRef, error)
	MarkDerivedStale(ctx context.Context, derivedID, reason string) error
	MarkDerivedDisabled(ctx context.Context, derivedID, reason string) error
}

// InputRef is one bounded lower-layer input selected for aggregation. The model
// receives only readable content and a kind; the service keeps the concrete
// type, identifier and version for dependency linking.
type InputRef struct {
	Type    DependencyType `json:"type"`
	ID      string         `json:"id"`
	Version int            `json:"version"`
	Kind    string         `json:"kind,omitempty"`
	Title   string         `json:"title,omitempty"`
	Content string         `json:"content"`
}

// Dependency converts one selected input into the exact edge stored for the
// generated revision.
func (r InputRef) Dependency() DependencyRef {
	return DependencyRef{Type: r.Type, ID: r.ID, Version: r.Version, Required: true}
}

// DerivedRef is one derived object that consumes an input. Version is the
// current usable revision of the dependent object, not the historical revision
// that first matched the edge.
type DerivedRef struct {
	ID        string `json:"id"`
	Version   int    `json:"version"`
	Layer     Layer  `json:"layer,omitempty"`
	Kind      Kind   `json:"kind,omitempty"`
	TargetKey string `json:"target_key,omitempty"`
	ScopeKey  string `json:"scope_key,omitempty"`
	Topic     string `json:"topic,omitempty"`
	State     State  `json:"state,omitempty"`
}

// DependencyRefs converts selected inputs into stable, de-duplicated dependency
// edges.
func DependencyRefs(inputs []InputRef) []DependencyRef {
	refs := make([]DependencyRef, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input.ID) == "" || !input.Type.Known() {
			continue
		}
		refs = append(refs, input.Dependency())
	}
	return normalizeDependencyRefs(refs)
}

// ExperienceDependencies converts recorded run identifiers into experience
// dependency edges.
func ExperienceDependencies(ids []string) []DependencyRef {
	refs := make([]DependencyRef, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		refs = append(refs, DependencyRef{Type: DependencyExperience, ID: id, Required: true})
	}
	return normalizeDependencyRefs(refs)
}

// MergeDependencies combines several edge lists and removes exact duplicates.
func MergeDependencies(groups ...[]DependencyRef) []DependencyRef {
	refs := make([]DependencyRef, 0)
	for _, group := range groups {
		refs = append(refs, group...)
	}
	return normalizeDependencyRefs(refs)
}

func normalizeDependencyRefs(refs []DependencyRef) []DependencyRef {
	seen := make(map[string]struct{}, len(refs))
	result := make([]DependencyRef, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.ID) == "" || !ref.Type.Known() {
			continue
		}
		key := strings.Join([]string{
			string(ref.Type), ref.ID, strconv.Itoa(ref.Version), boolString(ref.Required),
		}, "\x00")
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ref)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Version < result[j].Version
	})
	return result
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
