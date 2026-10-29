package governance

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestRelationRequestSerializesOnlyAliasesAndSemanticFields(t *testing.T) {
	request := RelationRequest{
		NewObservation: RelationCandidate{
			Ref: "new", Type: "observation", Kind: "fact",
			ScopeKey: "project:dmemd", Content: "项目使用 SQLite 保存记忆。",
			EvidenceHint: "项目使用 SQLite 保存记忆。",
		},
		Existing: []RelationCandidate{{
			Ref: "existing_1", Type: "memory", Kind: "fact",
			ScopeKey: "project:dmemd", Content: "项目用 SQLite 持久化数据。",
		}},
		KnownRelations: []string{"supports"},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, field := range []string{"new_observation", "existing", "known_relations"} {
		if !strings.Contains(text, `"`+field+`"`) {
			t.Fatalf("request missing %q: %s", field, text)
		}
	}
	for _, forbidden := range []string{
		`"id"`, `"source_id"`, `"observation_id"`, `"memory_id"`,
		`"version"`, `"evidence_id"`, `"action"`, `"created_at"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("request leaked %s: %s", forbidden, text)
		}
	}
	var decoded struct {
		NewObservation map[string]json.RawMessage   `json:"new_observation"`
		Existing       []map[string]json.RawMessage `json:"existing"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if ref := string(decoded.NewObservation["ref"]); ref != `"new"` {
		t.Fatalf("new ref = %s", ref)
	}
	if len(decoded.Existing) != 1 {
		t.Fatalf("existing = %+v", decoded.Existing)
	}
	if ref := string(decoded.Existing[0]["ref"]); ref != `"existing_1"` {
		t.Fatalf("existing ref = %s", ref)
	}
	keys := make([]string, 0, len(decoded.NewObservation))
	for key := range decoded.NewObservation {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"content", "evidence_hint", "kind", "ref", "scope_key", "type"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("new observation fields = %v, want %v", keys, want)
	}
}

func TestRelationRequestOmitsEmptyKnownRelations(t *testing.T) {
	encoded, err := json.Marshal(RelationRequest{
		NewObservation: RelationCandidate{Ref: "new", Content: "独立信息"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "known_relations") {
		t.Fatalf("empty known relations were serialized: %s", encoded)
	}
}
