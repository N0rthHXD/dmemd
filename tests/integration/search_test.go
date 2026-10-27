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

var searchBaseTime = time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)

type searchFixture struct {
	store    *sqlite.Store
	clock    time.Time
	memories *memory.Service
	search   *retrieval.Service
	source   source.Source
	logs     bytes.Buffer
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	store := testutil.NewStore(t)
	fixture := &searchFixture{
		store: store,
		clock: searchBaseTime,
		source: source.Source{
			ID:        "source-search",
			AppID:     "desktop-assistant",
			Content:   []byte(`{"messages":[{"id":"m1","content":"用户希望默认使用中文回答。"}]}`),
			CreatedAt: searchBaseTime,
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

func (f *searchFixture) advance(delta time.Duration) { f.clock = f.clock.Add(delta) }

func (f *searchFixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
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

func (f *searchFixture) revise(t *testing.T, input memory.ReviseInput) memory.Memory {
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

func (f *searchFixture) evidence(quote string) source.Evidence {
	return source.NewEvidence(f.source, source.EvidenceReference{Quote: quote}, "test-model", f.clock)
}

func (f *searchFixture) run(t *testing.T, request retrieval.Request) retrieval.Result {
	t.Helper()
	result, err := f.search.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestChineseKeywordSearchContract(t *testing.T) {
	f := newSearchFixture(t)
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

	if result := f.run(t, retrieval.Request{Query: "回答"}); !sameMembers(searchIDs(result), answer.ID, format.ID, longSnippet.ID) {
		t.Fatalf("two-character Chinese query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "终端"}); !slices.Equal(searchIDs(result), searchSet(procedure.ID)) {
		t.Fatalf("two-character content query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "运行服务"}); !sameMembers(searchIDs(result), procedure.ID, aiMemory.ID) {
		t.Fatalf("multi-term Chinese query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "部署步骤"}); !slices.Equal(searchIDs(result), searchSet(procedure.ID)) {
		t.Fatalf("long Chinese title query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆检索"}); !sameMembers(searchIDs(result), memoryResult.ID, aiMemory.ID) {
		t.Fatalf("dictionary-term query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆"}); !sameMembers(searchIDs(result), memoryResult.ID, aiMemory.ID) {
		t.Fatalf("two-character query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "AI记忆"}); !sameMembers(searchIDs(result), aiMemory.ID, memoryResult.ID, meeting.ID) {
		t.Fatalf("mixed query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "MEMORY"}); !slices.Equal(searchIDs(result), searchSet(meeting.ID)) {
		t.Fatalf("case-insensitive English query = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "终端，运行"}); !slices.Equal(searchIDs(result), searchSet(procedure.ID)) {
		t.Fatalf("punctuation boundary query = %+v", searchIDs(result))
	}

	anyResult := f.run(t, retrieval.Request{Query: "回答 语言"})
	if !sameMembers(searchIDs(anyResult), answer.ID, format.ID, longSnippet.ID) {
		t.Fatalf("unified any result = %+v", searchIDs(anyResult))
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
	answerItem := searchItem(t, anyResult, answer.ID)
	if len(answerItem.EvidenceRefs) != 1 || answerItem.EvidenceRefs[0].ID != answerEvidence.ID ||
		answerItem.EvidenceRefs[0].SourceID != f.source.ID {
		t.Fatalf("evidence_refs = %+v", answerItem.EvidenceRefs)
	}
	if answerItem.Score <= 0 {
		t.Fatalf("score = %v, want -bm25 > 0", answerItem.Score)
	}
	explained := f.run(t, retrieval.Request{Query: "回答", Explain: true})
	if item := searchItem(t, explained, answer.ID); item.Explanation == nil ||
		item.Explanation.Ranking != "bm25" || item.Explanation.FieldWeights != [2]int{4, 1} {
		t.Fatalf("explanation = %+v", item.Explanation)
	}

	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		Kinds: []memory.Kind{memory.KindPreference},
	}}); !slices.Equal(searchIDs(result), searchSet(answer.ID)) {
		t.Fatalf("kind filter = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆", Filters: retrieval.Filters{
		CreatedByApp: "notes",
	}}); !slices.Equal(searchIDs(result), searchSet(memoryResult.ID)) {
		t.Fatalf("app filter = %+v", searchIDs(result))
	}
	after := searchBaseTime.Add(3*time.Minute + 30*time.Second)
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		UpdatedAfter: &after,
	}}); !sameMembers(searchIDs(result), format.ID, longSnippet.ID) {
		t.Fatalf("updated_after filter = %+v", searchIDs(result))
	}
	before := searchBaseTime.Add(30 * time.Second)
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		UpdatedBefore: &before,
	}}); !slices.Equal(searchIDs(result), searchSet(answer.ID)) {
		t.Fatalf("updated_before filter = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "记忆", TopK: 1}); len(result.Items) != 1 {
		t.Fatalf("top_k = %d", len(result.Items))
	}
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		Kinds: []memory.Kind{"unknown"},
	}}); len(result.Items) != 0 {
		t.Fatalf("all-unknown kinds returned %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		States: []memory.State{"unknown"},
	}}); len(result.Items) != 0 {
		t.Fatalf("all-unknown states returned %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "回答", Filters: retrieval.Filters{
		States: []memory.State{memory.StateActive, memory.StateDeleted},
	}}); !sameMembers(searchIDs(result), answer.ID, format.ID, longSnippet.ID) {
		t.Fatalf("deleted state was not removed from active search: %+v", searchIDs(result))
	}

	longResult := f.run(t, retrieval.Request{Query: "关键回答"})
	if !sameMembers(searchIDs(longResult), longSnippet.ID, answer.ID, format.ID) {
		t.Fatalf("long snippet result = %+v", searchIDs(longResult))
	}
	item := searchItem(t, longResult, longSnippet.ID)
	if !strings.Contains(item.Snippet, "关键回答") || strings.Contains(item.Snippet, "关 键") {
		t.Fatalf("snippet = %q", item.Snippet)
	}
	if len([]rune(strings.Trim(item.Snippet, "…"))) > retrieval.DefaultSnippetRunes {
		t.Fatalf("snippet rune count = %d", len([]rune(item.Snippet)))
	}

	for _, query := range []string{"", " ，。！？ ", "*", "的 是 什么", "what is the", `" OR * (NEAR)`} {
		if result := f.run(t, retrieval.Request{Query: query}); len(result.Items) != 0 {
			t.Fatalf("query %q returned %+v", query, searchIDs(result))
		}
	}
	if result := f.run(t, retrieval.Request{Query: `记忆" OR * (NEAR)`}); !sameMembers(searchIDs(result), memoryResult.ID, aiMemory.ID) {
		t.Fatalf("literal operator query = %+v", searchIDs(result))
	}
}

