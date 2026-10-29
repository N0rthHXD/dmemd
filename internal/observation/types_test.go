package observation

import "testing"

func TestObservationVocabulary(t *testing.T) {
	for _, kind := range []Kind{KindFact, KindPreference, KindEvent, KindProcedure} {
		if !kind.Known() {
			t.Fatalf("kind %q is not known", kind)
		}
	}
	if Kind("unknown").Known() {
		t.Fatal("unknown kind was accepted")
	}
	for _, status := range []Status{StatusObserved, StatusPending, StatusRetracted, StatusDeleted} {
		if !status.Known() {
			t.Fatalf("status %q is not known", status)
		}
	}
	if Status("unknown").Known() {
		t.Fatal("unknown status was accepted")
	}
	for _, mode := range []ContentMode{ContentExtractive, ContentNormalized} {
		if !mode.Known() {
			t.Fatalf("content mode %q is not known", mode)
		}
	}
	if ContentMode("unknown").Known() {
		t.Fatal("unknown content mode was accepted")
	}
}
