package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
// versions arrive. It is deliberately deterministic and never calls a model.
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
				if errors.Is(err, memory.ErrConflictStale) || errors.Is(err, memory.ErrInvalidResolution) {
					applies = false
				} else {
					return err
				}
			}
		}
		if applies {
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

// deterministicResolution closes only conflicts whose current member set alone
// proves the outcome. No temporal or fact-group metadata exists in the current
// view, so ambiguous contradictions stay open.
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

	base := memory.ResolveInput{
		ConflictID:     conflict.ID,
		Revision:       conflict.Revision,
		MemberVersions: versions,
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
	if len(activeAccepted) == 1 && pending == 0 {
		base.Action = memory.ResolutionChoose
		base.MemberIDs = []string{activeAccepted[0].item.ID}
		base.Reason = "governance:auto_review:single_active"
		return base, true, nil
	}
	if len(activeAccepted) != len(states) {
		return memory.ResolveInput{}, false, nil
	}
	for _, state := range activeAccepted[1:] {
		if !sameReviewContent(activeAccepted[0].item.Content, state.item.Content) {
			return memory.ResolveInput{}, false, nil
		}
	}
	base.Action = memory.ResolutionChoose
	base.MemberIDs = []string{activeAccepted[0].item.ID}
	base.Reason = "governance:auto_review:converged"
	return base, true, nil
}

func sameReviewContent(left, right string) bool {
	return normalizeReviewContent(left) == normalizeReviewContent(right)
}

func normalizeReviewContent(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
