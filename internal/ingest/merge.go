package ingest

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	reasonAdd           = "ingest:add"
	reasonUpdate        = "ingest:update"
	reasonMergeEvidence = "ingest:merge_evidence"
)

type mergeResult struct {
	MemoryIDs []string
	Decisions []mergeDecision
}

type mergeDecision struct {
	Action   string
	MemoryID string
}

// normalizeCandidates applies provider-contract defaults and validates target
// references against the related context. The provider never chooses a version,
// evidence ID or persisted state; those are resolved in the write transaction.
func normalizeCandidates(src source.Source, related []memory.Memory, candidates []Candidate, modelName string, now time.Time) []Candidate {
	relatedIDs := make(map[string]struct{}, len(related))
	for _, item := range related {
		relatedIDs[item.ID] = struct{}{}
	}
	fallbackObserved := src.CreatedAt
	if fallbackObserved.IsZero() {
		fallbackObserved = now
	}
	fallbackObserved = fallbackObserved.UTC()

	result := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Action = normalizeAction(candidate.Action)
		if candidate.Action == ActionSkip {
			result = append(result, Candidate{Action: ActionSkip, Reason: candidate.Reason})
			continue
		}
		if strings.TrimSpace(candidate.Content) == "" {
			continue
		}
		candidate.Kind = normalizeKind(candidate.Kind)
		candidate.Title = strings.TrimSpace(candidate.Title)
		if candidate.Title == "" {
			candidate.Title = defaultTitle(candidate.Content)
		}
		if candidate.ObservedAt == nil || candidate.ObservedAt.IsZero() {
			observed := fallbackObserved
			candidate.ObservedAt = &observed
		} else {
			observed := candidate.ObservedAt.UTC()
			candidate.ObservedAt = &observed
		}
		if candidate.Action == ActionUpdate || candidate.Action == ActionMergeEvidence {
			if _, ok := relatedIDs[candidate.TargetMemoryID]; !ok || candidate.TargetMemoryID == "" {
				candidate.Action = ActionAdd
				candidate.TargetMemoryID = ""
			}
		}
		candidate.Evidence = buildCandidateEvidence(src, candidate.Evidence, modelName, now)
		result = append(result, candidate)
	}
	return result
}

func normalizeAction(action Action) Action {
	switch action {
	case ActionAdd, ActionUpdate, ActionMergeEvidence, ActionSkip:
		return action
	default:
		return ActionAdd
	}
}

func normalizeKind(kind memory.Kind) memory.Kind {
	switch kind {
	case memory.KindFact, memory.KindPreference, memory.KindEvent, memory.KindProcedure:
		return kind
	default:
		return memory.KindFact
	}
}

func defaultTitle(content string) string {
	trimmed := strings.TrimSpace(content)
	runes := []rune(trimmed)
	if len(runes) > 40 {
		runes = runes[:40]
	}
	return strings.TrimSpace(string(runes))
}

func buildCandidateEvidence(src source.Source, proposed []source.Evidence, modelName string, now time.Time) []source.Evidence {
	result := make([]source.Evidence, 0, len(proposed))
	seen := make(map[string]struct{}, len(proposed))
	for _, item := range proposed {
		// Evidence IDs and source relationships are service-owned. A model may
		// propose a message and quote only.
		evidence := source.NewEvidence(src, item.MessageID, item.Quote, modelName, now)
		key := evidenceKey(evidence)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, evidence)
	}
	return result
}

func evidenceKey(item source.Evidence) string {
	field := ""
	start, end := 0, 0
	if item.Location != nil {
		field = item.Location.Field
		start = item.Location.Start
		end = item.Location.End
	}
	return strings.Join([]string{
		item.SourceID, item.MessageID, item.Quote, field,
		strconv.Itoa(start), strconv.Itoa(end),
	}, "\x00")
}

