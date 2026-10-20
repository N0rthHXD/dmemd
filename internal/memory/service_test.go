package memory_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

var baseTime = time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)

type testClock struct {
	at time.Time
}

func (c *testClock) Now() time.Time { return c.at }

func (c *testClock) Advance(delta time.Duration) { c.at = c.at.Add(delta) }

type fixture struct {
	store   *sqlite.Store
	clock   *testClock
	memory  *memory.Service
	sources *source.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := testutil.NewStore(t)
	clock := &testClock{at: baseTime}
	return &fixture{
		store:   store,
		clock:   clock,
		memory:  memory.NewService(store, sqlite.MemoryStore{Store: store}, testutil.DocumentBuilder{}, memory.WithClock(clock.Now)),
		sources: source.NewService(store, sqlite.SourceStore{Store: store}, source.WithClock(clock.Now)),
	}
}

func (f *fixture) seedSource(t *testing.T, id, appID, content string) source.Source {
	t.Helper()
	item := source.Source{ID: id, AppID: appID, Content: []byte(content), CreatedAt: baseTime}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertSource(ctx, item)
	}); err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *fixture) create(t *testing.T, input memory.CreateInput) (memory.Memory, error) {
	t.Helper()
	var result memory.Memory
	err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		result, err = f.memory.Create(ctx, tx, input)
		return err
	})
	return result, err
}

func (f *fixture) revise(t *testing.T, input memory.ReviseInput) (memory.Memory, error) {
	t.Helper()
	var result memory.Memory
	err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		result, err = f.memory.Revise(ctx, tx, input)
		return err
	})
	return result, err
}

func reviseInput(current memory.Memory, evidence []source.Evidence, operation memory.Operation) memory.ReviseInput {
	return memory.ReviseInput{
		MemoryID:   current.ID,
		Kind:       current.Kind,
		Title:      current.Title,
		Content:    current.Content,
		ObservedAt: current.ObservedAt,
		Evidence:   evidence,
		Operation:  operation,
	}
}

