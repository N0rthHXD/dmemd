package integration_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

const hybridProfile = "hybrid-profile"

type hybridEncoder struct {
	profileID     string
	dimensions    int
	minSimilarity float64
	vector        []float32
	err           error
	block         bool
	mu            sync.Mutex
	calls         int
}

func (e *hybridEncoder) ProfileID() string      { return e.profileID }
func (e *hybridEncoder) Dimensions() int        { return e.dimensions }
func (e *hybridEncoder) MinSimilarity() float64 { return e.minSimilarity }
func (e *hybridEncoder) EmbedQuery(ctx context.Context, _ string) ([]float32, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	if e.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if e.err != nil {
		return nil, e.err
	}
	return e.vector, nil
}
func (e *hybridEncoder) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type hybridFixture struct {
	store    *sqlite.Store
	profile  string
	clock    time.Time
	memories *memory.Service
	search   *retrieval.Service
}

func newHybridFixture(t *testing.T, encoder retrieval.QueryEncoder, profileID string, enabled bool) *hybridFixture {
	t.Helper()
	store := testutil.NewStore(t)
	store.ConfigureEmbedding(enabled, profileID)
	clock := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	builder := retrieval.NewDocumentBuilder()
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, builder, memory.WithClock(now))
	options := []retrieval.Option{}
	if encoder != nil {
		options = append(options, retrieval.WithSemantic(encoder))
	}
	search := retrieval.NewService(store, builder, options...)
	return &hybridFixture{store: store, profile: profileID, clock: clock, memories: memories, search: search}
}

func (f *hybridFixture) advance(delta time.Duration) { f.clock = f.clock.Add(delta) }

