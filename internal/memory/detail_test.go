package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestDetailAssemblesCanonicalReferences(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sourceID := "source-detail"
	src := source.Source{ID: sourceID, ScopeKey: "project:dmemd", Content: []byte(`"项目使用 SQLite。"`), CreatedAt: now}
	obs := observation.Observation{
		ID: "observation-detail", SourceID: sourceID, ScopeKey: "project:dmemd",
		Kind: observation.KindFact, Content: "项目使用 SQLite。", ContentMode: observation.ContentExtractive,
		Status: observation.StatusObserved, ExtractionVersion: "extraction-v6", CreatedAt: now, UpdatedAt: now,
	}
	item := memory.Memory{
		ID: "memory-detail", Kind: memory.KindFact, Title: "存储", Content: "项目使用 SQLite。",
		State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, Version: 1,
		ScopeKey: "project:dmemd", OriginSourceID: sourceID, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		if err := tx.InsertObservation(ctx, obs); err != nil {
			return err
		}
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, memory.Revision{
			MemoryID: item.ID, Version: 1, Operation: memory.OperationCreate,
			Snapshot: item, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.InsertMemoryObservationRefs(ctx, item.ID, 1, []string{obs.ID}); err != nil {
			return err
		}
		return tx.InsertGovernanceRelation(ctx, governance.Relation{
			ID: idgen.NewUUID(), Kind: governance.RelationSupports,
			FromType: governance.EndpointObservation, FromID: obs.ID,
			ToType: governance.EndpointMemory, ToID: item.ID, ToVersion: 1, CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	service := memory.NewService(store, sqlite.MemoryStore{Store: store}, testutil.DocumentBuilder{})
	detail, err := service.Detail(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Memory.ID != item.ID || len(detail.ObservationIDs) != 1 || detail.ObservationIDs[0] != obs.ID {
		t.Fatalf("detail = %+v", detail)
	}
	if len(detail.Relations) != 1 || detail.Relations[0].FromType != "observation" ||
		detail.Relations[0].ToType != "memory" || detail.Relations[0].Kind != "supports" {
		t.Fatalf("relations = %+v", detail.Relations)
	}
	if detail.ConflictIDs == nil || detail.DerivedDependents == nil {
		t.Fatalf("reference slices must be non-nil: %+v", detail)
	}
}

func TestDetailNotFound(t *testing.T) {
	store := testutil.NewStore(t)
	service := memory.NewService(store, sqlite.MemoryStore{Store: store}, testutil.DocumentBuilder{})
	if _, err := service.Detail(context.Background(), "missing"); err == nil {
		t.Fatal("expected not found")
	}
}
