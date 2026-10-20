package integration_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

var p4BaseTime = time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)

type p4Fixture struct {
	store    *sqlite.Store
	clock    time.Time
	memories *memory.Service
	search   *retrieval.Service
	source   source.Source
	logs     bytes.Buffer
}

func newP4Fixture(t *testing.T) *p4Fixture {
	t.Helper()
	store := testutil.NewStore(t)
	fixture := &p4Fixture{
		store: store,
		clock: p4BaseTime,
		source: source.Source{
			ID:        "source-p4",
			AppID:     "desktop-assistant",
			Content:   []byte(`{"messages":[{"id":"m1","content":"用户希望默认使用中文回答。"}]}`),
			CreatedAt: p4BaseTime,
		},
	}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertSource(ctx, fixture.source)
	}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return fixture.clock }
	fixture.memories = memory.NewService(store, sqlite.MemoryStore{Store: store},
		retrieval.NewDocumentBuilder(), memory.WithClock(now))
	fixture.search = retrieval.NewService(store, retrieval.NewDocumentBuilder(),
		retrieval.WithLogger(slog.New(slog.NewTextHandler(&fixture.logs, nil))))
	return fixture
}

func (f *p4Fixture) advance(delta time.Duration) { f.clock = f.clock.Add(delta) }

