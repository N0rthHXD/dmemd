package testutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestNewStoreCreatesIsolatedMigratedDatabases(t *testing.T) {
	first := NewStore(t)
	second := NewStore(t)
	if first.Path() == second.Path() {
		t.Fatal("parallel test stores share a path")
	}
	for _, store := range []*sqlite.Store{first, second} {
		var count int
		if err := OpenInspector(t, store.Path()).QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("migration count = %d, want 2", count)
		}
	}
	CheckPrivateDatabaseFiles(t, first.Path())
}

func TestNewStoresCanRunInParallel(t *testing.T) {
	for i := 0; i < 4; i++ {
		t.Run("store", func(t *testing.T) {
			t.Parallel()
			store := NewStore(t)
			db := OpenInspector(t, store.Path())
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM memories`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("new store memories = %d", count)
			}
		})
	}
}

func TestFaultInjectorMarksPreciseStep(t *testing.T) {
	injected := errors.New("boom")
	injector := &FaultInjector{FailAt: "evidence", Err: injected}
	if err := injector.Checkpoint("snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := injector.Checkpoint("evidence"); !errors.Is(err, injected) {
		t.Fatalf("checkpoint error = %v", err)
	}
	if !injector.Hit() {
		t.Fatal("Hit() = false")
	}
	if got := strings.Join(injector.Steps(), ","); got != "snapshot,evidence" {
		t.Fatalf("steps = %q", got)
	}
}

func TestDocumentBuilderKeepsOriginalTextAndLowercasesTerms(t *testing.T) {
	item := memory.Memory{ID: "m1", Title: "Memory Title", Content: "Memory Content", State: memory.StateActive, UpdatedAt: time.Unix(0, 0).UTC()}
	document, err := (DocumentBuilder{}).BuildDocument(item)
	if err != nil {
		t.Fatal(err)
	}
	if document.TitleTerms != "memory title" || document.ContentTerms != "memory content" {
		t.Fatalf("terms = %q / %q", document.TitleTerms, document.ContentTerms)
	}
	if document.Title != "Memory Title" || document.Content != "Memory Content" {
		t.Fatalf("original text changed: %+v", document)
	}
}

func TestFakeModelScriptsResponsesAndCapturesRequests(t *testing.T) {
	model := NewFakeModel(t)
	model.EnqueueContent(`{"candidates":[]}`)
	model.EnqueueStatus(http.StatusTooManyRequests)
	model.EnqueueRaw(http.StatusOK, `{"unexpected":true}`)

	for i := 0; i < 3; i++ {
		response, err := http.Post(model.URL()+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"test"}`))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	if model.RequestCount() != 3 {
		t.Fatalf("request count = %d, want 3", model.RequestCount())
	}
	last, ok := model.LastRequest()
	if !ok || last.Method != http.MethodPost || last.Path != "/v1/chat/completions" {
		t.Fatalf("last request = %+v", last)
	}
}

func TestFakeModelDelayAndMalformedContent(t *testing.T) {
	model := NewFakeModel(t)
	model.Enqueue(FakeResponse{Delay: 50 * time.Millisecond, Content: "I could not determine any memories."})
	start := time.Now()
	response, err := http.Post(model.URL()+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("response returned after %v, want scripted delay", elapsed)
	}
	if !strings.Contains(string(body), "I could not determine any memories.") {
		t.Fatalf("malformed content missing from response: %s", body)
	}
}

func TestFakeModelBlocksUntilReleased(t *testing.T) {
	model := NewFakeModel(t)
	model.Enqueue(FakeResponse{Block: true, Content: `{"candidates":[]}`})

	done := make(chan error, 1)
	go func() {
		response, err := http.Post(model.URL()+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
		if err != nil {
			done <- err
			return
		}
		_ = response.Body.Close()
		done <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := model.WaitStarted(ctx); err != nil {
		t.Fatalf("WaitStarted: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("request completed before Release: %v", err)
	default:
	}
	model.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("request did not finish after Release")
	}
}
