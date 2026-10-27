package model

import (
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestParseExtractionV4KeepsTimeRolesAndQualifiers(t *testing.T) {
	result, err := ParseCandidates(`{"candidates":[{
		"candidate_id":"c1","kind":"event","title":"复诊","content":"用户2026-10-08复诊，可能改到10月9日。",
		"observed_at":"2026-10-01T09:00:00Z","observed_at_basis":"explicit",
		"event_time":"2026-10-08","event_time_basis":"explicit",
		"phase":"scheduled","durability":"episodic",
		"qualifiers":{"polarity":"unknown","certainty":"uncertain","attribution":"用户转述","conditions":["医生确认后"]},
		"procedure":{"goal":"复诊","steps":["挂号","就诊"]},
		"evidence":[{"segment_id":"seg-0001","quote":"用户2026-10-08复诊，可能改到10月9日。"}]
	}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
	candidate := result.Candidates[0]
	if candidate.EventTime == nil || candidate.EventTime.UTC() != time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("event_time = %v", candidate.EventTime)
	}
	if candidate.ObservedAtBasis != memory.BasisExplicit || candidate.EventTimeBasis != memory.BasisExplicit {
		t.Fatalf("time bases = %+v", candidate)
	}
	if candidate.Qualifiers == nil || candidate.Qualifiers.Certainty != memory.CertaintyUncertain ||
		candidate.Qualifiers.Attribution != "用户转述" || len(candidate.Qualifiers.Conditions) != 1 {
		t.Fatalf("qualifiers = %+v", candidate.Qualifiers)
	}
	if candidate.Procedure == nil || candidate.Procedure.Goal != "复诊" || len(candidate.Procedure.Steps) != 2 {
		t.Fatalf("procedure = %+v", candidate.Procedure)
	}
}

func TestParseExtractionV4RejectsMonthOnlyTime(t *testing.T) {
	result, err := ParseCandidates(`{"candidates":[{"kind":"event","content":"10月复诊","valid_from":"2026-10"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ValidFrom != nil ||
		len(result.Candidates[0].TemporalErrors) == 0 {
		t.Fatalf("result = %+v", result.Candidates)
	}
}
