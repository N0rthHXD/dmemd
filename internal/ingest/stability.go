package ingest

import (
	"context"
	"errors"
)

// ProcessingError carries stable diagnostics, never model output or source text.
type ProcessingError struct {
	Stage string
	Code  string
	Err   error
}

func (e *ProcessingError) Error() string { return e.Stage + ": " + e.Code }
func (e *ProcessingError) Unwrap() error { return e.Err }

func processingError(stage, code string, err error) error {
	if err == nil {
		return nil
	}
	var existing *ProcessingError
	if errors.As(err, &existing) {
		stage = existing.Stage
		if code == "" {
			return err
		}
	}
	if code == "" {
		code = failureKind(err)
		var provider *ProviderError
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &provider) && provider.Operation == "timeout") {
			code = "model_timeout"
		}
	}
	return &ProcessingError{Stage: stage, Code: code, Err: err}
}
