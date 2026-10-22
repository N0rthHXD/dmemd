// Package task owns the persistent governance-task vocabulary shared by the
// write path, storage and the governance worker. It deliberately has no
// dependency on memory or storage so business writers can enqueue tasks in the
// same transaction that saves their own changes.
package task

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind is one fixed governance task type. The first release dispatches these
// through a small handler map rather than a general policy engine.
type Kind string

const (
	KindClassify       Kind = "classify"
	KindReviewConflict Kind = "review_conflict"
	KindAggregate      Kind = "aggregate"
	KindBuildSkill     Kind = "build_skill"
	KindReconcile      Kind = "reconcile"
)

// Known reports whether kind is part of the fixed first-release set.
func (k Kind) Known() bool {
	switch k {
	case KindClassify, KindReviewConflict, KindAggregate, KindBuildSkill, KindReconcile:
		return true
	default:
		return false
	}
}

// Status is the persistent task state.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether a task can no longer be claimed.
func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Wait reasons are fixed codes and never contain model or source text.
const (
	WaitConfiguration = "configuration"
	WaitTransient     = "transient"
	WaitProtocol      = "protocol"
	WaitRateLimited   = "rate_limited"
)

// Stable error codes written to last_error_code.
const (
	ErrorConfiguration = "configuration"
	ErrorProtocol      = "protocol"
	ErrorRateLimited   = "rate_limited"
	ErrorTransient     = "transient"
)