func (f *p4Fixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
	t.Helper()
	if input.ObservedAt.IsZero() {
		input.ObservedAt = f.clock
	}
	if input.OriginSourceID == "" {
		input.OriginSourceID = f.source.ID
	}
	var created memory.Memory
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = f.memories.Create(ctx, tx, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func (f *p4Fixture) revise(t *testing.T, input memory.ReviseInput) memory.Memory {
	t.Helper()
	var revised memory.Memory
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		revised, err = f.memories.Revise(ctx, tx, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return revised
}

func (f *p4Fixture) evidence(quote string) source.Evidence {
	return source.NewEvidence(f.source, "", quote, "test-model", f.clock)
}

func (f *p4Fixture) run(t *testing.T, request retrieval.Request) retrieval.Result {
	t.Helper()
	result, err := f.search.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestP4ChineseKeywordSearchContract(t *testing.T) {
	f := newP4Fixture(t)
	answerEvidence := f.evidence("用户希望默认使用中文回答。")
	answer := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "回答语言", Content: "用户希望默认使用中文回答。",
		CreatedByApp: "desktop", Evidence: []source.Evidence{answerEvidence},
	})
	f.advance(time.Minute)
	memoryResult := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "记忆检索", Content: "中文记忆检索使用关键词。",
		CreatedByApp: "notes",
	})
	f.advance(time.Minute)
	meeting := f.create(t, memory.CreateInput{
		Kind: memory.KindEvent, Title: "Meeting Notes", Content: "Discussed AI memory architecture.",
		CreatedByApp: "notes",
	})
	f.advance(time.Minute)
	procedure := f.create(t, memory.CreateInput{
		Kind: memory.KindProcedure, Title: "部署步骤", Content: "打开终端，运行服务。",
		CreatedByApp: "ops",
	})
	f.advance(time.Minute)
	format := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "回答格式", Content: "回答应简洁。",
		CreatedByApp: "desktop",
	})
	f.advance(time.Minute)
	aiMemory := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "AI记忆", Content: "AI记忆服务保留上下文。",
		CreatedByApp: "desktop",
	})
	f.advance(time.Minute)
	longContent := strings.Repeat("前置说明。", 60) + "关键回答保持连续原文。" + strings.Repeat("后续说明。", 60)
	longSnippet := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "长摘要", Content: longContent, CreatedByApp: "desktop",
	})

	if result := f.run(t, retrieval.Request{Query: "回答"}); !p4SameMembers(p4IDs(result), answer.ID, format.ID, longSnippet.ID) {
		t.Fatalf("two-character Chinese query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "终"}); !slices.Equal(p4IDs(result), p4Set(procedure.ID)) {
		t.Fatalf("one-character Chinese query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "终端"}); !slices.Equal(p4IDs(result), p4Set(procedure.ID)) {
		t.Fatalf("two-character content query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "部署步骤"}); !slices.Equal(p4IDs(result), p4Set(procedure.ID)) {
		t.Fatalf("long Chinese title query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆检索"}); !slices.Equal(p4IDs(result), p4Set(memoryResult.ID)) {
		t.Fatalf("phrase query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆"}); !p4SameMembers(p4IDs(result), memoryResult.ID, aiMemory.ID) {
		t.Fatalf("two-character query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "AI记忆"}); !slices.Equal(p4IDs(result), p4Set(aiMemory.ID)) {
		t.Fatalf("mixed query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "MEMORY"}); !slices.Equal(p4IDs(result), p4Set(meeting.ID)) {
		t.Fatalf("case-insensitive English query = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "终端，运行"}); !slices.Equal(p4IDs(result), p4Set(procedure.ID)) {
		t.Fatalf("punctuation boundary query = %+v", p4IDs(result))
	}

	all := f.run(t, retrieval.Request{Query: "回答 语言"})
	if !slices.Equal(p4IDs(all), p4Set(answer.ID)) {
		t.Fatalf("match=all result = %+v", p4IDs(all))
	}
	anyResult := f.run(t, retrieval.Request{Query: "回答 语言", Match: retrieval.MatchAny})
	if !p4SameMembers(p4IDs(anyResult), answer.ID, format.ID, longSnippet.ID) {
		t.Fatalf("match=any result = %+v", p4IDs(anyResult))
	}
	for _, item := range anyResult.Items {
		switch item.Memory.ID {
		case answer.ID:
			if strings.Join(item.MatchedTerms, ",") != "回答,语言" {
				t.Fatalf("answer matched_terms = %#v", item.MatchedTerms)
			}
		case format.ID:
			if strings.Join(item.MatchedTerms, ",") != "回答" {
				t.Fatalf("format matched_terms = %#v", item.MatchedTerms)
			}
		}
		if item.Explanation != nil {
			t.Fatalf("explanation returned without explain=true: %+v", item.Explanation)
		}
	}
	if len(answerEvidence.ID) == 0 {
		t.Fatal("test evidence missing")
	}
	answerItem := p4Item(t, all, answer.ID)
	if len(answerItem.EvidenceRefs) != 1 || answerItem.EvidenceRefs[0].ID != answerEvidence.ID ||
		answerItem.EvidenceRefs[0].SourceID != f.source.ID {
		t.Fatalf("evidence_refs = %+v", answerItem.EvidenceRefs)
	}
	if answerItem.Score <= 0 {
		t.Fatalf("score = %v, want -bm25 > 0", answerItem.Score)
	}
	explained := f.run(t, retrieval.Request{Query: "回答", Explain: true})
	if item := p4Item(t, explained, answer.ID); item.Explanation == nil ||
		item.Explanation.Ranking != "bm25" || item.Explanation.FieldWeights != [2]int{4, 1} {
		t.Fatalf("explanation = %+v", item.Explanation)
	}

	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		Kinds: []memory.Kind{memory.KindPreference},
	}}); !slices.Equal(p4IDs(result), p4Set(answer.ID)) {
		t.Fatalf("kind filter = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆", Filters: retrieval.Filters{
		CreatedByApp: "notes",
	}}); !slices.Equal(p4IDs(result), p4Set(memoryResult.ID)) {
		t.Fatalf("app filter = %+v", p4IDs(result))
	}
	after := p4BaseTime.Add(3*time.Minute + 30*time.Second)
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		UpdatedAfter: &after,
	}}); !p4SameMembers(p4IDs(result), format.ID, longSnippet.ID) {
		t.Fatalf("updated_after filter = %+v", p4IDs(result))
	}
	before := p4BaseTime.Add(30 * time.Second)
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		UpdatedBefore: &before,
	}}); !slices.Equal(p4IDs(result), p4Set(answer.ID)) {
		t.Fatalf("updated_before filter = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆", TopK: 1}); len(result.Items) != 1 {
		t.Fatalf("top_k = %d", len(result.Items))
	}
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		Kinds: []memory.Kind{"unknown"},
	}}); len(result.Items) != 0 {
		t.Fatalf("all-unknown kinds returned %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		States: []memory.State{"unknown"},
	}}); len(result.Items) != 0 {
		t.Fatalf("all-unknown states returned %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		States: []memory.State{memory.StateActive, memory.StateDeleted},
	}}); !p4SameMembers(p4IDs(result), answer.ID, format.ID, longSnippet.ID) {
		t.Fatalf("deleted state was not removed from active search: %+v", p4IDs(result))
	}

	longResult := f.run(t, retrieval.Request{Query: "关键回答"})
	if !slices.Equal(p4IDs(longResult), p4Set(longSnippet.ID)) {
		t.Fatalf("long snippet result = %+v", p4IDs(longResult))
	}
	item := p4Item(t, longResult, longSnippet.ID)
	if !strings.Contains(item.Snippet, "关键回答") || strings.Contains(item.Snippet, "关 键") {
		t.Fatalf("snippet = %q", item.Snippet)
	}
	if len([]rune(strings.Trim(item.Snippet, "…"))) > retrieval.DefaultSnippetRunes {
		t.Fatalf("snippet rune count = %d", len([]rune(item.Snippet)))
	}

	for _, query := range []string{"", " ，。！？ ", "*", `记忆" OR * (NEAR)`} {
		if result := f.run(t, retrieval.Request{Query: query}); len(result.Items) != 0 {
			t.Fatalf("query %q returned %+v", query, p4IDs(result))
		}
	}
}

