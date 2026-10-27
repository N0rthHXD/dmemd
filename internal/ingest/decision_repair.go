package ingest

import (
	"context"
	"encoding/json"
	"time"
)

// DecisionIssue is a fixed diagnostic safe to include in a repair request.
type DecisionIssue struct {
	CandidateID string `json:"candidate_id,omitempty"`
	Code        string `json:"code"`
}

type DecisionRepairRequest struct {
	DecisionRequest
	Decisions []Decision      `json:"decisions"`
	Issues    []DecisionIssue `json:"issues"`
}

func (p *Processor) repairExtraction(ctx context.Context, provider ExtractionRepairProvider, request ExtractionRepairRequest) (ExtractionResult, error) {
	started := time.Now()
	result, err := modelCall(ctx, p.extractionTimeout, func(callCtx context.Context) (ExtractionResult, error) {
		return provider.RepairExtraction(callCtx, request)
	})
	p.logModelCall("extraction_repair", len(request.Candidates), len(request.Source.Content), started, err)
	return result, processingError("extraction_repair", "", err)
}

func (p *Processor) logModelCall(stage string, batch, size int, started time.Time, err error) {
	code := ""
	if err != nil {
		code = failureKind(err)
	}
	p.logger.Info("ingest model call", "stage", stage, "model", p.modelName,
		"batch_size", batch, "context_size", size, "duration_ms", time.Since(started).Milliseconds(), "error_type", code)
}

func (p *Processor) decideValidated(ctx context.Context, request DecisionRequest) ([]Decision, error) {
	started := time.Now()
	result, err := modelCall(ctx, p.decisionTimeout, func(callCtx context.Context) (DecisionResult, error) {
		return p.decider.Decide(callCtx, request)
	})
	p.logModelCall("decision", len(request.Candidates), decisionContextSize(request), started, err)
	if err != nil && failureKind(err) != "protocol" {
		return nil, err
	}
	for _, decision := range result.Decisions {
		if decision.Action == ActionSeparate && len(decision.TargetMemoryIDs) > 0 {
			p.logger.Info("decision protocol locally normalized", "stage", "decision", "batch_size", len(request.Candidates),
				"context_size", decisionContextSize(request), "protocol_error_type", "separate_targets_removed")
		}
	}
	valid, invalid, issues := inspectDecisions(request, result)
	if err != nil {
		valid = nil
		invalid = request.Candidates
		issues = []DecisionIssue{{Code: "invalid_structure"}}
	}
	if len(issues) == 0 {
		return valid, nil
	}
	for _, issue := range issues {
		p.logger.Warn("decision protocol issue", "stage", "decision", "batch_size", len(request.Candidates),
			"context_size", decisionContextSize(request), "protocol_error_type", issue.Code)
	}
	repairer, ok := p.decider.(DecisionRepairProvider)
	if !ok {
		return nil, processingError("decision", "decision_protocol_invalid", ErrProtocol)
	}
	repairRequest := request
	repairRequest.Candidates = invalid
	badIDs := make(map[string]bool, len(invalid))
	for _, candidate := range invalid {
		badIDs[candidate.CandidateID] = true
	}
	badDecisions := make([]Decision, 0, len(invalid))
	for _, decision := range result.Decisions {
		if badIDs[decision.CandidateID] {
			badDecisions = append(badDecisions, decision)
		}
	}
	started = time.Now()
	repaired, repairErr := modelCall(ctx, p.decisionTimeout, func(callCtx context.Context) (DecisionResult, error) {
		return repairer.RepairDecision(callCtx, DecisionRepairRequest{DecisionRequest: repairRequest, Decisions: badDecisions, Issues: issues})
	})
	p.logModelCall("decision_repair", len(invalid), decisionContextSize(repairRequest), started, repairErr)
	if repairErr != nil {
		return nil, processingError("decision_repair", "", repairErr)
	}
	fixed, _, remaining := inspectDecisions(repairRequest, repaired)
	if len(remaining) != 0 {
		for _, issue := range remaining {
			p.logger.Warn("decision repair invalid", "stage", "decision_repair", "batch_size", len(invalid),
				"context_size", decisionContextSize(repairRequest), "protocol_error_type", issue.Code)
		}
		return nil, processingError("decision_repair", "decision_repair_failed", ErrProtocol)
	}
	return validateDecisions(request.Candidates, DecisionResult{Decisions: append(valid, fixed...)})
}

func inspectDecisions(request DecisionRequest, result DecisionResult) (valid []Decision, invalid []Candidate, issues []DecisionIssue) {
	expected := make(map[string]bool, len(request.Candidates))
	candidates := make(map[string]Candidate, len(request.Candidates))
	for _, candidate := range request.Candidates {
		expected[candidate.CandidateID] = true
		candidates[candidate.CandidateID] = candidate
	}
	byID := make(map[string][]Decision)
	unknown := false
	for _, decision := range result.Decisions {
		if !expected[decision.CandidateID] {
			unknown = true
		}
		byID[decision.CandidateID] = append(byID[decision.CandidateID], decision)
	}
	for _, candidate := range request.Candidates {
		items := byID[candidate.CandidateID]
		code := ""
		if unknown {
			code = "unknown_candidate"
		} else if len(items) != 1 {
			code = "decision_cardinality"
		}
		var decision Decision
		if code == "" {
			decision = items[0]
			if decision.Action == ActionSeparate {
				decision.TargetMemoryIDs = nil
			}
			_, err := validateDecisions([]Candidate{candidate}, DecisionResult{Decisions: []Decision{decision}})
			if err != nil {
				code = "invalid_decision"
				if decision.Action == ActionChange && len(decision.TargetMemoryIDs) != 1 {
					code = "change_target_cardinality"
				}
			}
			for _, id := range decision.TargetMemoryIDs {
				found := false
				for _, item := range request.RelatedMemories {
					if item.ID == id {
						found = true
						break
					}
				}
				if !found {
					code = "unknown_target_memory"
				}
			}
			if decision.TargetGroupID != "" {
				found := false
				for _, group := range request.FactGroups {
					if group.ID == decision.TargetGroupID {
						found = true
						break
					}
				}
				if !found {
					code = "unknown_target_group"
				}
			}
			if _, err := decisionEvidence(candidate, decision, candidates); err != nil {
				code = "invalid_evidence_ref"
			}
		}
		if code != "" {
			invalid = append(invalid, candidate)
			issues = append(issues, DecisionIssue{CandidateID: candidate.CandidateID, Code: code})
		} else {
			valid = append(valid, decision)
		}
	}
	return
}

func decisionContextSize(request DecisionRequest) int {
	data, _ := json.Marshal(request)
	return len(data)
}
