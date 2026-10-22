// Package source owns accepted submissions and the evidence derived from them.
//
// Content is stored exactly as valid JSON received from a client. A source may
// be explicitly redacted later; redaction keeps the identifier and status but
// removes the original payload and every evidence quote copied from it.
package source

import (
	"encoding/json"
	"time"
)

// Source is one accepted submission.
type Source struct {
	ID         string          `json:"id"`
	AppID      string          `json:"app_id,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	ScopeKey   string          `json:"scope_key,omitempty"`
	Content    json.RawMessage `json:"content"`
	Redacted   bool            `json:"redacted"`
	RedactedAt *time.Time      `json:"redacted_at,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Location is an optional, best-effort position of a quote inside the source
// content. Field is a dot-separated path such as "messages.0.content"; Start
// and End are rune offsets into the text at that path. A missing location is
// valid: the source association is still retained.
type Location struct {
	Field string `json:"field,omitempty"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Evidence links a source (and optionally one message inside it) to a quoted
// fragment. Quote and Location are cleared when the source is redacted.
type Evidence struct {
	ID         string     `json:"id"`
	SourceID   string     `json:"source_id"`
	MessageID  string     `json:"message_id,omitempty"`
	Quote      string     `json:"quote,omitempty"`
	Location   *Location  `json:"location,omitempty"`
	ModelName  string     `json:"model_name,omitempty"`
	Redacted   bool       `json:"redacted"`
	RedactedAt *time.Time `json:"redacted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// RedactResult reports the outcome of an explicit source redaction.
type RedactResult struct {
	SourceID      string `json:"id"`
	Redacted      bool   `json:"redacted"`
	CancelledJobs int64  `json:"cancelled_jobs"`
}