func TestP4NormalizationEquivalentInputs(t *testing.T) {
	f := newP4Fixture(t)
	created := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "Café 2026",
		Content:      "记住 Cafe\u0301 偏好与 AI 记忆。",
		CreatedByApp: "desktop",
	})
	for _, query := range []string{"café", "Cafe\u0301", "CAFÉ", "cafe", "2026", "AI记忆"} {
		result := f.run(t, retrieval.Request{Query: query})
		if !slices.Equal(p4IDs(result), p4Set(created.ID)) {
			t.Fatalf("query %q result = %+v", query, p4IDs(result))
		}
		if len(result.Items[0].MatchedTerms) == 0 {
			t.Fatalf("query %q returned no matched_terms", query)
		}
	}
}

func TestP4SearchLifecycleCorrectionAndRollback(t *testing.T) {
	f := newP4Fixture(t)
	ctx := context.Background()
	oldEvidence := f.evidence("旧的记忆内容")
	created := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "旧标题", Content: "旧的记忆内容",
		CreatedByApp: "desktop", Evidence: []source.Evidence{oldEvidence},
	})
	if result := f.run(t, retrieval.Request{Query: "旧"}); !slices.Equal(p4IDs(result), p4Set(created.ID)) {
		t.Fatalf("initial search = %+v", p4IDs(result))
	}

	f.advance(time.Minute)
	revised := f.revise(t, memory.ReviseInput{
		MemoryID: created.ID, Kind: memory.KindFact, Title: "新标题", Content: "新的搜索内容",
		ObservedAt: f.clock, Operation: memory.OperationUpdate, Reason: "correction",
	})
	if result := f.run(t, retrieval.Request{Query: "旧"}); len(result.Items) != 0 {
		t.Fatalf("corrected old text still matched: %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新"}); !slices.Equal(p4IDs(result), p4Set(created.ID)) {
		t.Fatalf("corrected text search = %+v", p4IDs(result))
	}

	f.advance(time.Minute)
	addedEvidence := f.evidence("新的搜索内容")
	revised = f.revise(t, memory.ReviseInput{
		MemoryID: revised.ID, Evidence: []source.Evidence{addedEvidence},
		Operation: memory.OperationAddEvidence,
	})
	result := f.run(t, retrieval.Request{Query: "新"})
	item := p4Item(t, result, created.ID)
	if len(item.EvidenceRefs) != 1 || item.EvidenceRefs[0].ID != addedEvidence.ID {
		t.Fatalf("evidence refs after add_evidence = %+v", item.EvidenceRefs)
	}

	f.advance(time.Minute)
	archived, err := f.memories.UpdateState(ctx, created.ID, memory.StateArchived)
	if err != nil {
		t.Fatal(err)
	}
	if result := f.run(t, retrieval.Request{Query: "新"}); len(result.Items) != 0 {
		t.Fatalf("archived memory returned by default: %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新", Filters: retrieval.Filters{
		States: []memory.State{memory.StateArchived},
	}}); !slices.Equal(p4IDs(result), p4Set(created.ID)) {
		t.Fatalf("explicit archived search = %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新", Filters: retrieval.Filters{
		States: []memory.State{memory.StateActive, memory.StateArchived},
	}}); !slices.Equal(p4IDs(result), p4Set(created.ID)) {
		t.Fatalf("active+archived search = %+v", p4IDs(result))
	}

	f.advance(time.Minute)
	deleted, err := f.memories.Delete(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.State != memory.StateDeleted {
		t.Fatalf("deleted = %+v", deleted)
	}
	for _, states := range [][]memory.State{
		{memory.StateActive},
		{memory.StateArchived},
		{memory.StateActive, memory.StateArchived},
		{memory.StateDeleted},
		{memory.StateActive, memory.StateDeleted},
	} {
		if result := f.run(t, retrieval.Request{Query: "新", Filters: retrieval.Filters{States: states}}); len(result.Items) != 0 {
			t.Fatalf("deleted memory returned for states %v: %+v", states, p4IDs(result))
		}
	}

	f.advance(time.Minute)
	restored, err := f.memories.Restore(ctx, memory.RestoreInput{MemoryID: created.ID, Version: 1, Reason: "restore"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != archived.Version+2 {
		t.Fatalf("restored version = %d", restored.Version)
	}
	if result := f.run(t, retrieval.Request{Query: "旧"}); !slices.Equal(p4IDs(result), p4Set(created.ID)) {
		t.Fatalf("restored memory not searchable: %+v", p4IDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新"}); len(result.Items) != 0 {
		t.Fatalf("current text should be the restored old content: %+v", p4IDs(result))
	}

	if err := f.memories.Purge(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	for _, states := range [][]memory.State{{memory.StateActive}, {memory.StateArchived}} {
		if result := f.run(t, retrieval.Request{Query: "旧", Filters: retrieval.Filters{States: states}}); len(result.Items) != 0 {
			t.Fatalf("purged memory searchable: %+v", p4IDs(result))
		}
	}
	if _, err := f.memories.Get(ctx, created.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("purged memory read error = %v", err)
	}

	failure := errors.New("injected transaction failure")
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := f.memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindFact, Title: "幽灵", Content: "幽灵索引不应存在",
			ObservedAt: f.clock, OriginSourceID: f.source.ID,
		}); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("rollback error = %v", err)
	}
	if result := f.run(t, retrieval.Request{Query: "幽灵"}); len(result.Items) != 0 {
		t.Fatalf("rolled-back memory left a search hit: %+v", p4IDs(result))
	}
}

func TestP4IndexRebuildMatchesSearchResults(t *testing.T) {
	f := newP4Fixture(t)
	ctx := context.Background()
	active := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "重建回答", Content: "重建后的回答仍可搜索。",
	})
	f.advance(time.Minute)
	archived := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "重建归档", Content: "归档记忆保留索引。",
	})
	if _, err := f.memories.UpdateState(ctx, archived.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	deleted := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "重建删除", Content: "删除记忆不进入索引。",
	})
	if _, err := f.memories.Delete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}

	requests := []retrieval.Request{
		{Query: "重建", TopK: 20},
		{Query: "重建", Filters: retrieval.Filters{States: []memory.State{memory.StateArchived}}},
		{Query: "删除", Filters: retrieval.Filters{States: []memory.State{memory.StateActive, memory.StateArchived}}},
	}
	before := make([][]string, len(requests))
	for i, request := range requests {
		before[i] = p4IDs(f.run(t, request))
	}
	if before[0] == nil || len(before[0]) != 1 || before[0][0] != active.ID {
		t.Fatalf("before rebuild active = %+v", before[0])
	}
	if len(before[1]) != 1 || before[1][0] != archived.ID {
		t.Fatalf("before rebuild archived = %+v", before[1])
	}

	result, err := f.search.RebuildIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Indexed != 2 {
		t.Fatalf("indexed = %d, want active+archived", result.Indexed)
	}
	for i, request := range requests {
		after := p4IDs(f.run(t, request))
		if strings.Join(after, ",") != strings.Join(before[i], ",") {
			t.Fatalf("request %d changed after rebuild: before=%v after=%v", i, before[i], after)
		}
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "search index rebuilt") || !strings.Contains(logs, "indexed=2") {
		t.Fatalf("rebuild log = %q", logs)
	}
	if strings.Contains(logs, "重建回答") || strings.Contains(logs, "归档记忆") {
		t.Fatalf("rebuild log leaked memory text: %q", logs)
	}

	var failedLogs bytes.Buffer
	failing := retrieval.NewService(failingIndexBackend{Store: f.store}, retrieval.NewDocumentBuilder(),
		retrieval.WithLogger(slog.New(slog.NewTextHandler(&failedLogs, nil))))
	failedResult, err := failing.RebuildIndex(ctx)
	if !errors.Is(err, errP4IndexFailure) {
		t.Fatalf("failing rebuild error = %v", err)
	}
	if failedResult.Indexed != 0 {
		t.Fatalf("failed rebuild reported indexed=%d", failedResult.Indexed)
	}
	if !strings.Contains(failedLogs.String(), "search index rebuild failed") {
		t.Fatalf("failure log = %q", failedLogs.String())
	}
}