func TestChineseDictionaryTermsMatchIndependently(t *testing.T) {
	f := newSearchFixture(t)
	reordered := f.create(t, memory.CreateInput{
		Kind: memory.KindEvent, Title: "工作变动",
		Content: "Jon工作的时候失去银行职员身份，是什么？",
	})
	f.advance(time.Minute)
	partial := f.create(t, memory.CreateInput{
		Kind: memory.KindEvent, Title: "缺少银行职员", Content: "Jon失去工作。",
	})

	query := "Jon是什么时候失去银行职员工作的？"
	result := f.run(t, retrieval.Request{Query: query})
	if !sameMembers(searchIDs(result), reordered.ID, partial.ID) {
		t.Fatalf("long Chinese query = %+v", searchIDs(result))
	}
	item := searchItem(t, result, reordered.ID)
	wantTerms := []string{"jon", "失去", "银行职员", "工作"}
	if !slices.Equal(item.MatchedTerms, wantTerms) {
		t.Fatalf("matched_terms = %#v, want %#v", item.MatchedTerms, wantTerms)
	}
}

func TestKeywordTermSelectionAndCoverageRerank(t *testing.T) {
	f := newSearchFixture(t)
	exact := f.create(t, memory.CreateInput{
		Kind: memory.KindEvent, Title: "记录", Content: "Jon失去银行职员工作。",
	})
	f.advance(time.Minute)
	partial := f.create(t, memory.CreateInput{
		Kind: memory.KindEvent, Title: "Jon失去工作", Content: "无关正文。",
	})
	f.advance(time.Minute)
	borderline := f.create(t, memory.CreateInput{
		Kind: memory.KindEvent, Title: "Jon工作", Content: "无关正文。",
	})
	f.advance(time.Minute)
	weak := f.create(t, memory.CreateInput{
		Kind: memory.KindEvent, Title: "工作记录", Content: "普通工作记录。",
	})

	result := f.run(t, retrieval.Request{Query: "Jon是什么时候失去银行职员工作的？"})
	if !slices.Equal(searchIDs(result), []string{exact.ID, partial.ID, borderline.ID, weak.ID}) {
		t.Fatalf("reranked result = %+v, want exact, partial, borderline, weak", searchIDs(result))
	}
	for i := 1; i < len(result.Items); i++ {
		if result.Items[i-1].Score < result.Items[i].Score {
			t.Fatalf("scores are not descending: %+v", result.Items)
		}
	}
	for _, item := range result.Items {
		for _, term := range item.MatchedTerms {
			if term == "的" || term == "是" || term == "什么" || term == "时候" {
				t.Fatalf("stopword leaked into matched_terms: %#v", item.MatchedTerms)
			}
		}
	}
	if got := searchItem(t, result, exact.ID).MatchedTerms; !slices.Equal(got, []string{"jon", "失去", "银行职员", "工作"}) {
		t.Fatalf("exact matched_terms = %#v", got)
	}
	if got := searchItem(t, result, partial.ID).MatchedTerms; !slices.Equal(got, []string{"jon", "失去", "工作"}) {
		t.Fatalf("partial matched_terms = %#v", got)
	}
	if got := searchItem(t, result, weak.ID).MatchedTerms; !slices.Equal(got, []string{"工作"}) {
		t.Fatalf("low-coverage matched_terms = %#v", got)
	}

	keywords := f.run(t, retrieval.Request{Query: "jon 失去 银行职员 工作"})
	if !slices.Equal(searchIDs(keywords), searchIDs(result)) {
		t.Fatalf("keywords and question differ: %+v vs %+v", searchIDs(keywords), searchIDs(result))
	}
}

