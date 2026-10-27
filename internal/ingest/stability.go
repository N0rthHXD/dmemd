package ingest

import (
	"context"
	"errors"
	"time"
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
		} else if errors.Is(err, ErrStateChanged) {
			code = "state_changed"
		}
	}
	return &ProcessingError{Stage: stage, Code: code, Err: err}
}

// modelCall applies a fresh per-call limit; repairs do not share a call timeout,
// but every call still shares the enclosing process budget.
func modelCall[T any](ctx context.Context, timeout time.Duration, call func(context.Context) (T, error)) (T, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := call(callCtx)
	if callCtx.Err() != nil {
		err = callCtx.Err()
	}
	return result, err
}