func TestP4RelatedActiveContext(t *testing.T) {
	f := newP4Fixture(t)
	ctx := context.Background()
	answer := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "回答语言", Content: "用户希望默认使用中文回答。",
	})
	f.advance(time.Minute)
	notes := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "记忆检索", Content: "中文记忆检索使用关键词。",
	})
	f.advance(time.Minute)
	archived := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "归档回答", Content: "归档回答不应进入上下文。",
	})
	if _, err := f.memories.UpdateState(ctx, archived.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	deleted := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "删除回答", Content: "删除回答不应进入上下文。",
	})
	if _, err := f.memories.Delete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	unrelated := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "部署步骤", Content: "打开终端运行服务。",
	})

	related, err := f.search.RelatedActive(ctx, "回答 记忆", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p4SameMembers(p4MemoryIDs(related), answer.ID, notes.ID) {
		t.Fatalf("related active = %+v", p4MemoryIDs(related))
	}
	for _, item := range related {
		if item.ID == archived.ID || item.ID == deleted.ID || item.ID == unrelated.ID {
			t.Fatalf("ineligible memory entered context: %+v", item)
		}
	}

	related, err = f.search.RelatedActive(ctx, "回答", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 1 || related[0].ID != answer.ID {
		t.Fatalf("limited related = %+v", p4MemoryIDs(related))
	}
	related, err = f.search.RelatedActive(ctx, "完全不存在的关键词", 0)
	if err != nil {
		t.Fatal(err)
	}
	if related == nil || len(related) != 0 {
		t.Fatalf("missing related context = %#v", related)
	}
}

var errP4IndexFailure = errors.New("injected index rebuild failure")

type failingIndexBackend struct {
	*sqlite.Store
}

func (f failingIndexBackend) RebuildSearchDocuments(context.Context, memory.DocumentBuilder) (int, error) {
	return 0, errP4IndexFailure
}

func p4IDs(result retrieval.Result) []string {
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.Memory.ID)
	}
	return ids
}

func p4SameMembers(ids []string, want ...string) bool {
	if len(ids) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

func p4Set(ids ...string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func p4Item(t *testing.T, result retrieval.Result, id string) retrieval.Item {
	t.Helper()
	for _, item := range result.Items {
		if item.Memory.ID == id {
			return item
		}
	}
	t.Fatalf("memory %s missing from result %+v", id, p4IDs(result))
	return retrieval.Item{}
}

func p4MemoryIDs(items []memory.Memory) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
