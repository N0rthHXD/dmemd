package aggregation

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/observation"
)

// JobTx is the minimal task-registration capability used by write-path
// triggers. The governance transaction already implements it.
type JobTx interface {
	UpsertJob(ctx context.Context, input task.UpsertInput) (task.Job, bool, error)
}

// staleTargetTx is the optional artifact capability used to take an outdated
// local aggregate out of default recall immediately. The governance write
// transaction implements it even though its declared interface is narrower.
type staleTargetTx interface {
	GetArtifactByTarget(ctx context.Context, kind artifact.Kind, targetKey string) (artifact.DerivedMemory, error)
	UpdateArtifact(ctx context.Context, item artifact.DerivedMemory) error
	DeleteArtifactSearchDocument(ctx context.Context, artifactID string) error
}

// TriggerConfig controls deterministic L1 target generation after an
// observation is governed.
type TriggerConfig struct {
	BucketSize time.Duration
	Debounce   time.Duration
	Now        func() time.Time
}

// Trigger registers the L1 target affected by one governed observation. It
// never calls a model and never reads canonical memory.
type Trigger struct {
	bucketSize time.Duration
	debounce   time.Duration
	now        func() time.Time
}

// NewTrigger assembles the write-path aggregation trigger.
func NewTrigger(config TriggerConfig) *Trigger {
	bucketSize := config.BucketSize
	if bucketSize <= 0 {
		bucketSize = artifact.DefaultL1BucketSize
	}
	debounce := config.Debounce
	if debounce < 0 {
		debounce = 0
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Trigger{bucketSize: bucketSize, debounce: debounce, now: now}
}

// ObservationGoverned registers the L1 bucket containing one observation. The
// observation status is part of the input hash so a pending-to-observed change
// re-evaluates the same target without creating a second job.
func (t *Trigger) ObservationGoverned(ctx context.Context, tx JobTx, item observation.Observation) error {
	if t == nil || tx == nil {
		return nil
	}
	scopeKey := strings.TrimSpace(item.ScopeKey)
	if scopeKey == "" {
		return nil
	}
	switch item.Status {
	case observation.StatusDeleted, observation.StatusRetracted:
		return nil
	}
	at := item.CreatedAt
	if at.IsZero() {
		at = t.now().UTC()
	}
	target := L1TargetForTime(scopeKey, at, t.bucketSize)
	now := t.now().UTC()
	if err := markTargetStale(ctx, tx, target, now); err != nil {
		return err
	}
	next := now.Add(t.debounce)
	_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
		Kind:      task.KindAggregateL1,
		TargetKey: target.TargetKey(),
		InputHash: task.InputHash("l1-observation", item.ID, string(item.Kind),
			string(item.Status), item.UpdatedAt.UTC().Format(time.RFC3339Nano)),
		NextAttemptAt: &next,
		Now:           now,
	})
	return err
}

func markTargetStale(ctx context.Context, tx JobTx, target artifact.Target, now time.Time) error {
	marker, ok := tx.(staleTargetTx)
	if !ok {
		return nil
	}
	item, err := marker.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
	if errors.Is(err, artifact.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if item.State == artifact.StateActive {
		item.State = artifact.StateStale
		item.UpdatedAt = now
		if err := marker.UpdateArtifact(ctx, item); err != nil {
			return err
		}
	}
	return marker.DeleteArtifactSearchDocument(ctx, item.ID)
}
