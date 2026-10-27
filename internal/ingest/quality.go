package ingest

import (
	"context"
	"errors"
)

// ExtractionQualityError carries business-quality diagnostics alongside a
// failed extraction attempt. Worker persists the diagnostic fields without
// storing source text. Kind controls scheduling: protocol attempts consume the
// bounded protocol retry budget; transient attempts use normal backoff.
type ExtractionQualityError struct {
	Kind        string
	Quality     QualityOutcome
	Stats       ExtractionStats
	Retention   RetentionStats
	Timings     ProcessingTimings
	ModelUsage  ModelUsage
	Diagnostics []ExtractionDiagnostic
	Err         error
}

func (e *ExtractionQualityError) Error() string {
	if e == nil {
		return "ingest: extraction attempt failed"
	}
	if e.Quality == QualityUnusable {
		return "ingest: extraction candidates unusable"
	}
	return "ingest: extraction attempt failed"
}

func (e *ExtractionQualityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ExtractionQualityError) FailureKind() string {
	if e == nil || e.Kind == "" {
		return "transient"
	}
	return e.Kind
}

type extractionOutcome struct {
	Candidates  []Candidate
	Warnings    []string
	Stats       ExtractionStats
	Retention   RetentionStats
	Timings     ProcessingTimings
	Usage       ModelUsage
	Diagnostics []ExtractionDiagnostic
	quality     QualityOutcome
}

func (o *extractionOutcome) prepare() {
	if o.Warnings == nil {
		o.Warnings = []string{}
	}
	if o.Diagnostics == nil {
		o.Diagnostics = []ExtractionDiagnostic{}
	}
	if o.Candidates == nil {
		o.Candidates = []Candidate{}
	}
	o.Retention.Normalize()
	o.Warnings = uniqueWarnings(o.Warnings)
}

func (o extractionOutcome) finalQuality() QualityOutcome {
	switch {
	case o.Stats.RawCandidates == 0 && o.Stats.ParsedCandidates == 0:
		return QualityEmpty
	case o.Stats.IsolatedCandidates > 0, o.Stats.DroppedCandidates > 0, o.Stats.ExtractionChunkFailures > 0:
		return QualityPartial
	default:
		return QualityComplete
	}
}

func extractionDiagnostic(candidateID, field, code, stage, disposition string) ExtractionDiagnostic {
	return ExtractionDiagnostic{
		CandidateID: candidateID, Field: field, Code: code, Stage: stage, Disposition: disposition,
	}
}

func appendDiagnostic(target *[]ExtractionDiagnostic, seen map[ExtractionDiagnostic]struct{}, diagnostic ExtractionDiagnostic) {
	if _, exists := seen[diagnostic]; exists {
		return
	}
	seen[diagnostic] = struct{}{}
	*target = append(*target, diagnostic)
}

func appendNormalizationDiagnostics(target *[]ExtractionDiagnostic, seen map[ExtractionDiagnostic]struct{}, candidates []Candidate) {
	for _, candidate := range candidates {
		for _, normalization := range candidate.Normalizations {
			appendDiagnostic(target, seen, extractionDiagnostic(
				candidate.CandidateID, normalization.Field, normalization.Code, "normalize", "normalized",
			))
		}
	}
}

func countNormalized(candidates []Candidate) (candidateCount, normalizationCount int) {
	for _, candidate := range candidates {
		if len(candidate.Normalizations) == 0 {
			continue
		}
		candidateCount++
		normalizationCount += len(candidate.Normalizations)
	}
	return candidateCount, normalizationCount
}

// finalizeDiagnosticDispositions rewrites the diagnostic disposition to the
// final outcome for the candidate. Stage keeps whether the record came from
// normalization, validation or repair.
func finalizeDiagnosticDispositions(diagnostics []ExtractionDiagnostic, accepted, isolated, excluded, repaired map[string]struct{}) {
	for index := range diagnostics {
		id := diagnostics[index].CandidateID
		if id == "" {
			continue
		}
		switch {
		case containsID(excluded, id):
			diagnostics[index].Disposition = "excluded"
		case containsID(isolated, id):
			diagnostics[index].Disposition = "isolated"
		case containsID(repaired, id):
			diagnostics[index].Disposition = "repaired"
		case containsID(accepted, id):
			diagnostics[index].Disposition = "accepted"
		}
	}
}

func candidateIDSet(candidates []Candidate) map[string]struct{} {
	result := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		result[candidate.CandidateID] = struct{}{}
	}
	return result
}

func containsID(values map[string]struct{}, id string) bool {
	if values == nil {
		return false
	}
	_, ok := values[id]
	return ok
}

func repairFailureStats(stats *ExtractionStats, err error) {
	var provider *ProviderError
	switch {
	case err == nil:
		stats.RepairSucceeded++
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &provider) && provider.Operation == "timeout"):
		stats.RepairTimedOut++
		stats.RepairFailed++
	case errors.Is(err, ErrProtocol) || (errors.As(err, &provider) && provider.Operation == "protocol"):
		stats.RepairInvalidOutput++
		stats.RepairFailed++
	default:
		stats.RepairFailed++
	}
}

func repairFailureCode(err error) string {
	var provider *ProviderError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &provider) && provider.Operation == "timeout"):
		return "repair_timeout"
	case errors.Is(err, ErrProtocol) || (errors.As(err, &provider) && provider.Operation == "protocol"):
		return "repair_invalid_output"
	default:
		return "repair_failed"
	}
}

// extractionFailure wraps a failed stage with the source-free extraction
// diagnostics accumulated so far. The worker persists these fields even when
// the attempt never reaches the commit transaction.
func extractionFailure(outcome extractionOutcome, err error) error {
	var existing *ExtractionQualityError
	if errors.As(err, &existing) {
		return err
	}
	return &ExtractionQualityError{
		Kind: failureKind(err), Stats: outcome.Stats, Retention: outcome.Retention,
		Timings: outcome.Timings, ModelUsage: outcome.Usage,
		Diagnostics: outcome.Diagnostics, Err: err,
	}
}