// applyCandidates performs merge decisions inside the caller's short
// transaction. Every candidate observes the actual persisted state, not the
// related-context snapshot from before the model call.
func applyCandidates(ctx context.Context, tx Tx, writer memory.Writer, src source.Source, related []memory.Memory, candidates []Candidate) (mergeResult, error) {
	known := make(map[string]memory.Memory, len(related))
	active := make([]memory.Memory, 0, len(related))
	for _, item := range related {
		current, err := tx.GetMemory(ctx, item.ID)
		if errors.Is(err, memory.ErrNotFound) {
			continue
		}
		if err != nil {
			return mergeResult{}, err
		}
		known[current.ID] = current
		if current.State == memory.StateActive {
			active = append(active, current)
		}
	}

	evidence, err := tx.ListEvidenceBySource(ctx, src.ID)
	if err != nil {
		return mergeResult{}, err
	}
	evidenceIndex := make(map[string]source.Evidence, len(evidence))
	for _, item := range evidence {
		evidenceIndex[evidenceKey(item)] = item
	}

	result := mergeResult{MemoryIDs: []string{}}
	seenResult := make(map[string]struct{})
	addResult := func(id string) {
		if id == "" {
			return
		}
		if _, duplicate := seenResult[id]; duplicate {
			return
		}
		seenResult[id] = struct{}{}
		result.MemoryIDs = append(result.MemoryIDs, id)
	}

	for _, candidate := range candidates {
		action := candidate.Action
		if action == ActionSkip {
			result.Decisions = append(result.Decisions, mergeDecision{Action: "skip"})
			continue
		}

		var target *memory.Memory
		if action == ActionUpdate {
			target = knownTarget(known, candidate.TargetMemoryID)
			if target == nil {
				// The target was purged after context selection. A correction
				// with no current record is a new memory.
				action = ActionAdd
			} else if target.State != memory.StateActive {
				// Background work never reactivates or rewrites archived,
				// deleted or otherwise inactive records.
				result.Decisions = append(result.Decisions, mergeDecision{Action: "skip-inactive-target"})
				continue
			} else if candidate.ObservedAt != nil && candidate.ObservedAt.Before(target.ObservedAt) {
				// Older content must not overwrite a clearly newer fact. Treat
				// it as an independent historical event instead.
				action = ActionAdd
			} else if sameContent(*target, candidate) {
				// The model said update but the fact text is unchanged. This is
				// an evidence-only change: merge the complete evidence set instead
				// of dropping the existing support.
				action = ActionMergeEvidence
			}
		}
		if action == ActionMergeEvidence {
			target = knownTarget(known, candidate.TargetMemoryID)
			if target == nil || target.State != memory.StateActive {
				result.Decisions = append(result.Decisions, mergeDecision{Action: "skip-inactive-target"})
				continue
			}
		}

		operation := reasonAdd
		switch action {
		case ActionUpdate:
			resolved := resolveEvidence(candidate.Evidence, evidenceIndex)
			reason := candidate.Reason
			if reason == "" {
				reason = reasonUpdate
			}
			revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
				MemoryID:   target.ID,
				Kind:       candidate.Kind,
				Title:      candidate.Title,
				Content:    candidate.Content,
				ObservedAt: observedValue(candidate.ObservedAt),
				Evidence:   resolved,
				Operation:  memory.OperationUpdate,
				Reason:     reason,
			})
			if err != nil {
				return mergeResult{}, err
			}
			known[revised.ID] = revised
			replaceActive(&active, revised)
			if revised.Version != target.Version {
				addResult(revised.ID)
				result.Decisions = append(result.Decisions, mergeDecision{Action: "update", MemoryID: revised.ID})
			} else {
				result.Decisions = append(result.Decisions, mergeDecision{Action: "skip-unchanged"})
			}
		case ActionMergeEvidence:
			currentEvidence, err := tx.ListRevisionEvidence(ctx, target.ID, target.Version)
			if err != nil {
				return mergeResult{}, err
			}
			resolved := resolveEvidence(candidate.Evidence, evidenceIndex)
			if !hasNewEvidence(currentEvidence, resolved) {
				result.Decisions = append(result.Decisions, mergeDecision{Action: "skip-duplicate-evidence"})
				continue
			}
			reason := candidate.Reason
			if reason == "" {
				reason = reasonMergeEvidence
			}
			revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
				MemoryID:  target.ID,
				Evidence:  resolved,
				Operation: memory.OperationAddEvidence,
				Reason:    reason,
			})
			if err != nil {
				return mergeResult{}, err
			}
			known[revised.ID] = revised
			replaceActive(&active, revised)
			if revised.Version != target.Version {
				addResult(revised.ID)
				result.Decisions = append(result.Decisions, mergeDecision{Action: "merge_evidence", MemoryID: revised.ID})
			} else {
				result.Decisions = append(result.Decisions, mergeDecision{Action: "skip-unchanged"})
			}
		case ActionAdd:
			duplicate := findDuplicate(active, candidate)
			if duplicate != nil {
				currentEvidence, err := tx.ListRevisionEvidence(ctx, duplicate.ID, duplicate.Version)
				if err != nil {
					return mergeResult{}, err
				}
				resolved := resolveEvidence(candidate.Evidence, evidenceIndex)
				if !hasNewEvidence(currentEvidence, resolved) {
					result.Decisions = append(result.Decisions, mergeDecision{Action: "skip-duplicate"})
					continue
				}
				reason := candidate.Reason
				if reason == "" {
					reason = reasonMergeEvidence
				}
				revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
					MemoryID:  duplicate.ID,
					Evidence:  resolved,
					Operation: memory.OperationAddEvidence,
					Reason:    reason,
				})
				if err != nil {
					return mergeResult{}, err
				}
				known[revised.ID] = revised
				replaceActive(&active, revised)
				addResult(revised.ID)
				result.Decisions = append(result.Decisions, mergeDecision{Action: "merge-duplicate", MemoryID: revised.ID})
				continue
			}

			resolved := resolveEvidence(candidate.Evidence, evidenceIndex)
			reason := candidate.Reason
			if reason == "" {
				reason = operation
			}
			created, err := writer.Create(ctx, tx, memory.CreateInput{
				Kind:           candidate.Kind,
				Title:          candidate.Title,
				Content:        candidate.Content,
				ObservedAt:     observedValue(candidate.ObservedAt),
				CreatedByApp:   src.AppID,
				OriginSourceID: src.ID,
				Evidence:       resolved,
				Reason:         reason,
			})
			if err != nil {
				return mergeResult{}, err
			}
			known[created.ID] = created
			active = append(active, created)
			addResult(created.ID)
			result.Decisions = append(result.Decisions, mergeDecision{Action: "add", MemoryID: created.ID})
		}
	}
	return result, nil
}