func TestNormalizationEquivalentInputs(t *testing.T) {
	f := newSearchFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "Café 2026",
		Content:      "记住 Cafe\u0301 偏好与 AI 记忆。",
		CreatedByApp: "desktop",
	})
	for _, query := range []string{"café", "Cafe\u0301", "CAFÉ", "cafe", "2026", "AI记忆"} {
		result := f.run(t, retrieval.Request{Query: query})
		if !slices.Equal(searchIDs(result), searchSet(created.ID)) {
			t.Fatalf("query %q result = %+v", query, searchIDs(result))
		}
		if len(result.Items[0].MatchedTerms) == 0 {
			t.Fatalf("query %q returned no matched_terms", query)
		}
	}
}

func TestNaturalLanguageAndKeywordsUseAnySemantics(t *testing.T) {
	f := newSearchFixture(t)
	daughter := f.create(t, memory.CreateInput{Kind: memory.KindEvent, Title: "入学", Content: "女儿将于2027年入读小学。"})
	allergy := f.create(t, memory.CreateInput{Kind: memory.KindFact, Title: "饮食限制", Content: "花生过敏。"})
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Title: "疑问词", Content: "我 对 什么 什么时候 the is what"})
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Title: "无关记录", Content: "办公室使用蓝色桌子。"})
	for _, test := range []struct {
		queries []string
		want    string
	}{
		{queries: []string{"女儿什么时候上小学？", "女儿 小学", "小学 不存在的词"}, want: daughter.ID},
		{queries: []string{"我对什么过敏？", "过敏", "过敏 不存在的词"}, want: allergy.ID},
	} {
		for _, mode := range []retrieval.Mode{retrieval.ModeKeyword, retrieval.ModeHybrid} {
			for _, query := range test.queries {
				result := f.run(t, retrieval.Request{Query: query, Mode: mode})
				if !slices.Equal(searchIDs(result), []string{test.want}) {
					t.Fatalf("mode=%s query=%q result=%v", mode, query, searchIDs(result))
				}
			}
		}
	}
	for _, query := range []string{"我 对 什么", "什么时候", "the is what", "火星殖民地"} {
		for _, mode := range []retrieval.Mode{retrieval.ModeKeyword, retrieval.ModeHybrid} {
			if result := f.run(t, retrieval.Request{Query: query, Mode: mode}); len(result.Items) != 0 {
				t.Fatalf("negative query=%q mode=%s result=%v", query, mode, searchIDs(result))
			}
		}
	}
}

