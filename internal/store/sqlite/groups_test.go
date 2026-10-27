package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestFactGroupIdentityIsScopedAndConditioned(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

	inputs := []memory.FactGroupInput{
		{ScopeKey: "project:a", Subject: "  User ", Predicate: "Language", Scope: "Work", Conditions: []string{"Written"}, Cardinality: memory.CardinalitySingle},
		{ScopeKey: "project:a", Subject: "user", Predicate: "language", Scope: "Work", Conditions: []string{"Spoken"}, Cardinality: memory.CardinalitySingle},
		{ScopeKey: "project:b", Subject: "user", Predicate: "language", Scope: "Work", Conditions: []string{"Written"}, Cardinality: memory.CardinalitySingle},
	}
	groups := make([]memory.FactGroup, 0, len(inputs))
	for _, input := range inputs {
		group, err := memory.NewFactGroup(input, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			return tx.InsertFactGroup(ctx, group)
		}); err != nil {
			t.Fatal(err)
		}
		groups = append(groups, group)
	}

	for index, input := range inputs {
		var found memory.FactGroup
		if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			var err error
			found, err = tx.FindFactGroup(ctx, memory.FactGroupIdentity{
				ScopeKey: input.ScopeKey, Subject: input.Subject, Predicate: input.Predicate,
				Scope: input.Scope, Conditions: input.Conditions,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if found.ID != groups[index].ID {
			t.Fatalf("identity %d matched %s want %s", index, found.ID, groups[index].ID)
		}
	}

	var scoped []memory.FactGroup
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		scoped, err = tx.FindFactGroups(ctx, "project:a", "USER", "language")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 {
		t.Fatalf("scoped groups = %d, want 2 (other scope must not leak)", len(scoped))
	}
}
