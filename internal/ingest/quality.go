package ingest

// ExtractionQualityError carries business-quality diagnostics alongside a
// failed extraction attempt. Worker persists the diagnostic fields without
// storing source text. Kind controls scheduling: protocol attempts consume the
// bounded protocol retry budget; transient attempts use normal backoff.
type ExtractionQualityError struct {
	Kind        string
	Quality     QualityOutcome
	Stats       ExtractionStats
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
		return "ingest: extraction observations unusable"
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