func TestAnySearchPreservesStructuredFilters(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	transition := searchBaseTime.Add(time.Hour)
	current := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "本项目", Content: "项目使用 SQLite。", ScopeKey: "project:dmemd",
		ValidFrom: &transition, ValidFromBasis: memory.BasisExplicit,
	})
	historical := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "旧数据库", Content: "项目使用 MySQL。", ScopeKey: "project:dmemd",
		ValidTo: &transition, ValidToBasis: memory.BasisExplicit,
	})
	global := f.create(t, memory.CreateInput{Kind: memory.KindPreference, Content: "偏好 SQLite。", ScopeKey: "user"})
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "SQLite 用于其他项目。", ScopeKey: "project:other"})
	pending := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Content: "SQLite 未确认。", ScopeKey: "project:dmemd", AssertionStatus: memory.AssertionPending,
	})
	f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Content: "SQLite 错误记录。", ScopeKey: "project:dmemd", AssertionStatus: memory.AssertionRejected,
	})
	archived := f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "SQLite 归档。", ScopeKey: "project:dmemd"})
	if _, err := f.memories.UpdateState(ctx, archived.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []retrieval.Mode{retrieval.ModeKeyword, retrieval.ModeHybrid} {
		for _, test := range []struct {
			name    string
			filters retrieval.Filters
			want    []string
		}{
			{name: "current scope", filters: retrieval.Filters{ScopeKey: "project:dmemd", AsOf: timePtr(transition)}, want: []string{current.ID, global.ID, pending.ID}},
			{name: "historical scope", filters: retrieval.Filters{ScopeKey: "project:dmemd", AsOf: timePtr(searchBaseTime)}, want: []string{historical.ID, global.ID, pending.ID}},
			{name: "unknown scope", filters: retrieval.Filters{ScopeKey: "project:unknown", AsOf: timePtr(transition)}, want: []string{global.ID}},
			{name: "kind", filters: retrieval.Filters{ScopeKey: "project:dmemd", AsOf: timePtr(transition), Kinds: []memory.Kind{memory.KindFact}}, want: []string{current.ID, pending.ID}},
			{name: "archive", filters: retrieval.Filters{ScopeKey: "project:dmemd", AsOf: timePtr(transition), States: []memory.State{memory.StateArchived}}, want: []string{archived.ID}},
		} {
			// Each eligible memory matches only a subset of these query terms.
			result := f.run(t, retrieval.Request{Query: "什么 SQLite MySQL 不存在的词", Mode: mode, Filters: test.filters})
			if !sameMembers(searchIDs(result), test.want...) {
				t.Fatalf("mode=%s %s = %v, want %v", mode, test.name, searchIDs(result), test.want)
			}
		}
	}
}

func TestNegationOnlyTermsRemainSearchable(t *testing.T) {
	f := newSearchFixture(t)
	for _, term := range []string{"不", "没", "无", "not", "never"} {
		created := f.create(t, memory.CreateInput{Kind: memory.KindFact, Title: "否定", Content: term})
		for _, mode := range []retrieval.Mode{retrieval.ModeKeyword, retrieval.ModeHybrid} {
			result := f.run(t, retrieval.Request{Query: "什么 " + term, Mode: mode})
			if !slices.Equal(searchIDs(result), []string{created.ID}) || !slices.Equal(result.Items[0].MatchedTerms, []string{term}) {
				t.Fatalf("negation %q mode=%s = %+v", term, mode, result.Items)
			}
		}
	}
}