func (f *hybridFixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
	t.Helper()
	if input.ObservedAt.IsZero() {
		input.ObservedAt = f.clock
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

// seedVectors commits one three-dimensional vector for every queued job. The
// vector map is keyed by memory ID; every current job must be present.
func (f *hybridFixture) seedVectors(t *testing.T, vectors map[string][]float32) {
	t.Helper()
	ctx := context.Background()
	for {
		job, found, err := f.store.ClaimJob(ctx, f.profile, f.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return
		}
		vector, ok := vectors[job.MemoryID]
		if !ok {
			t.Fatalf("unexpected embedding job for %s", job.MemoryID)
		}
		encoded, err := embedding.EncodeVector(vector, 3)
		if err != nil {
			t.Fatal(err)
		}
		committed, err := f.store.CommitJob(ctx, job, encoded, 3, f.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !committed {
			t.Fatalf("vector commit rejected for %s", job.MemoryID)
		}
	}
}

func TestHybridRRFFusionAndExplanations(t *testing.T) {
	encoder := &hybridEncoder{
		profileID: hybridProfile, dimensions: 3, minSimilarity: 0.5,
		vector: []float32{1, 0, 0},
	}
	f := newHybridFixture(t, encoder, hybridProfile, true)
	keywordOnly := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "格式", Content: "回答应简洁并且不要展开。",
	})
	f.advance(time.Minute)
	semanticOnly := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "饮食", Content: "用户偏好清淡饮食。",
	})
	f.advance(time.Minute)
	both := f.create(t, memory.CreateInput{
		Kind: memory.KindPreference, Title: "回答", Content: "回答保持简洁。",
	})
	f.seedVectors(t, map[string][]float32{
		keywordOnly.ID:  {0, 1, 0},
		semanticOnly.ID: {1, 0, 0},
		both.ID:         {0.98, 0.2, 0},
	})

	result, err := f.search.Search(context.Background(), retrieval.Request{
		Query: "回答", Mode: retrieval.ModeHybrid, TopK: 10, Explain: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Retrieval.Mode != retrieval.ModeHybrid {
		t.Fatalf("retrieval = %+v", result.Retrieval)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	if got := hybridIDs(result); len(got) != 3 || got[0] != both.ID || got[1] != semanticOnly.ID || got[2] != keywordOnly.ID {
		t.Fatalf("hybrid order = %v", got)
	}
	wantBoth := 1/61.0 + 1/62.0
	wantSemantic := 1 / 61.0
	wantKeyword := 1 / 62.0
	for _, item := range result.Items {
		switch item.Memory.ID {
		case both.ID:
			if math.Abs(item.Score-wantBoth) > 1e-12 {
				t.Fatalf("both score = %v, want %v", item.Score, wantBoth)
			}
			if len(item.MatchedTerms) != 1 || item.MatchedTerms[0] != "回答" {
				t.Fatalf("both matched_terms = %#v", item.MatchedTerms)
			}
			if item.Explanation == nil || item.Explanation.Ranking != "rrf" ||
				item.Explanation.Keyword == nil || item.Explanation.Keyword.Rank != 1 ||
				item.Explanation.Semantic == nil || item.Explanation.Semantic.Rank != 2 {
				t.Fatalf("both explanation = %+v", item.Explanation)
			}
			if item.Explanation.FieldWeights != [2]int{} {
				t.Fatalf("hybrid field weights = %v", item.Explanation.FieldWeights)
			}
		case semanticOnly.ID:
			if math.Abs(item.Score-wantSemantic) > 1e-12 {
				t.Fatalf("semantic score = %v", item.Score)
			}
			if item.MatchedTerms == nil || len(item.MatchedTerms) != 0 {
				t.Fatalf("semantic matched_terms = %#v", item.MatchedTerms)
			}
			if item.Explanation == nil || item.Explanation.Keyword != nil ||
				item.Explanation.Semantic == nil || item.Explanation.Semantic.Rank != 1 {
				t.Fatalf("semantic explanation = %+v", item.Explanation)
			}
		case keywordOnly.ID:
			if math.Abs(item.Score-wantKeyword) > 1e-12 {
				t.Fatalf("keyword score = %v", item.Score)
			}
			if item.Explanation == nil || item.Explanation.Semantic != nil ||
				item.Explanation.Keyword == nil || item.Explanation.Keyword.Rank != 2 {
				t.Fatalf("keyword explanation = %+v", item.Explanation)
			}
		}
	}

	filtered, err := f.search.Search(context.Background(), retrieval.Request{
		Query: "回答", Mode: retrieval.ModeHybrid, Filters: retrieval.Filters{Kinds: []memory.Kind{memory.KindPreference}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(filtered); len(got) != 1 || got[0] != both.ID {
		t.Fatalf("filtered hybrid = %v", got)
	}

	if _, err := f.memories.UpdateState(context.Background(), both.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	defaultResult, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", Mode: retrieval.ModeHybrid})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range hybridIDs(defaultResult) {
		if id == both.ID {
			t.Fatal("archived memory returned by default hybrid search")
		}
	}
	archivedResult, err := f.search.Search(context.Background(), retrieval.Request{
		Query: "回答", Mode: retrieval.ModeHybrid,
		Filters: retrieval.Filters{States: []memory.State{memory.StateArchived}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(archivedResult); len(got) != 1 || got[0] != both.ID {
		t.Fatalf("archived hybrid = %v", got)
	}

	// A content revision invalidates the semantic-only vector.
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := f.memories.Revise(ctx, tx, memory.ReviseInput{
			MemoryID: semanticOnly.ID, Content: "用户偏好辛辣饮食。", ObservedAt: f.clock,
			Operation: memory.OperationUpdate,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	afterRevision, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", Mode: retrieval.ModeHybrid})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range hybridIDs(afterRevision) {
		if id == semanticOnly.ID {
			t.Fatal("stale vector returned after content revision")
		}
	}
}

func TestHybridDegradation(t *testing.T) {
	t.Run("empty vector index", func(t *testing.T) {
		encoder := &hybridEncoder{profileID: hybridProfile, dimensions: 3, minSimilarity: 0.5, vector: []float32{1, 0, 0}}
		f := newHybridFixture(t, encoder, hybridProfile, true)
		created := f.create(t, memory.CreateInput{Title: "回答", Content: "回答应简洁。"})
		result, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", Mode: retrieval.ModeHybrid})
		if err != nil {
			t.Fatal(err)
		}
		if result.Retrieval.Mode != retrieval.ModeHybrid || len(result.Warnings) != 0 {
			t.Fatalf("empty-index result = %+v", result)
		}
		if got := hybridIDs(result); len(got) != 1 || got[0] != created.ID {
			t.Fatalf("keyword candidates = %v", got)
		}
		if encoder.callCount() != 1 {
			t.Fatalf("query model calls = %d", encoder.callCount())
		}
	})

	t.Run("semantic unavailable", func(t *testing.T) {
		encoder := &hybridEncoder{profileID: hybridProfile, dimensions: 3, minSimilarity: 0.5, vector: []float32{1, 0, 0}}
		f := newHybridFixture(t, encoder, hybridProfile, true)
		created := f.create(t, memory.CreateInput{Title: "回答", Content: "回答应简洁。"})
		f.seedVectors(t, map[string][]float32{created.ID: {1, 0, 0}})
		encoder.err = errors.New("provider down")
		result, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", Mode: retrieval.ModeHybrid})
		if err != nil {
			t.Fatal(err)
		}
		if result.Retrieval.Mode != retrieval.ModeKeyword || len(result.Warnings) != 1 || result.Warnings[0] != retrieval.WarningSemanticUnavailable {
			t.Fatalf("unavailable result = %+v", result)
		}
		if len(result.Items) != 1 || result.Items[0].Memory.ID != created.ID {
			t.Fatalf("unavailable fallback = %v", hybridIDs(result))
		}
		if result.Items[0].Explanation != nil {
			t.Fatal("explanation returned without explain=true")
		}
	})

	t.Run("semantic timeout", func(t *testing.T) {
		encoder := &hybridEncoder{profileID: hybridProfile, dimensions: 3, minSimilarity: 0.5, vector: []float32{1, 0, 0}, block: true}
		f := newHybridFixture(t, encoder, hybridProfile, true)
		created := f.create(t, memory.CreateInput{Title: "回答", Content: "回答应简洁。"})
		f.seedVectors(t, map[string][]float32{created.ID: {1, 0, 0}})
		started := time.Now()
		result, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", Mode: retrieval.ModeHybrid})
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(started) > time.Second {
			t.Fatal("semantic timeout exceeded the budget by too much")
		}
		if result.Retrieval.Mode != retrieval.ModeKeyword || len(result.Warnings) != 1 || result.Warnings[0] != retrieval.WarningSemanticTimeout {
			t.Fatalf("timeout result = %+v", result)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		f := newHybridFixture(t, nil, "", false)
		created := f.create(t, memory.CreateInput{Title: "回答", Content: "回答应简洁。"})
		result, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", Mode: retrieval.ModeHybrid})
		if err != nil {
			t.Fatal(err)
		}
		if result.Retrieval.Mode != retrieval.ModeKeyword || len(result.Warnings) != 1 || result.Warnings[0] != retrieval.WarningSemanticUnavailable {
			t.Fatalf("disabled result = %+v", result)
		}
		if got := hybridIDs(result); len(got) != 1 || got[0] != created.ID {
			t.Fatalf("disabled fallback = %v", got)
		}
	})

	t.Run("sqlite error is not hidden", func(t *testing.T) {
		f := newHybridFixture(t, nil, "", false)
		f.store.Close()
		if _, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答"}); err == nil {
			t.Fatal("search on a closed database succeeded")
		}
	})
}

func TestHybridValidationBlankAndCancellation(t *testing.T) {
	encoder := &hybridEncoder{profileID: hybridProfile, dimensions: 3, minSimilarity: 0.5, vector: []float32{1, 0, 0}, block: true}
	f := newHybridFixture(t, encoder, hybridProfile, true)
	f.create(t, memory.CreateInput{Title: "回答", Content: "回答应简洁。"})

	if _, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", Mode: "unknown"}); !errors.Is(err, retrieval.ErrInvalidRequest) {
		t.Fatalf("invalid mode error = %v", err)
	}
	if _, err := f.search.Search(context.Background(), retrieval.Request{Query: "回答", TopK: retrieval.MaxTopK + 1}); !errors.Is(err, retrieval.ErrInvalidRequest) {
		t.Fatalf("top_k error = %v", err)
	}
	for _, query := range []string{"", " ，。！？ "} {
		result, err := f.search.Search(context.Background(), retrieval.Request{Query: query, Mode: retrieval.ModeHybrid})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 0 || result.Retrieval.Mode != retrieval.ModeHybrid {
			t.Fatalf("blank query result = %+v", result)
		}
	}
	if encoder.callCount() != 0 {
		t.Fatal("blank query called the query model")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.search.Search(ctx, retrieval.Request{Query: "回答", Mode: retrieval.ModeHybrid}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search error = %v", err)
	}
}

func hybridIDs(result retrieval.Result) []string {
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.Memory.ID)
	}
	return ids
}