func knownTarget(known map[string]memory.Memory, id string) *memory.Memory {
	item, ok := known[id]
	if !ok {
		return nil
	}
	copied := item
	return &copied
}

func replaceActive(active *[]memory.Memory, item memory.Memory) {
	for index := range *active {
		if (*active)[index].ID == item.ID {
			(*active)[index] = item
			return
		}
	}
	if item.State == memory.StateActive {
		*active = append(*active, item)
	}
}

func findDuplicate(active []memory.Memory, candidate Candidate) *memory.Memory {
	for index := range active {
		item := &active[index]
		if item.State != memory.StateActive || item.Kind != candidate.Kind {
			continue
		}
		if sameContent(*item, candidate) {
			return item
		}
	}
	return nil
}

func sameContent(item memory.Memory, candidate Candidate) bool {
	return retrieval.Normalize(strings.TrimSpace(item.Content)) ==
		retrieval.Normalize(strings.TrimSpace(candidate.Content))
}

func resolveEvidence(incoming []source.Evidence, index map[string]source.Evidence) []source.Evidence {
	result := make([]source.Evidence, 0, len(incoming))
	seen := make(map[string]struct{}, len(incoming))
	for _, item := range incoming {
		key := evidenceKey(item)
		if existing, ok := index[key]; ok {
			item = existing
		} else {
			index[key] = item
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	return result
}

func hasNewEvidence(current, incoming []source.Evidence) bool {
	known := make(map[string]struct{}, len(current))
	for _, item := range current {
		known[item.ID] = struct{}{}
	}
	for _, item := range incoming {
		if _, ok := known[item.ID]; !ok {
			return true
		}
	}
	return false
}

func observedValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}
