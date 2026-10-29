package governance

import (
	"errors"
	"testing"
	"time"
)

func TestRelationValidation(t *testing.T) {
	valid := Relation{
		ID: "rel-1", Kind: RelationDuplicate,
		FromType: EndpointObservation, FromID: "obs-1",
		ToType: EndpointMemory, ToID: "mem-1", ToVersion: 2,
		CreatedAt: time.Now().UTC(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid relation rejected: %v", err)
	}
	cases := []Relation{
		{ID: "", Kind: RelationDuplicate, FromType: EndpointObservation, FromID: "obs-1", ToType: EndpointMemory, ToID: "mem-1"},
		{ID: "rel", Kind: "unknown", FromType: EndpointObservation, FromID: "obs-1", ToType: EndpointMemory, ToID: "mem-1"},
		{ID: "rel", Kind: RelationDuplicate, FromType: EndpointObservation, FromID: "obs-1", FromVersion: 1, ToType: EndpointMemory, ToID: "mem-1"},
		{ID: "rel", Kind: RelationDuplicate, FromType: "", FromID: "obs-1", ToType: EndpointMemory, ToID: "mem-1"},
		{ID: "rel", Kind: RelationDuplicate, FromType: EndpointObservation, FromID: "obs-1", ToType: EndpointMemory, ToID: ""},
	}
	for _, item := range cases {
		if err := item.Validate(); !errors.Is(err, ErrInvalidRelation) {
			t.Fatalf("invalid relation %+v error = %v", item, err)
		}
	}
}

func TestRelationVocabulary(t *testing.T) {
	for _, kind := range []RelationKind{
		RelationDuplicate, RelationSupports, RelationEnriches, RelationSupersedes,
		RelationContradicts, RelationSeparate, RelationIndependent, RelationUnresolved,
	} {
		if !kind.Known() {
			t.Fatalf("relation kind %q is not known", kind)
		}
	}
	if RelationKind("unknown").Known() {
		t.Fatal("unknown relation kind was accepted")
	}
	if !EndpointObservation.Known() || !EndpointMemory.Known() {
		t.Fatal("endpoint types must be known")
	}
	if EndpointType("derived").Known() {
		t.Fatal("unsupported endpoint type was accepted")
	}
}

func TestRelationTargetRequirement(t *testing.T) {
	for _, kind := range []RelationKind{
		RelationDuplicate, RelationSupports, RelationEnriches, RelationSupersedes,
		RelationContradicts,
	} {
		if !kind.RequiresTarget() {
			t.Fatalf("relation kind %q must require a target", kind)
		}
	}
	for _, kind := range []RelationKind{
		RelationSeparate, RelationIndependent, RelationUnresolved,
	} {
		if kind.RequiresTarget() {
			t.Fatalf("relation kind %q must allow an empty target", kind)
		}
	}
}
