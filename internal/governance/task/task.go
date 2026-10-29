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
	KindGovernObservation    Kind = "govern_observation"
	KindClassifyObservation  Kind = "classify_observation"
	KindAggregateL1          Kind = "aggregate_l1"
	KindAggregateL2          Kind = "aggregate_l2"
	KindAggregateL3          Kind = "aggregate_l3"
	KindBuildGuidance        Kind = "build_guidance"
	KindReviewConflict       Kind = "review_conflict"
	KindInvalidateDependency Kind = "invalidate_dependency"
	KindReconcile            Kind = "reconcile"
)

// Known reports whether kind is part of the fixed governance-task set.
func (k Kind) Known() bool {
	switch k {
	case KindGovernObservation, KindClassifyObservation,
		KindAggregateL1, KindAggregateL2, KindAggregateL3,
		KindBuildGuidance, KindReviewConflict, KindInvalidateDependency,
		KindReconcile:
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

// ErrRevisionChanged means another transaction changed the task target after it
// was claimed. Handlers return it so the worker discards their result instead of
// overwriting newer state.
var ErrRevisionChanged = errors.New("governance task revision changed")

// Tx is the transaction-scoped task capability used by business writers.
type Tx interface {
	UpsertJob(ctx context.Context, input UpsertInput) (Job, bool, error)
	CancelJob(ctx context.Context, kind Kind, targetKey string, now time.Time) (bool, error)
	GetGovernanceJobByTarget(ctx context.Context, kind Kind, targetKey string) (Job, error)
}

// QueueTx is the complete task capability used by the governance and
// aggregation workers. The *ForKinds variants scope every operation to the
// task types owned by one worker; an empty kind list means all task types.
type QueueTx interface {
	Tx
	GetGovernanceJob(ctx context.Context, id string) (Job, error)
	ClaimNextGovernanceJob(ctx context.Context, now time.Time) (Job, bool, error)
	ClaimNextGovernanceJobForKinds(ctx context.Context, now time.Time, kinds []Kind) (Job, bool, error)
	RequeueRunningGovernanceJobs(ctx context.Context, now time.Time) (int64, error)
	RequeueRunningGovernanceJobsForKinds(ctx context.Context, now time.Time, kinds []Kind) (int64, error)
	RequeueWaitingConfigurationGovernanceJobs(ctx context.Context, now time.Time) (int64, error)
	RequeueWaitingConfigurationGovernanceJobsForKinds(ctx context.Context, now time.Time, kinds []Kind) (int64, error)
	RequeueProtocolFailedGovernanceJobs(ctx context.Context, now time.Time) (int64, error)
	RequeueProtocolFailedGovernanceJobsForKinds(ctx context.Context, now time.Time, kinds []Kind) (int64, error)
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

const (
	observationPrefix = "observation:"
	l1Prefix          = "l1:"
	l2Prefix          = "l2:"
	l3Prefix          = "l3:"
)

// ObservationKey is the canonical target key for one observation governance
// task. Observation identifiers are service-generated UUIDs.
func ObservationKey(id string) string {
	return observationPrefix + encodeKeyPart(id)
}

// ParseObservationKey extracts the observation identifier from a governance
// target key.
func ParseObservationKey(targetKey string) (observationID string, ok bool) {
	if !strings.HasPrefix(targetKey, observationPrefix) {
		return "", false
	}
	observationID, err := decodeKeyPart(strings.TrimPrefix(targetKey, observationPrefix))
	if err != nil || observationID == "" {
		return "", false
	}
	return observationID, true
}

// L1TargetKey addresses one local aggregate bucket inside a scope.
func L1TargetKey(scopeKey, bucket string) string {
	return l1Prefix + encodeKeyPart(scopeKey) + ":" + encodeKeyPart(bucket)
}

// L2TargetKey addresses one domain aggregate inside a scope.
func L2TargetKey(scopeKey, domain string) string {
	return l2Prefix + encodeKeyPart(scopeKey) + ":" + encodeKeyPart(domain)
}

// L3TargetKey addresses one scope-level aggregate.
func L3TargetKey(scopeKey string) string {
	return l3Prefix + encodeKeyPart(scopeKey)
}

// ParseL1TargetKey extracts the scope and bucket from a local aggregate key.
func ParseL1TargetKey(targetKey string) (scopeKey, bucket string, ok bool) {
	return parseScopedKey(l1Prefix, targetKey)
}

// ParseL2TargetKey extracts the scope and domain from a domain aggregate key.
func ParseL2TargetKey(targetKey string) (scopeKey, domain string, ok bool) {
	return parseScopedKey(l2Prefix, targetKey)
}

// ParseL3TargetKey extracts the scope from a scope-level aggregate key.
func ParseL3TargetKey(targetKey string) (scopeKey string, ok bool) {
	if !strings.HasPrefix(targetKey, l3Prefix) {
		return "", false
	}
	scopeKey, err := decodeKeyPart(strings.TrimPrefix(targetKey, l3Prefix))
	if err != nil || scopeKey == "" {
		return "", false
	}
	return scopeKey, true
}

func parseScopedKey(prefix, targetKey string) (scopeKey, value string, ok bool) {
	if !strings.HasPrefix(targetKey, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(targetKey, prefix), ":")
	if len(parts) != 2 {
		return "", "", false
	}
	scopeKey, err := decodeKeyPart(parts[0])
	if err != nil || scopeKey == "" {
		return "", "", false
	}
	value, err = decodeKeyPart(parts[1])
	if err != nil || value == "" {
		return "", "", false
	}
	return scopeKey, value, true
}

// AggregateKindForTargetKey reports the layer-specific aggregation task kind for
// a canonical L1/L2/L3 target key.
func AggregateKindForTargetKey(targetKey string) (Kind, bool) {
	switch {
	case strings.HasPrefix(targetKey, l1Prefix):
		return KindAggregateL1, true
	case strings.HasPrefix(targetKey, l2Prefix):
		return KindAggregateL2, true
	case strings.HasPrefix(targetKey, l3Prefix):
		return KindAggregateL3, true
	default:
		return "", false
	}
}

// DerivedKey addresses one derived-memory target inside a scope. The layer is
// kept as a string-like value so this package does not import artifact (which
// imports this package for its own target keys).
func DerivedKey[L ~string](layer L, scopeKey, key string) string {
	switch string(layer) {
	case "l1":
		return L1TargetKey(scopeKey, key)
	case "l2":
		return L2TargetKey(scopeKey, key)
	case "l3":
		return L3TargetKey(scopeKey)
	default:
		prefix := strings.TrimSpace(string(layer))
		if prefix == "" {
			prefix = "derived"
		}
		return prefix + ":" + encodeKeyPart(scopeKey) + ":" + encodeKeyPart(key)
	}
}

// ParseDerivedKey extracts the scope and key of a derived target that uses the
// generic layer-prefixed encoding. L1/L2 delegate to their scoped parsers; L3
// has no second key part.
func ParseDerivedKey[L ~string](layer L, targetKey string) (scopeKey, key string, ok bool) {
	switch string(layer) {
	case "l1":
		scopeKey, key, ok = ParseL1TargetKey(targetKey)
		return scopeKey, key, ok
	case "l2":
		scopeKey, key, ok = ParseL2TargetKey(targetKey)
		return scopeKey, key, ok
	case "l3":
		scopeKey, ok = ParseL3TargetKey(targetKey)
		return scopeKey, "", ok
	default:
		prefix := strings.TrimSpace(string(layer))
		if prefix == "" || !strings.HasPrefix(targetKey, prefix+":") {
			return "", "", false
		}
		parts := strings.Split(strings.TrimPrefix(targetKey, prefix+":"), ":")
		if len(parts) != 2 {
			return "", "", false
		}
		scopeKey, err := decodeKeyPart(parts[0])
		if err != nil || scopeKey == "" {
			return "", "", false
		}
		key, err = decodeKeyPart(parts[1])
		if err != nil || key == "" {
			return "", "", false
		}
		return scopeKey, key, true
	}
}

// ReconcileKey is the single global reconcile target.
const ReconcileKey = "global"

const (
	skillPrefix      = "skill:"
	experiencePrefix = "experience:"
	conflictPrefix   = "conflict:"
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
