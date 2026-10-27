package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

type repairDecisionStub struct {
	result    DecisionResult
	err       error
	repaired  DecisionResult
	repairErr error
	calls     int
	request   DecisionRepairRequest
}

func (s *repairDecisionStub) Decide(context.Context, DecisionRequest) (DecisionResult, error) {
	return s.result, s.err
}
func (s *repairDecisionStub) RepairDecision(_ context.Context, request DecisionRepairRequest) (DecisionResult, error) {
	s.calls++
	s.request = request
	return s.repaired, s.repairErr
}

func TestDecisionLocalRepair(t *testing.T) {
	request := DecisionRequest{
		Candidates:      []Candidate{{CandidateID: "c1"}, {CandidateID: "c2"}},
		RelatedMemories: []memory.Memory{{ID: "m1"}, {ID: "m2"}},
	}
	for _, tc := range []struct {
		name      string
		first     Decision
		fixed     Decision
		wantCalls int
		wantError bool
	}{
		{"separate strips targets", Decision{CandidateID: "c2", Action: ActionSeparate, TargetMemoryIDs: []string{"bogus"}}, Decision{}, 0, false},
		{"change repaired", Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"m1", "m2"}}, Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"m2"}}, 1, false},
		{"change not guessed", Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"m1", "m2"}}, Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"m1", "m2"}}, 1, true},
		{"unknown target rejected", Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"bogus"}}, Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"bogus"}}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &repairDecisionStub{
				result:   DecisionResult{Decisions: []Decision{{CandidateID: "c1", Action: ActionSkip}, tc.first}},
				repaired: DecisionResult{Decisions: []Decision{tc.fixed}},
			}
			p := &Processor{decider: stub, decisionTimeout: time.Second, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			decisions, err := p.decideValidated(context.Background(), request)
			if (err != nil) != tc.wantError || stub.calls != tc.wantCalls {
				t.Fatalf("decisions=%+v err=%v calls=%d", decisions, err, stub.calls)
			}
			if tc.wantError && !errors.Is(err, ErrProtocol) {
				t.Fatalf("err=%v", err)
			}
			if stub.calls > 0 {
				if len(stub.request.Candidates) != 1 || stub.request.Candidates[0].CandidateID != "c2" || len(stub.request.Decisions) != 1 {
					t.Fatalf("repair was not local: %+v", stub.request)
				}
				if len(stub.request.Issues) != 1 {
					t.Fatalf("issues=%+v", stub.request.Issues)
				}
			}
			if !tc.wantError {
				if len(decisions) != 2 || decisions[0].Action != ActionSkip {
					t.Fatalf("valid decision changed: %+v", decisions)
				}
				if tc.wantCalls == 0 && len(decisions[1].TargetMemoryIDs) != 0 {
					t.Fatalf("targets retained: %+v", decisions[1])
				}
			}
		})
	}
}

func TestDecisionMalformedResponseGetsOneRepair(t *testing.T) {
	stub := &repairDecisionStub{err: &ProviderError{Operation: "protocol"}, repaired: DecisionResult{Decisions: []Decision{{CandidateID: "c1", Action: ActionSkip}}}}
	p := &Processor{decider: stub, decisionTimeout: time.Second, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err := p.decideValidated(context.Background(), DecisionRequest{Candidates: []Candidate{{CandidateID: "c1"}}})
	if err != nil || stub.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, stub.calls)
	}
	stub.err = &ProviderError{Operation: "credentials"}
	stub.calls = 0
	_, err = p.decideValidated(context.Background(), DecisionRequest{Candidates: []Candidate{{CandidateID: "c1"}}})
	if err == nil || stub.calls != 0 {
		t.Fatalf("credentials retried: err=%v calls=%d", err, stub.calls)
	}
}

func TestModelCallTimeoutAndParentBudget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		parent, call time.Duration
	}{
		{"per call", time.Second, time.Millisecond},
		{"process budget", time.Millisecond, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.parent)
			defer cancel()
			_, err := modelCall(ctx, tc.call, func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, ctx.Err() })
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
