package retrieval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const (
	// maxAssociationGroups bounds how many primary fact groups are expanded in
	// one query. The primary Top-K already bounds association anchors.
	maxAssociationGroups = 50
	// maxAssociationMembersPerGroup bounds each same-fact timeline expansion.
	maxAssociationMembersPerGroup = 5
)

type associationEntry struct {
	candidate Candidate
	reasons   []string
	anchors   []string
	priority  int
}

// expandAssociations attaches bounded, explicitly linked memories to primary
// hits. It never widens scope, kind, state, assertion or time filters, and it
// never replaces a primary hit: associated items only use remaining Top-K room.
func (s *Service) expandAssociations(ctx context.Context, result *Result, groups []Group, filters Filters, topK int, diagnostics *SearchDiagnostics) error {
	budget := topK - len(result.Items)
	if budget <= 0 || len(result.Items) == 0 || s.backend == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(result.Items))
	primaryIDs := make([]string, 0, len(result.Items))
	groupIDs := make([]string, 0, len(result.Items))
	groupSeen := make(map[string]struct{}, len(result.Items))
	groupAnchors := make(map[string]string, len(result.Items))
	for _, item := range result.Items {
		seen[item.Memory.ID] = struct{}{}
		primaryIDs = append(primaryIDs, item.Memory.ID)
		if item.Cluster != nil {
			for _, memberID := range item.Cluster.MemberIDs {
				seen[memberID] = struct{}{}
			}
		}
		groupID := item.Memory.FactGroupID
		if groupID == "" {
			continue
		}
		if _, ok := groupSeen[groupID]; !ok && len(groupIDs) < maxAssociationGroups {
			groupIDs = append(groupIDs, groupID)
			groupSeen[groupID] = struct{}{}
			groupAnchors[groupID] = item.Memory.ID
		}
	}

	entries := make(map[string]*associationEntry)
	order := make([]string, 0)
	add := func(candidate Candidate, reason, anchor string, priority int) {
		if _, ok := seen[candidate.Memory.ID]; ok {
			return
		}
		if candidate.Memory.State == memory.StateDeleted || !matchesFilters(candidate.Memory, filters) {
			if diagnostics != nil {
				diagnostics.RelatedExcluded++
			}
			return
		}
		entry := entries[candidate.Memory.ID]
		if entry == nil {
			entry = &associationEntry{candidate: candidate, priority: priority}
			entries[candidate.Memory.ID] = entry
			order = append(order, candidate.Memory.ID)
		}
		entry.reasons = appendUniqueString(entry.reasons, reason)
		if anchor != "" {
			entry.anchors = appendUniqueString(entry.anchors, anchor)
		}
		if priority < entry.priority {
			entry.priority = priority
		}
	}

	// Conflict members are the highest-priority association because losing the
	// competing value would present an unresolved fact as certain.
	conflicts, err := s.backend.OpenConflictsForMemories(ctx, primaryIDs)
	if err != nil {
		return fmt.Errorf("读取关联冲突: %w", err)
	}
	if len(conflicts) > 0 {
		memberIDs := make([]string, 0, len(conflicts)*2)
		primarySet := make(map[string]struct{}, len(primaryIDs))
		for _, id := range primaryIDs {
			primarySet[id] = struct{}{}
		}
		for _, conflict := range conflicts {
			for _, member := range conflict.Members {
				memberIDs = append(memberIDs, member.MemoryID)
			}
		}
		candidates, err := s.backend.LoadSearchCandidates(ctx, memberIDs)
		if err != nil {
			return fmt.Errorf("读取关联冲突成员: %w", err)
		}
		for _, conflict := range conflicts {
			anchor := ""
			for _, member := range conflict.Members {
				if _, ok := primarySet[member.MemoryID]; ok {
					anchor = member.MemoryID
					break
				}
			}
			for _, member := range conflict.Members {
				candidate, ok := candidates[member.MemoryID]
				if !ok {
					if diagnostics != nil {
						diagnostics.RelatedExcluded++
					}
					continue
				}
				add(candidate, RelatedConflictMember, anchor, 0)
			}
		}
	}

	// Same-fact timeline members provide the other half of a preference, an
	// event qualifier or a procedure condition without expanding the source.
	if reader, ok := s.backend.(AssociationReader); ok && len(groupIDs) > 0 {
		members, err := reader.LoadFactGroupMembers(ctx, groupIDs, maxAssociationMembersPerGroup)
		if err != nil {
			return fmt.Errorf("读取同事实关联: %w", err)
		}
		for _, groupID := range groupIDs {
			for _, candidate := range members[groupID] {
				add(candidate, RelatedFactGroupMember, groupAnchors[groupID], 1)
			}
		}
	}

	if len(order) == 0 {
		return nil
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, right := entries[order[i]], entries[order[j]]
		if left.priority != right.priority {
			return left.priority < right.priority
		}
		if !left.candidate.Memory.UpdatedAt.Equal(right.candidate.Memory.UpdatedAt) {
			return left.candidate.Memory.UpdatedAt.After(right.candidate.Memory.UpdatedAt)
		}
		return order[i] < order[j]
	})
	if len(order) > budget {
		if diagnostics != nil {
			diagnostics.RelatedExcluded += len(order) - budget
		}
		order = order[:budget]
	}

	keys := make([]VersionKey, 0, len(order))
	for _, id := range order {
		memoryID := entries[id].candidate.Memory.ID
		keys = append(keys, VersionKey{MemoryID: memoryID, Version: entries[id].candidate.Memory.Version})
	}
	assemblies, err := s.backend.LoadSearchAssemblies(ctx, keys)
	if err != nil {
		return err
	}
	items := make([]Item, 0, len(order))
	for _, id := range order {
		entry := entries[id]
		key := VersionKey{MemoryID: entry.candidate.Memory.ID, Version: entry.candidate.Memory.Version}
		assembly, ok := assemblies[key]
		if !ok || !assembly.Current {
			continue
		}
		item := assembly.Memory
		matched := MatchedGroups(groups, item.Title, item.Content)
		if matched == nil {
			matched = []string{}
		}
		items = append(items, Item{
			Memory:       item,
			Score:        0,
			Snippet:      Snippet(item.Content, matched),
			MatchedTerms: matched,
			EvidenceRefs: EvidenceRefsFrom(assembly.Evidence),
			Related: &RelatedInfo{
				ReasonCodes:     append([]string(nil), entry.reasons...),
				AnchorMemoryIDs: append([]string(nil), entry.anchors...),
			},
		})
	}
	result.Items = append(result.Items, items...)
	if diagnostics != nil {
		diagnostics.RelatedExpanded = len(items)
	}
	return nil
}

