package embedding

import (
	"errors"
	"time"
)

// ProviderError is the only error shape a provider adapter returns. Code is a
// stable, sanitized identifier safe to persist and log; it never contains the
// endpoint, credential, query text or model response.
type ProviderError struct {
	Code       string
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

// Error implements error using only the sanitized code.
func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return "embedding provider failure"
	}
	return "embedding provider: " + e.Code
}

// Unwrap exposes the internal cause to process-local debugging without writing
// it to storage or structured logs.
func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ErrorCode returns the stable code for persistence.
func ErrorCode(err error) string {
	var provider *ProviderError
	if errors.As(err, &provider) && provider.Code != "" {
		return provider.Code
	}
	if err == nil {
		return ""
	}
	return "internal"
}

// RetryAfter returns the provider's requested delay, or zero.
func RetryAfter(err error) time.Duration {
	var provider *ProviderError
	if errors.As(err, &provider) {
		return provider.RetryAfter
	}
	return 0
}

// IsRetryable reports whether the provider classified the failure as temporary.
func IsRetryable(err error) bool {
	var provider *ProviderError
	if errors.As(err, &provider) {
		return provider.Retryable
	}
	return false
}

// NewProviderError builds one sanitized adapter error. Input is deliberately not
// retained.
func NewProviderError(code string, retryable bool, cause error) *ProviderError {
	return &ProviderError{Code: code, Retryable: retryable, Err: cause}
}
