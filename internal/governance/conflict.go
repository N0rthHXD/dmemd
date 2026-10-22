package governance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

// ConflictResolver applies one version-checked adjudication. The memory service
// implements it; governance and explicit management share the same transaction
// path.
type ConflictResolver interface {
	ResolveInTx(ctx context.Context, tx memory.Tx, input memory.ResolveInput) (memory.Conflict, error)
}

// ConflictHandler re-examines open conflicts when new evidence or related
// versions arrive. It is deliberately deterministic: it only closes conflicts
// whose current member set already proves the outcome, and never calls a model.
// Complex contradictions stay open and isolated from aggregation and skills.
type ConflictHandler struct {
	store    Store
	resolver ConflictResolver
	now      func() time.Time
}

type conflictMemberState struct {
	item memory.Memory
}

// ConflictOption customizes the conflict handler.
type ConflictOption func(*ConflictHandler)

// WithConflictClock overrides the handler clock for deterministic tests.
func WithConflictClock(now func() time.Time) ConflictOption {
	return func(handler *ConflictHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

// NewConflictHandler assembles the bounded conflict reviewer.
func NewConflictHandler(store Store, resolver ConflictResolver, options ...ConflictOption) *ConflictHandler {
	handler := &ConflictHandler{store: store, resolver: resolver, now: time.Now}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// Process implements Handler.
func (h *ConflictHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindReviewConflict {
		return fmt.Errorf("conflict handler received kind %s", job.Kind)
	}
	if h.store == nil || h.resolver == nil {
		return errors.New("governance: conflict handler dependencies not configured")
	}
	conflictID, ok := task.ParseConflictKey(job.TargetKey)
	if !ok {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false,
			Err: fmt.Errorf("invalid conflict target %q", job.TargetKey)}
	}
	now := h.now().UTC()
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		conflict, err := tx.GetConflict(ctx, conflictID)
		if errors.Is(err, memory.ErrConflictNotFound) {
			_, cancelErr := tx.CancelJob(ctx, task.KindReviewConflict, job.TargetKey, now)
			return cancelErr
		}
		if err != nil {
			return err
		}
		if conflict.Status != memory.ConflictOpen {
			ok, err := tx.FinishJob(ctx, job.ID, job.Revision, now)
			if err != nil {
				return err
			}
			if !ok {
				return ErrRevisionChanged
			}
			return nil
		}

		input, applies, err := deterministicResolution(ctx, tx, conflict)
		if err != nil {
			return err
		}
		if applies {
			if _, err := h.resolver.ResolveInTx(ctx, tx, input); err != nil {
				// A rule that no longer holds is not a durable failure: leave
				// the conflict open and wait for the next input change.
				if errors.Is(err, memory.ErrConflictStale) || errors.Is(err, memory.ErrInvalidResolution) {
					applies = false
				} else {
					return err
				}
			}
		}
		if applies {
			// ResolveInTx cancels the review target in the same transaction.
			return nil
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

// deterministicResolution returns a resolution only when the current member set
// alone is sufficient. No model judgement, receive order or occurrence count is
// used as proof.
func deterministicResolution(ctx context.Context, tx Tx, conflict memory.Conflict) (memory.ResolveInput, bool, error) {
	states := make([]conflictMemberState, 0, len(conflict.Members))
	versions := make([]memory.ConflictMember, 0, len(conflict.Members))
	for _, member := range conflict.Members {
		item, err := tx.GetMemory(ctx, member.MemoryID)
		if errors.Is(err, memory.ErrNotFound) {
			return memory.ResolveInput{}, false, nil
		}
		if err != nil {
			return memory.ResolveInput{}, false, err
		}
		states = append(states, conflictMemberState{item: item})
		versions = append(versions, memory.ConflictMember{MemoryID: item.ID, Version: item.Version})
	}
	if len(states) < 2 {
		return memory.ResolveInput{}, false, nil
	}

	activeAccepted := make([]conflictMemberState, 0, len(states))
	pending := 0
	for _, state := range states {
		if state.item.State != memory.StateActive {
			continue
		}
		switch state.item.AssertionStatus {
		case memory.AssertionAccepted:
			activeAccepted = append(activeAccepted, state)
		case memory.AssertionPending:
			pending++
		}
	}

	base := memory.ResolveInput{
		ConflictID:     conflict.ID,
		Revision:       conflict.Revision,
		MemberVersions: versions,
	}
	// A stale conflict is safe to close when exactly one accepted active member
	// remains and no pending alternative is still under consideration.
	if len(activeAccepted) == 1 && pending == 0 {
		chosen := activeAccepted[0].item
		if allOverlap(chosen, states) {
			base.Action = memory.ResolutionChoose
			base.MemberIDs = []string{chosen.ID}
			base.Reason = "governance:auto_review:single_active"
			return base, true, nil
		}
	}
	if len(activeAccepted) != len(states) {
		return memory.ResolveInput{}, false, nil
	}

	// Different revisions may converge on identical content. Treat that as the
	// same assertion rather than inventing a winner.
	converged := true
	for _, state := range activeAccepted[1:] {
		if !sameReviewContent(activeAccepted[0].item.Content, state.item.Content) {
			converged = false
			break
		}
	}
	if converged && allOverlap(activeAccepted[0].item, states) {
		base.Action = memory.ResolutionChoose
		base.MemberIDs = []string{activeAccepted[0].item.ID}
		base.Reason = "governance:auto_review:converged"
		return base, true, nil
	}

	// Existing, fully known and pairwise disjoint intervals prove a timeline
	// without re-cutting it. Unknown or overlapping bounds remain isolated.
	if intervalsDisjoint(activeAccepted) {
		sort.Slice(activeAccepted, func(i, j int) bool {
			return activeAccepted[i].item.ValidFrom.Before(*activeAccepted[j].item.ValidFrom)
		})
		segments := make([]memory.TimelineSegment, 0, len(activeAccepted))
		for _, state := range activeAccepted {
			segments = append(segments, memory.TimelineSegment{
				MemoryID:  state.item.ID,
				Content:   state.item.Content,
				ValidFrom: state.item.ValidFrom,
				ValidTo:   state.item.ValidTo,
			})
		}
		base.Action = memory.ResolutionTimeline
		base.Timeline = segments
		base.Reason = "governance:auto_review:disjoint_timeline"
		return base, true, nil
	}
	return memory.ResolveInput{}, false, nil
}

func allOverlap(chosen memory.Memory, states []conflictMemberState) bool {
	for _, state := range states {
		if state.item.ID == chosen.ID {
			continue
		}
		if !memory.Overlaps(chosen.ValidFrom, chosen.ValidTo, state.item.ValidFrom, state.item.ValidTo) {
			return false
		}
	}
	return true
}

func intervalsDisjoint(states []conflictMemberState) bool {
	for index, state := range states {
		if state.item.ValidFrom == nil || state.item.ValidTo == nil {
			return false
		}
		for other := index + 1; other < len(states); other++ {
			if memory.Overlaps(state.item.ValidFrom, state.item.ValidTo,
				states[other].item.ValidFrom, states[other].item.ValidTo) {
				return false
			}
		}
	}
	return true
}

func sameReviewContent(left, right string) bool {
	return normalizeReviewContent(left) == normalizeReviewContent(right)
}

func normalizeReviewContent(value string) string {
	runes := []rune(strings.ToLower(strings.TrimSpace(value)))
	var builder strings.Builder
	builder.Grow(len(runes))
	for index := 0; index < len(runes); index++ {
		current := runes[index]
		if !unicode.IsSpace(current) {
			builder.WriteRune(current)
			continue
		}
		previous := rune(0)
		for before := index - 1; before >= 0; before-- {
			if !unicode.IsSpace(runes[before]) {
				previous = runes[before]
				break
			}
		}
		next := rune(0)
		for after := index + 1; after < len(runes); after++ {
			if !unicode.IsSpace(runes[after]) {
				next = runes[after]
				break
			}
		}
		if unicode.Is(unicode.Han, previous) || unicode.Is(unicode.Han, next) {
			// Chinese text is commonly written with or without separators.
			// Remove whitespace adjacent to Han characters, but keep word
			// boundaries between non-Han tokens.
			continue
		}
		builder.WriteRune(' ')
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}