func TestSearchLifecycleCorrectionAndRollback(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	oldEvidence := f.evidence("旧的记忆内容")
	created := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "旧标题", Content: "旧的记忆内容",
		CreatedByApp: "desktop", Evidence: []source.Evidence{oldEvidence},
	})
	if result := f.run(t, retrieval.Request{Query: "旧"}); !slices.Equal(searchIDs(result), searchSet(created.ID)) {
		t.Fatalf("initial search = %+v", searchIDs(result))
	}

	f.advance(time.Minute)
	revised := f.revise(t, memory.ReviseInput{
		MemoryID: created.ID, Kind: memory.KindFact, Title: "新标题", Content: "新的搜索内容",
		ObservedAt: f.clock, Operation: memory.OperationUpdate, Reason: "correction",
	})
	if result := f.run(t, retrieval.Request{Query: "旧"}); len(result.Items) != 0 {
		t.Fatalf("corrected old text still matched: %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新"}); !slices.Equal(searchIDs(result), searchSet(created.ID)) {
		t.Fatalf("corrected text search = %+v", searchIDs(result))
	}

	f.advance(time.Minute)
	addedEvidence := f.evidence("新的搜索内容")
	revised = f.revise(t, memory.ReviseInput{
		MemoryID: revised.ID, Evidence: []source.Evidence{addedEvidence},
		Operation: memory.OperationAddEvidence,
	})
	result := f.run(t, retrieval.Request{Query: "新"})
	item := searchItem(t, result, created.ID)
	if len(item.EvidenceRefs) != 1 || item.EvidenceRefs[0].ID != addedEvidence.ID {
		t.Fatalf("evidence refs after add_evidence = %+v", item.EvidenceRefs)
	}

	f.advance(time.Minute)
	archived, err := f.memories.UpdateState(ctx, created.ID, memory.StateArchived)
	if err != nil {
		t.Fatal(err)
	}
	if result := f.run(t, retrieval.Request{Query: "新"}); len(result.Items) != 0 {
		t.Fatalf("archived memory returned by default: %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新", Filters: retrieval.Filters{
		States: []memory.State{memory.StateArchived},
	}}); !slices.Equal(searchIDs(result), searchSet(created.ID)) {
		t.Fatalf("explicit archived search = %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新", Filters: retrieval.Filters{
		States: []memory.State{memory.StateActive, memory.StateArchived},
	}}); !slices.Equal(searchIDs(result), searchSet(created.ID)) {
		t.Fatalf("active+archived search = %+v", searchIDs(result))
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
			t.Fatalf("deleted memory returned for states %v: %+v", states, searchIDs(result))
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
	if result := f.run(t, retrieval.Request{Query: "旧"}); !slices.Equal(searchIDs(result), searchSet(created.ID)) {
		t.Fatalf("restored memory not searchable: %+v", searchIDs(result))
	}
	if result := f.run(t, retrieval.Request{Query: "新"}); len(result.Items) != 0 {
		t.Fatalf("current text should be the restored old content: %+v", searchIDs(result))
	}

	if err := f.memories.Purge(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	for _, states := range [][]memory.State{{memory.StateActive}, {memory.StateArchived}} {
		if result := f.run(t, retrieval.Request{Query: "旧", Filters: retrieval.Filters{States: states}}); len(result.Items) != 0 {
			t.Fatalf("purged memory searchable: %+v", searchIDs(result))
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
		t.Fatalf("rolled-back memory left a search hit: %+v", searchIDs(result))
	}
}

func TestIndexRebuildMatchesSearchResults(t *testing.T) {
	f := newSearchFixture(t)
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
		before[i] = searchIDs(f.run(t, request))
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
		after := searchIDs(f.run(t, request))
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

func TestRelatedContext(t *testing.T) {
	f := newSearchFixture(t)
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
	f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "疑问词", Content: "什么",
	})

	first, err := f.search.RelatedContext(ctx, "", "回答 记忆", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !sameMembers(searchMemoryIDs(first.Memories), answer.ID, notes.ID, archived.ID) {
		t.Fatalf("related context = %+v", searchMemoryIDs(first.Memories))
	}
	if !first.Complete {
		t.Fatalf("related context unexpectedly truncated: %+v", first)
	}
	for _, item := range first.Memories {
		if item.ID == deleted.ID || item.ID == unrelated.ID {
			t.Fatalf("ineligible memory entered context: %+v", item)
		}
	}

	limited, err := f.search.RelatedContext(ctx, "", "回答", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Memories) != 1 || limited.Complete {
		t.Fatalf("limited related = %+v complete=%v", searchMemoryIDs(limited.Memories), limited.Complete)
	}
	if limited.Memories[0].ID == deleted.ID || limited.Memories[0].ID == unrelated.ID {
		t.Fatalf("ineligible memory entered limited context: %+v", limited.Memories)
	}
	related, err := f.search.RelatedContext(ctx, "", "回答 什么", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !sameMembers(searchMemoryIDs(related.Memories), answer.ID, archived.ID) {
		t.Fatalf("related context term selection = %+v", searchMemoryIDs(related.Memories))
	}
	if result := f.run(t, retrieval.Request{Query: "回答 什么"}); !slices.Equal(searchIDs(result), searchSet(answer.ID)) {
		t.Fatalf("public any term selection = %+v", searchIDs(result))
	}
	empty, err := f.search.RelatedContext(ctx, "", "火星殖民地", 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Memories == nil || len(empty.Memories) != 0 {
		t.Fatalf("missing related context = %#v", empty)
	}
}

var errP4IndexFailure = errors.New("injected index rebuild failure")

type failingIndexBackend struct {
	*sqlite.Store
}

func (f failingIndexBackend) RebuildSearchDocuments(context.Context, memory.DocumentBuilder) (int, error) {
	return 0, errP4IndexFailure
}

func searchIDs(result retrieval.Result) []string {
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.Memory.ID)
	}
	return ids
}

func sameMembers(ids []string, want ...string) bool {
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

func searchSet(ids ...string) []string {
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

func searchItem(t *testing.T, result retrieval.Result, id string) retrieval.Item {
	t.Helper()
	for _, item := range result.Items {
		if item.Memory.ID == id {
			return item
		}
	}
	t.Fatalf("memory %s missing from result %+v", id, searchIDs(result))
	return retrieval.Item{}
}

func searchMemoryIDs(items []memory.Memory) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
