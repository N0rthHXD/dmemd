package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

func TestArtifactAndTargetListsAreStableAndBounded(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.CreateArtifact(ctx, artifact.Artifact{
			ID: "artifact-active", Kind: artifact.KindSkill,
			TargetKey: task.SkillKey("user", "active"), ScopeKey: "user", Topic: "active",
			CurrentVersion: 1, State: artifact.StateActive, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.CreateArtifact(ctx, artifact.Artifact{
			ID: "artifact-disabled", Kind: artifact.KindSkill,
			TargetKey: task.SkillKey("user", "disabled"), ScopeKey: "user", Topic: "disabled",
			CurrentVersion: 2, State: artifact.StateDisabled, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind: task.KindBuildSkill, TargetKey: task.SkillKey("user", "queued"),
			InputHash: "queued", NextAttemptAt: &now, Now: now,
		}); err != nil {
			return err
		}
		return tx.UpsertTargetEvaluation(ctx, artifact.TargetEvaluation{
			Kind: artifact.KindSkill, TargetKey: task.SkillKey("user", "missing"),
			ScopeKey: "user", Topic: "missing", Status: artifact.TargetStatusNoEligibleInput,
			ReasonCode: "no_procedure_input", EvaluatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	artifacts, err := store.ListArtifacts(ctx, artifact.ListQuery{ScopeKey: "user", State: artifact.StateDisabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts.Items) != 1 || artifacts.Items[0].ID != "artifact-disabled" {
		t.Fatalf("artifacts = %+v", artifacts)
	}

	first, err := store.ListTargets(ctx, artifact.TargetQuery{ScopeKey: "user", Kind: artifact.KindSkill, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := store.ListTargets(ctx, artifact.TargetQuery{
		ScopeKey: "user", Kind: artifact.KindSkill, Limit: 2, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
	all := append(append([]artifact.TargetRecord{}, first.Items...), second.Items...)
	statuses := map[artifact.TargetStatus]int{}
	for _, item := range all {
		statuses[item.Status]++
	}
	if statuses[artifact.TargetStatusActive] != 1 || statuses[artifact.TargetStatusDisabled] != 1 ||
		statuses[artifact.TargetStatusQueued] != 1 || statuses[artifact.TargetStatusNoEligibleInput] != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}

	filtered, err := store.ListTargets(ctx, artifact.TargetQuery{
		ScopeKey: "user", Kind: artifact.KindSkill, Status: artifact.TargetStatusQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Artifact != nil ||
		filtered.Items[0].Job == nil || filtered.Items[0].Job.Status != task.StatusQueued {
		t.Fatalf("queued targets = %+v", filtered)
	}
}