// Job is one persisted governance target. Revision protects the result of an
// in-flight execution: input changes and cancellation increment it, so a stale
// result cannot overwrite newer state.
type Job struct {
	ID                  string     `json:"id"`
	Kind                Kind       `json:"kind"`
	TargetKey           string     `json:"target_key"`
	Revision            int64      `json:"revision"`
	Status              Status     `json:"status"`
	Attempts            int        `json:"attempts"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	NextAttemptAt       *time.Time `json:"next_attempt_at,omitempty"`
	LastErrorCode       string     `json:"last_error_code,omitempty"`
	WaitReason          string     `json:"wait_reason,omitempty"`
	InputHash           string     `json:"input_hash,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

// UpsertInput registers or refreshes one target. InputHash suppresses repeated
// work for exactly the same input; NextAttemptAt carries the debounce deadline.
type UpsertInput struct {
	Kind          Kind
	TargetKey     string
	InputHash     string
	NextAttemptAt *time.Time
	Now           time.Time
}

// RescheduleInput updates a claimed task without touching its result objects.
type RescheduleInput struct {
	Status              Status
	NextAttemptAt       *time.Time
	LastErrorCode       string
	WaitReason          string
	ConsecutiveFailures int
	Now                 time.Time
}

// ErrNotFound is returned for an unknown task identifier.
var ErrNotFound = errors.New("governance task not found")

// Tx is the transaction-scoped task capability used by business writers.
type Tx interface {
	UpsertJob(ctx context.Context, input UpsertInput) (Job, bool, error)
	CancelJob(ctx context.Context, kind Kind, targetKey string, now time.Time) (bool, error)
	GetGovernanceJobByTarget(ctx context.Context, kind Kind, targetKey string) (Job, error)
}

// QueueTx is the complete task capability used by the governance worker.
type QueueTx interface {
	Tx
	GetGovernanceJob(ctx context.Context, id string) (Job, error)
	ClaimNextGovernanceJob(ctx context.Context, now time.Time) (Job, bool, error)
	RequeueRunningGovernanceJobs(ctx context.Context, now time.Time) (int64, error)
	RequeueWaitingConfigurationGovernanceJobs(ctx context.Context, now time.Time) (int64, error)
	RequeueProtocolFailedGovernanceJobs(ctx context.Context, now time.Time) (int64, error)
	RequeueStaleRunningGovernanceJobs(ctx context.Context, olderThan, now time.Time) (int64, error)
	BumpSkillJobsForScope(ctx context.Context, scopeKey string, now time.Time) (int64, error)
	ListMemoryIDsMissingJob(ctx context.Context, kind Kind, afterID string, limit int) ([]string, error)
	FinishJob(ctx context.Context, id string, revision int64, now time.Time) (bool, error)
	RescheduleJob(ctx context.Context, id string, revision int64, input RescheduleInput) (bool, error)
}

// MemoryKey is the canonical target key for one atomic memory. Atomic memory
// identifiers are UUIDs assigned by the service, so the kind plus this value is
// unambiguous.
func MemoryKey(memoryID string) string {
	return memoryID
}

// ReconcileKey is the single global reconcile target.
const ReconcileKey = "global"

const (
	aggregateSummaryPrefix = "summary:"
	aggregateProfilePrefix = "profile:"
	skillPrefix            = "skill:"
	experiencePrefix       = "experience:"
	conflictPrefix         = "conflict:"
)

// ConflictKey is the canonical target key for one open-conflict review. Conflict
// identifiers are service-generated UUIDs, so the kind plus this value is
// unambiguous.
func ConflictKey(conflictID string) string {
	return conflictPrefix + encodeKeyPart(conflictID)
}

// ParseConflictKey extracts the conflict identifier from a review target.
func ParseConflictKey(targetKey string) (conflictID string, ok bool) {
	if !strings.HasPrefix(targetKey, conflictPrefix) {
		return "", false
	}
	conflictID, err := decodeKeyPart(strings.TrimPrefix(targetKey, conflictPrefix))
	if err != nil || conflictID == "" {
		return "", false
	}
	return conflictID, true
}

// AggregateKey is the canonical target key for one (scope, topic) summary. Both
// parts are length-delimiting base64url values, so no concatenation ambiguity
// can merge two groups.
func AggregateKey(scopeKey, topic string) string {
	return aggregateSummaryPrefix + encodeKeyPart(scopeKey) + ":" + encodeKeyPart(topic)
}

// ProfileKey is the canonical target key for one scope profile.
func ProfileKey(scopeKey string) string {
	return aggregateProfilePrefix + encodeKeyPart(scopeKey)
}

// ParseAggregateKey extracts the scope and topic from a canonical summary key.
func ParseAggregateKey(targetKey string) (scopeKey, topic string, ok bool) {
	if !strings.HasPrefix(targetKey, aggregateSummaryPrefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(targetKey, aggregateSummaryPrefix), ":")
	if len(parts) != 2 {
		return "", "", false
	}
	scopeKey, err := decodeKeyPart(parts[0])
	if err != nil {
		return "", "", false
	}
	topic, err = decodeKeyPart(parts[1])
	if err != nil {
		return "", "", false
	}
	if scopeKey == "" || topic == "" {
		return "", "", false
	}
	return scopeKey, topic, true
}

// ParseProfileKey extracts the scope from a canonical profile key.
func ParseProfileKey(targetKey string) (scopeKey string, ok bool) {
	if !strings.HasPrefix(targetKey, aggregateProfilePrefix) {
		return "", false
	}
	scopeKey, err := decodeKeyPart(strings.TrimPrefix(targetKey, aggregateProfilePrefix))
	if err != nil || scopeKey == "" {
		return "", false
	}
	return scopeKey, true
}

// SkillTargetPrefix returns the canonical target-key prefix shared by every
// skill in one scope. It is used for targeted invalidation, never for parsing.
func SkillTargetPrefix(scopeKey string) string {
	return skillPrefix + encodeKeyPart(scopeKey) + ":"
}

// SkillKey is the canonical target key for one scope/topic guidance skill. Both
// parts are length-delimiting base64url values; an empty topic is valid and
// means the scope-level skill for procedures without topic labels.
func SkillKey(scopeKey, topic string) string {
	return skillPrefix + encodeKeyPart(scopeKey) + ":" + encodeKeyPart(topic)
}

// ParseSkillKey extracts the scope and topic from a canonical skill key.
func ParseSkillKey(targetKey string) (scopeKey, topic string, ok bool) {
	if !strings.HasPrefix(targetKey, skillPrefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(targetKey, skillPrefix), ":")
	if len(parts) != 2 {
		return "", "", false
	}
	scopeKey, err := decodeKeyPart(parts[0])
	if err != nil || scopeKey == "" {
		return "", "", false
	}
	topic, err = decodeKeyPart(parts[1])
	if err != nil {
		return "", "", false
	}
	return scopeKey, topic, true
}

// ExperienceKey is the canonical target key for one experience case built from
// exactly one recorded application result.
func ExperienceKey(experienceID string) string {
	return experiencePrefix + encodeKeyPart(experienceID)
}

// ParseExperienceKey extracts the internal experience-run identifier.
func ParseExperienceKey(targetKey string) (experienceID string, ok bool) {
	if !strings.HasPrefix(targetKey, experiencePrefix) {
		return "", false
	}
	experienceID, err := decodeKeyPart(strings.TrimPrefix(targetKey, experiencePrefix))
	if err != nil || experienceID == "" {
		return "", false
	}
	return experienceID, true
}

func encodeKeyPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeKeyPart(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("decode target key: %w", err)
	}
	return string(decoded), nil
}

// InputHash hashes ordered, already-normalized parts into a stable digest.
func InputHash(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