// clusterDisplayItems folds exact duplicate expressions of one fact into the
// highest-ranked item. The fold requires the same fact group, kind, status,
// state, phase, validity/event time and normalized content, so different
// negation, conditions or time ranges are never silently merged.
func clusterDisplayItems(items []Item) []Item {
	if len(items) < 2 {
		return items
	}
	result := make([]Item, 0, len(items))
	index := make(map[string]int, len(items))
	for _, item := range items {
		key, ok := displayClusterKey(item.Memory)
		if !ok {
			result = append(result, item)
			continue
		}
		if position, found := index[key]; found {
			mergeDisplayItem(&result[position], item)
			continue
		}
		index[key] = len(result)
		result = append(result, item)
	}
	return result
}

func displayClusterKey(item memory.Memory) (string, bool) {
	if item.FactGroupID == "" {
		return "", false
	}
	parts := []string{
		item.FactGroupID,
		item.ScopeKey,
		string(item.Kind),
		string(item.Phase),
		string(item.State),
		string(item.AssertionStatus),
		timeKey(item.ValidFrom),
		timeKey(item.ValidTo),
		timeKey(item.EventTime),
		normalizeClusterText(item.Content),
	}
	return strings.Join(parts, "\x00"), true
}

func timeKey(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeClusterText(text string) string {
	return strings.Join(strings.Fields(Normalize(text)), " ")
}

func mergeDisplayItem(target *Item, duplicate Item) {
	if target.Cluster == nil {
		target.Cluster = &DisplayCluster{
			FactGroupID: target.Memory.FactGroupID,
			MemberIDs:   []string{target.Memory.ID},
		}
	}
	target.Cluster.MemberIDs = appendUniqueString(target.Cluster.MemberIDs, duplicate.Memory.ID)
	target.EvidenceRefs = mergeEvidenceRefs(target.EvidenceRefs, duplicate.EvidenceRefs)
	target.MatchedTerms = mergeStrings(target.MatchedTerms, duplicate.MatchedTerms)
	target.Cluster.EvidenceCount = len(target.EvidenceRefs)
}

func mergeEvidenceRefs(left, right []EvidenceRef) []EvidenceRef {
	if len(right) == 0 {
		return left
	}
	seen := make(map[string]struct{}, len(left)+len(right))
	result := make([]EvidenceRef, 0, len(left)+len(right))
	for _, ref := range append(append([]EvidenceRef(nil), left...), right...) {
		if ref.ID == "" {
			continue
		}
		if _, duplicate := seen[ref.ID]; duplicate {
			continue
		}
		seen[ref.ID] = struct{}{}
		result = append(result, ref)
	}
	return result
}

func mergeStrings(left, right []string) []string {
	result := append([]string(nil), left...)
	for _, value := range right {
		result = appendUniqueString(result, value)
	}
	return result
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// populateFilterDiagnostics performs bounded, count-only relaxations of one
// filter at a time. It retains scope, application and update boundaries, never
// returns content, and cannot alter the actual result set.
func (s *Service) populateFilterDiagnostics(ctx context.Context, groups []Group, filters Filters, diagnostics *SearchDiagnostics) {
	if s.backend == nil || len(groups) == 0 || diagnostics == nil {
		return
	}
	expression, err := Compile(groups)
	if err != nil {
		return
	}
	count := func(adjusted Filters) int {
		hits, err := s.backend.Search(ctx, Query{
			Groups:     groups,
			Expression: expression,
			Filters:    adjusted,
			TopK:       CandidateLimitMax,
		})
		if err != nil {
			return 0
		}
		return len(hits)
	}

	broad := filters
	broad.Kinds = nil
	broad.States = []memory.State{memory.StateActive, memory.StateArchived}
	broad.AssertionStatus = []memory.AssertionStatus{memory.AssertionAccepted, memory.AssertionPending}
	broad.AsOf = nil
	broad.EventAt = nil
	diagnostics.TermCandidates = count(broad)

	if len(filters.Kinds) > 0 {
		relaxed := filters
		relaxed.Kinds = nil
		diagnostics.KindFilterExcluded = nonNegative(count(relaxed) - diagnostics.KeywordCandidates)
	}
	if filters.AsOf != nil || filters.EventAt != nil {
		relaxed := filters
		relaxed.AsOf = nil
		relaxed.EventAt = nil
		diagnostics.TimeFilterExcluded = nonNegative(count(relaxed) - diagnostics.KeywordCandidates)
	}
	relaxedStates := filters
	relaxedStates.States = []memory.State{memory.StateActive, memory.StateArchived}
	diagnostics.StateFilterExcluded = nonNegative(count(relaxedStates) - diagnostics.KeywordCandidates)
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