func TestCreateWritesSnapshotRevisionEvidenceAndIndex(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-create", "desktop-assistant",
		`{"messages":[{"id":"msg-1","role":"user","content":"以后请默认使用中文回答。"}]}`)
	evidence := source.NewEvidence(src, "msg-1", "以后请默认使用中文回答。", "test-model", baseTime)
	if evidence.Location == nil || evidence.Location.Field != "messages.0.content" ||
		evidence.Location.Start != 0 || evidence.Location.End != 12 {
		t.Fatalf("location = %+v", evidence.Location)
	}

	created, err := f.create(t, memory.CreateInput{
		Kind:           memory.KindPreference,
		Title:          "Answer Language",
		Content:        "用户希望默认使用中文回答。",
		ObservedAt:     baseTime.Add(-time.Hour),
		CreatedByApp:   "desktop-assistant",
		OriginSourceID: src.ID,
		Evidence:       []source.Evidence{evidence},
		Reason:         "initial extraction",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.State != memory.StateActive || created.Version != 1 {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt != baseTime || created.UpdatedAt != baseTime {
		t.Fatalf("timestamps = %v %v", created.CreatedAt, created.UpdatedAt)
	}
	if created.ObservedAt != baseTime.Add(-time.Hour) {
		t.Fatalf("observed_at = %v", created.ObservedAt)
	}

	current, err := f.memory.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != created.Content || current.CreatedByApp != "desktop-assistant" {
		t.Fatalf("current = %+v", current)
	}

	versions, err := f.memory.ListVersions(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Operation != memory.OperationCreate {
		t.Fatalf("versions = %+v", versions)
	}
	if len(versions[0].Evidence) != 1 || versions[0].Evidence[0].ID != evidence.ID {
		t.Fatalf("revision evidence = %+v", versions[0].Evidence)
	}
	if versions[0].Evidence[0].Location == nil || versions[0].Evidence[0].Location.Field != "messages.0.content" {
		t.Fatalf("revision location = %+v", versions[0].Evidence[0].Location)
	}

	db := testutil.OpenInspector(t, f.store.Path())
	var memoryID, state, titleTerms string
	if err := db.QueryRowContext(context.Background(),
		`SELECT memory_id, state, title_terms FROM memory_search_docs WHERE memory_id = ?`,
		created.ID).Scan(&memoryID, &state, &titleTerms); err != nil {
		t.Fatal(err)
	}
	if memoryID != created.ID || state != string(memory.StateActive) || titleTerms != "answer language" {
		t.Fatalf("search document = %s %s %q", memoryID, state, titleTerms)
	}
}

func TestCreateRollsBackEveryTableOnCallerFailure(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-rollback", "app", `{"text":"remember me"}`)
	evidence := source.NewEvidence(src, "", "remember me", "model", baseTime)
	failure := errors.New("injected create failure")

	err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := f.memory.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindFact, Title: "title", Content: "content",
			ObservedAt: baseTime, OriginSourceID: src.ID,
			Evidence: []source.Evidence{evidence},
		}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}

	db := testutil.OpenInspector(t, f.store.Path())
	for _, table := range []string{"memories", "memory_revisions", "evidence", "revision_evidence", "memory_search_docs"} {
		if count := countRows(t, db, `SELECT COUNT(*) FROM `+table); count != 0 {
			t.Fatalf("%s rows after rollback = %d", table, count)
		}
	}
}

func TestReviseKeepsHistoryAndDoesNotInheritOldEvidence(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-revise", "app", `{"text":"old conclusion"}`)
	oldEvidence := source.NewEvidence(src, "", "old conclusion", "model", baseTime)
	created, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "First", Content: "old conclusion",
		ObservedAt: baseTime, CreatedByApp: "app", OriginSourceID: src.ID,
		Evidence: []source.Evidence{oldEvidence},
	})
	if err != nil {
		t.Fatal(err)
	}

	f.clock.Advance(time.Minute)
	revised, err := f.revise(t, memory.ReviseInput{
		MemoryID: created.ID, Kind: memory.KindFact, Title: "Corrected",
		Content: "new conclusion", ObservedAt: baseTime.Add(time.Minute),
		Operation: memory.OperationUpdate, Reason: "explicit correction",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Version != 2 || revised.Content != "new conclusion" {
		t.Fatalf("revised = %+v", revised)
	}
	if revised.CreatedAt != created.CreatedAt || revised.CreatedByApp != "app" || revised.OriginSourceID != src.ID {
		t.Fatalf("creation metadata changed: %+v", revised)
	}

	first, err := f.memory.GetVersion(context.Background(), created.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Evidence) != 1 || first.Evidence[0].ID != oldEvidence.ID {
		t.Fatalf("first evidence = %+v", first.Evidence)
	}
	second, err := f.memory.GetVersion(context.Background(), created.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Evidence) != 0 {
		t.Fatalf("new conclusion inherited evidence: %+v", second.Evidence)
	}
	if second.Operation != memory.OperationUpdate || second.Reason != "explicit correction" {
		t.Fatalf("second revision = %+v", second)
	}
}

func TestAddEvidenceCreatesVersionAndIgnoresDuplicates(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-evidence", "app", `{"text":"supporting quote"}`)
	firstEvidence := source.NewEvidence(src, "", "supporting quote", "model", baseTime)
	created, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "Title", Content: "Body",
		ObservedAt: baseTime, OriginSourceID: src.ID,
		Evidence: []source.Evidence{firstEvidence},
	})
	if err != nil {
		t.Fatal(err)
	}

	f.clock.Advance(time.Minute)
	secondEvidence := source.NewEvidence(src, "", "supporting quote", "model", f.clock.Now())
	added, err := f.revise(t, memory.ReviseInput{
		MemoryID:  created.ID,
		Evidence:  []source.Evidence{secondEvidence},
		Operation: memory.OperationAddEvidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Title != created.Title || added.Content != created.Content {
		t.Fatalf("add_evidence changed fact text: %+v", added)
	}
	if added.Version != 2 {
		t.Fatalf("version after add_evidence = %d", added.Version)
	}
	currentEvidence, err := f.memory.GetEvidence(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(currentEvidence) != 2 {
		t.Fatalf("current evidence = %+v", currentEvidence)
	}

	f.clock.Advance(time.Minute)
	duplicate, err := f.revise(t, reviseInput(added, []source.Evidence{secondEvidence}, memory.OperationAddEvidence))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Version != added.Version {
		t.Fatalf("duplicate evidence created version %d", duplicate.Version)
	}
	versions, err := f.memory.ListVersions(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions after duplicate = %d", len(versions))
	}

	f.clock.Advance(time.Minute)
	unchanged, err := f.revise(t, reviseInput(duplicate, currentEvidence, memory.OperationUpdate))
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != duplicate.Version {
		t.Fatalf("unchanged update created version %d", unchanged.Version)
	}
}

func TestLifecycleArchiveDeleteRestoreAndEvidenceQueries(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-lifecycle", "app", `{"text":"fact"}`)
	evidence := source.NewEvidence(src, "", "fact", "model", baseTime)
	created, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "Fact", Content: "fact",
		ObservedAt: baseTime, CreatedByApp: "app", OriginSourceID: src.ID,
		Evidence: []source.Evidence{evidence},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	f.clock.Advance(time.Minute)
	archived, err := f.memory.UpdateState(ctx, created.ID, memory.StateArchived)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Version != 2 || archived.State != memory.StateArchived {
		t.Fatalf("archived = %+v", archived)
	}
	archiveRevision, err := f.memory.GetVersion(ctx, created.ID, archived.Version)
	if err != nil {
		t.Fatal(err)
	}
	if archiveRevision.Operation != memory.OperationArchive || len(archiveRevision.Evidence) != 1 {
		t.Fatalf("archive revision = %+v", archiveRevision)
	}

	defaultList, err := f.memory.List(ctx, memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultList) != 1 || defaultList[0].State != memory.StateArchived {
		t.Fatalf("default management list = %+v", defaultList)
	}
	activeOnly, err := f.memory.List(ctx, memory.ListOptions{States: []memory.State{memory.StateActive}})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeOnly) != 0 {
		t.Fatalf("active-only list = %+v", activeOnly)
	}
	archivedList, err := f.memory.List(ctx, memory.ListOptions{States: []memory.State{memory.StateArchived}})
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedList) != 1 || archivedList[0].ID != created.ID {
		t.Fatalf("archived list = %+v", archivedList)
	}

	unchanged, err := f.memory.UpdateState(ctx, created.ID, memory.StateArchived)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != archived.Version {
		t.Fatalf("same state created version %d", unchanged.Version)
	}

	f.clock.Advance(time.Minute)
	active, err := f.memory.UpdateState(ctx, created.ID, memory.StateActive)
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != 3 || active.State != memory.StateActive {
		t.Fatalf("reactivated = %+v", active)
	}
	activateRevision, err := f.memory.GetVersion(ctx, created.ID, active.Version)
	if err != nil {
		t.Fatal(err)
	}
	if activateRevision.Operation != memory.OperationActivate {
		t.Fatalf("activate operation = %s", activateRevision.Operation)
	}

	f.clock.Advance(time.Minute)
	deleted, err := f.memory.Delete(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Version != 4 || deleted.State != memory.StateDeleted {
		t.Fatalf("deleted = %+v", deleted)
	}
	tombstone, err := f.memory.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tombstone.State != memory.StateDeleted {
		t.Fatalf("tombstone = %+v", tombstone)
	}
	again, err := f.memory.Delete(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != deleted.Version {
		t.Fatalf("repeat delete created version %d", again.Version)
	}
	db := testutil.OpenInspector(t, f.store.Path())
	if count := countRows(t, db, `SELECT COUNT(*) FROM memory_search_docs WHERE memory_id = ?`, created.ID); count != 0 {
		t.Fatalf("deleted memory retained %d search documents", count)
	}
	defaultList, err = f.memory.List(ctx, memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultList) != 0 {
		t.Fatalf("deleted tombstone appeared in default list: %+v", defaultList)
	}
	deletedList, err := f.memory.List(ctx, memory.ListOptions{States: []memory.State{memory.StateDeleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(deletedList) != 1 || deletedList[0].ID != created.ID {
		t.Fatalf("deleted list = %+v", deletedList)
	}
	versions, err := f.memory.ListVersions(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 4 {
		t.Fatalf("history after delete = %d versions", len(versions))
	}

	f.clock.Advance(time.Minute)
	restored, err := f.memory.Restore(ctx, memory.RestoreInput{MemoryID: created.ID, Version: 1, Reason: "restore first"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != 5 || restored.State != memory.StateActive || restored.Content != "fact" {
		t.Fatalf("restored = %+v", restored)
	}
	if restored.CreatedAt != created.CreatedAt || restored.CreatedByApp != "app" {
		t.Fatalf("restore changed creation metadata: %+v", restored)
	}
	restoreRevision, err := f.memory.GetVersion(ctx, created.ID, restored.Version)
	if err != nil {
		t.Fatal(err)
	}
	if restoreRevision.Operation != memory.OperationRestore || len(restoreRevision.Evidence) != 1 {
		t.Fatalf("restore revision = %+v", restoreRevision)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM memory_search_docs WHERE memory_id = ? AND state = 'active'`, created.ID); count != 1 {
		t.Fatalf("restored search document count = %d", count)
	}
	activeList, err := f.memory.List(ctx, memory.ListOptions{States: []memory.State{memory.StateActive}})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeList) != 1 || activeList[0].ID != created.ID {
		t.Fatalf("restored memory missing from active list: %+v", activeList)
	}

	currentEvidence, err := f.memory.GetEvidence(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(currentEvidence) != 1 || currentEvidence[0].ID != evidence.ID {
		t.Fatalf("current evidence = %+v", currentEvidence)
	}
	historicalEvidence, err := f.memory.GetEvidence(ctx, created.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(historicalEvidence) != 1 || historicalEvidence[0].ID != evidence.ID {
		t.Fatalf("historical evidence = %+v", historicalEvidence)
	}

	if _, err := f.memory.Restore(ctx, memory.RestoreInput{MemoryID: created.ID, Version: 99}); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing restore version error = %v", err)
	}
	if _, err := f.memory.GetEvidence(ctx, created.ID, 99); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing evidence version error = %v", err)
	}
	if _, err := f.memory.ListVersions(ctx, "missing", 0); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing memory versions error = %v", err)
	}
	if _, err := f.memory.GetVersion(ctx, created.ID, 99); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing version error = %v", err)
	}
	if _, err := f.memory.UpdateState(ctx, "missing", memory.StateArchived); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing state change error = %v", err)
	}
	if _, err := f.memory.Delete(ctx, "missing"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing delete error = %v", err)
	}
}

func TestDeleteRollsBackStateRevisionAndIndexTogether(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-delete-rollback", "app", `{"text":"keep"}`)
	evidence := source.NewEvidence(src, "", "keep", "model", baseTime)
	created, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "Keep", Content: "keep",
		ObservedAt: baseTime, OriginSourceID: src.ID,
		Evidence: []source.Evidence{evidence},
	})
	if err != nil {
		t.Fatal(err)
	}

	failure := errors.New("injected index failure")
	service := memory.NewService(f.store, failingDeleteStore{
		inner: sqlite.MemoryStore{Store: f.store},
		err:   failure,
	}, testutil.DocumentBuilder{}, memory.WithClock(f.clock.Now))
	if _, err := service.Delete(context.Background(), created.ID); !errors.Is(err, failure) {
		t.Fatalf("delete error = %v", err)
	}

	loaded, err := f.memory.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != memory.StateActive || loaded.Version != 1 {
		t.Fatalf("memory after rollback = %+v", loaded)
	}
	versions, err := f.memory.ListVersions(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Operation != memory.OperationCreate {
		t.Fatalf("versions after rollback = %+v", versions)
	}
	db := testutil.OpenInspector(t, f.store.Path())
	if count := countRows(t, db, `SELECT COUNT(*) FROM memory_search_docs WHERE memory_id = ? AND state = 'active'`, created.ID); count != 1 {
		t.Fatalf("search documents after rollback = %d", count)
	}
}

type failingDeleteStore struct {
	inner memory.Store
	err   error
}

func (s failingDeleteStore) WithTx(ctx context.Context, fn func(context.Context, memory.Tx) error) error {
	return s.inner.WithTx(ctx, func(ctx context.Context, tx memory.Tx) error {
		return fn(ctx, failingDeleteTx{Tx: tx, err: s.err})
	})
}

type failingDeleteTx struct {
	memory.Tx
	err error
}

func (t failingDeleteTx) DeleteSearchDocument(ctx context.Context, memoryID string) error {
	if err := t.Tx.DeleteSearchDocument(ctx, memoryID); err != nil {
		return err
	}
	return t.err
}

func TestSourceRedactionKeepsBodyAndPreventsEvidenceRestoration(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-redacted", "app",
		`{"messages":[{"id":"m1","content":"secret quote"}]}`)
	evidence := source.NewEvidence(src, "m1", "secret quote", "model", baseTime)
	created, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "Secret", Content: "body survives",
		ObservedAt: baseTime, OriginSourceID: src.ID,
		Evidence: []source.Evidence{evidence},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	first, err := f.sources.Redact(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceID != src.ID || !first.Redacted || first.CancelledJobs != 0 {
		t.Fatalf("redact result = %+v", first)
	}
	redactedSource, err := f.sources.Get(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !redactedSource.Redacted || len(redactedSource.Content) != 0 || redactedSource.RedactedAt == nil {
		t.Fatalf("source after redaction = %+v", redactedSource)
	}

	currentEvidence, err := f.memory.GetEvidence(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(currentEvidence) != 1 || !currentEvidence[0].Redacted ||
		currentEvidence[0].Quote != "" || currentEvidence[0].Location != nil {
		t.Fatalf("current evidence after redaction = %+v", currentEvidence)
	}
	historical, err := f.memory.GetVersion(ctx, created.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !historical.Evidence[0].Redacted || historical.Evidence[0].Quote != "" || historical.Evidence[0].Location != nil {
		t.Fatalf("history evidence after redaction = %+v", historical.Evidence)
	}

	restored, err := f.memory.Restore(ctx, memory.RestoreInput{MemoryID: created.ID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Content != "body survives" {
		t.Fatalf("restored body = %q", restored.Content)
	}
	restoredEvidence, err := f.memory.GetEvidence(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredEvidence) != 1 || !restoredEvidence[0].Redacted ||
		restoredEvidence[0].Quote != "" || restoredEvidence[0].Location != nil {
		t.Fatalf("evidence reappeared after restore: %+v", restoredEvidence)
	}

	second, err := f.sources.Redact(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Redacted || second.CancelledJobs != 0 {
		t.Fatalf("repeat redaction = %+v", second)
	}
	reloaded, err := f.sources.Get(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RedactedAt == nil || !reloaded.RedactedAt.Equal(*redactedSource.RedactedAt) {
		t.Fatalf("repeat redaction changed redacted_at: %v vs %v", reloaded.RedactedAt, redactedSource.RedactedAt)
	}
}

func TestPurgeRemovesDedicatedDataAndKeepsSharedEvidence(t *testing.T) {
	f := newFixture(t)
	src := f.seedSource(t, "source-purge", "app", `{"text":"shared and dedicated"}`)
	dedicated := source.NewEvidence(src, "", "dedicated", "model", baseTime)
	shared := source.NewEvidence(src, "", "shared", "model", baseTime)

	first, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "First", Content: "first",
		ObservedAt: baseTime, OriginSourceID: src.ID,
		Evidence: []source.Evidence{dedicated, shared},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "Second", Content: "second",
		ObservedAt: baseTime, OriginSourceID: src.ID,
		Evidence: []source.Evidence{shared},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := f.memory.Purge(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.memory.Get(ctx, first.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("purged memory error = %v", err)
	}
	if _, err := f.memory.GetVersion(ctx, first.ID, 1); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("purged revision error = %v", err)
	}

	db := testutil.OpenInspector(t, f.store.Path())
	if count := countRows(t, db, `SELECT COUNT(*) FROM evidence`); count != 1 {
		t.Fatalf("evidence rows after purge = %d, want 1", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM evidence WHERE id = ?`, shared.ID); count != 1 {
		t.Fatalf("shared evidence was deleted")
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sources WHERE id = ?`, src.ID); count != 1 {
		t.Fatalf("source was deleted")
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM memory_search_docs`); count != 1 {
		t.Fatalf("search documents after purge = %d", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM memory_revisions WHERE memory_id = ?`, first.ID); count != 0 {
		t.Fatalf("revisions after purge = %d", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM memory_fts WHERE memory_fts MATCH 'first'`); count != 0 {
		t.Fatalf("purged content still matches FTS: %d", count)
	}
	secondEvidence, err := f.memory.GetEvidence(ctx, second.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondEvidence) != 1 || secondEvidence[0].ID != shared.ID {
		t.Fatalf("second evidence = %+v", secondEvidence)
	}
	assertForeignKeysClean(t, db)

	if err := f.memory.Purge(ctx, first.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("repeat purge error = %v", err)
	}

	third, err := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "Third", Content: "third",
		ObservedAt: baseTime, OriginSourceID: src.ID,
		Evidence: []source.Evidence{source.NewEvidence(src, "", "third", "model", baseTime)},
	})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected purge failure")
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := f.memory.PurgeInTx(ctx, tx, third.ID); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("purge rollback error = %v", err)
	}
	if _, err := f.memory.Get(ctx, third.ID); err != nil {
		t.Fatalf("purged memory missing after rollback: %v", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM evidence`); count != 2 {
		t.Fatalf("evidence rows after purge rollback = %d, want 2", count)
	}
	if err := f.memory.Purge(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.memory.Purge(ctx, third.ID); err != nil {
		t.Fatal(err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM evidence`); count != 0 {
		t.Fatalf("orphan evidence rows after purging all memories = %d", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sources`); count != 1 {
		t.Fatalf("sources after purging all memories = %d", count)
	}
	assertForeignKeysClean(t, db)
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertForeignKeysClean(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID int64
		var parent string
		var fk int
		if err := rows.Scan(&table, &rowID, &parent, &fk); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation: %s row %d -> %s fk %d", table, rowID, parent, fk)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
