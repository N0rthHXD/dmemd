package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func newSearchFactGroup(t *testing.T, f *searchFixture, input memory.FactGroupInput) memory.FactGroup {
	t.Helper()
	group, err := memory.NewFactGroup(input, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertFactGroup(ctx, group)
	}); err != nil {
		t.Fatal(err)
	}
	return group
}

func TestBoundedAssociationRecallAndDisplayCluster(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	group := newSearchFactGroup(t, f, memory.FactGroupInput{
		ScopeKey: "project:association", Subject: "用户", Predicate: "饮食偏好",
		Cardinality: memory.CardinalityMultiple,
	})
	firstEvidence := f.evidence("用户喜欢低糖饮食。")
	first := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "饮食偏好", Content: "用户喜欢低糖饮食。",
		FactGroupID: group.ID, ScopeKey: "project:association",
		Evidence: []source.Evidence{firstEvidence},
	})
	f.advance(time.Minute)
	secondEvidence := f.evidence("用户喜欢低糖饮食。")
	duplicate := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "饮食偏好", Content: "用户喜欢低糖饮食。",
		FactGroupID: group.ID, ScopeKey: "project:association",
		Evidence: []source.Evidence{secondEvidence},
	})
	f.advance(time.Minute)
	related := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "饮食偏好", Content: "用户不喜欢甜食。",
		FactGroupID: group.ID, ScopeKey: "project:association",
	})
	f.advance(time.Minute)
	archived := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "饮食偏好", Content: "用户曾经喜欢甜食。",
		FactGroupID: group.ID, ScopeKey: "project:association",
	})
	if _, err := f.memories.UpdateState(ctx, archived.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "饮食偏好", Content: "用户喜欢低糖饮食。",
		FactGroupID: group.ID, ScopeKey: "project:other",
	})

	result := f.run(t, retrieval.Request{Query: "低糖", TopK: 10, Filters: retrieval.Filters{ScopeKey: "project:association"}})
	if len(result.Items) != 2 {
		t.Fatalf("bounded association result = %+v", searchIDs(result))
	}
	if result.Items[0].Memory.ID != first.ID && result.Items[0].Memory.ID != duplicate.ID {
		t.Fatalf("primary hit missing: %+v", searchIDs(result))
	}
	cluster := result.Items[0].Cluster
	if cluster == nil || len(cluster.MemberIDs) != 2 || cluster.EvidenceCount != 2 {
		t.Fatalf("display cluster = %+v", cluster)
	}
	associated := result.Items[1]
	if associated.Memory.ID != related.ID || associated.Related == nil ||
		!contains(associated.Related.ReasonCodes, retrieval.RelatedFactGroupMember) ||
		len(associated.Related.AnchorMemoryIDs) != 1 {
		t.Fatalf("associated item = %+v", associated)
	}
	for _, item := range result.Items {
		if item.Memory.ID == archived.ID {
			t.Fatalf("archived member ignored state filter: %+v", searchIDs(result))
		}
		if item.Memory.ScopeKey == "project:other" {
			t.Fatalf("cross-scope member entered association: %+v", searchIDs(result))
		}
	}

	limited := f.run(t, retrieval.Request{Query: "低糖", TopK: 1, Filters: retrieval.Filters{ScopeKey: "project:association"}})
	if len(limited.Items) != 1 {
		t.Fatalf("top_k did not bound associated context: %+v", searchIDs(limited))
	}
}

func TestRecallExplainReportsFilterStageCounts(t *testing.T) {
	f := newSearchFixture(t)
	f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "回答语言", Content: "用户希望使用中文回答。",
	})
	f.advance(time.Minute)
	f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "回答事实", Content: "中文回答使用关键词检索。",
	})
	f.advance(time.Minute)
	future := f.clock.Add(time.Hour)
	f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "未来回答", Content: "中文回答未来生效。",
		ValidFrom: &future, ValidFromBasis: memory.BasisExplicit,
	})
	f.advance(time.Minute)
	archived := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "归档回答", Content: "中文回答归档。",
	})
	if _, err := f.memories.UpdateState(context.Background(), archived.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}

	result, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query:            "中文回答",
		Layers:           []retrieval.Layer{retrieval.LayerAtomic},
		LayersSpecified:  true,
		Filters:          retrieval.Filters{Kinds: []memory.Kind{memory.KindFact}, AsOf: timePtr(f.clock)},
		Explain:          true,
		MaxContextTokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Diagnostics == nil {
		t.Fatal("explain=true did not return diagnostics")
	}
	diagnostics := result.Diagnostics
	if diagnostics.KeywordCandidates != 1 {
		t.Fatalf("keyword candidates = %d", diagnostics.KeywordCandidates)
	}
	if diagnostics.TermCandidates < 4 {
		t.Fatalf("term candidates = %d", diagnostics.TermCandidates)
	}
	if diagnostics.KindFilterExcluded < 1 {
		t.Fatalf("kind excluded = %d", diagnostics.KindFilterExcluded)
	}
	if diagnostics.TimeFilterExcluded < 1 {
		t.Fatalf("time excluded = %d", diagnostics.TimeFilterExcluded)
	}
	if diagnostics.StateFilterExcluded < 1 {
		t.Fatalf("state excluded = %d", diagnostics.StateFilterExcluded)
	}
	if len(result.Memories) != 0 || diagnostics.ContextBudgetTruncated == 0 {
		t.Fatalf("context budget diagnostics = %+v, memories=%d", diagnostics, len(result.Memories))
	}
	if result.Memories == nil {
		t.Fatal("memories partition must remain a non-nil empty array")
	}
}
