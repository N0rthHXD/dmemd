package aggregation

import (
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

const (
	// DefaultDomain is the code-generated L2 domain used until an independent
	// classification result supplies narrower domains. The model never chooses
	// it.
	DefaultDomain = "general"
	// DefaultDebounce delays a next-layer rebuild after a lower-layer commit.
	DefaultDebounce = 10 * time.Second
	// TriggerDebounce delays the first L1 rebuild after an observation is
	// governed. Ordinary additions must not call a model in the write path.
	TriggerDebounce = 5 * time.Second
)

// L1Target returns the local aggregate target for one scope and bucket.
func L1Target(scopeKey, bucket string) artifact.Target {
	return artifact.Target{
		Layer:    artifact.LayerL1,
		Kind:     artifact.KindLocalSummary,
		ScopeKey: strings.TrimSpace(scopeKey),
		Bucket:   strings.TrimSpace(bucket),
		Topic:    DefaultDomain,
	}
}

// L1TargetForTime returns the local aggregate target whose code-generated time
// window contains at.
func L1TargetForTime(scopeKey string, at time.Time, bucketSize time.Duration) artifact.Target {
	return L1Target(scopeKey, artifact.L1BucketForTime(at, bucketSize))
}

// L2Target returns the domain aggregate target for one scope.
func L2Target(scopeKey string) artifact.Target {
	return L2TargetForDomain(scopeKey, DefaultDomain)
}

// L2TargetForDomain returns the domain aggregate target for one named domain.
func L2TargetForDomain(scopeKey, domain string) artifact.Target {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = DefaultDomain
	}
	return artifact.Target{
		Layer:    artifact.LayerL2,
		Kind:     artifact.KindTopicSummary,
		ScopeKey: strings.TrimSpace(scopeKey),
		Topic:    domain,
	}
}

// L3Target returns the scope-level aggregate target.
func L3Target(scopeKey string) artifact.Target {
	return artifact.Target{
		Layer:    artifact.LayerL3,
		Kind:     artifact.KindScopeSummary,
		ScopeKey: strings.TrimSpace(scopeKey),
	}
}

// NextTarget returns the derived target that consumes a committed layer. L1
// feeds L2, L2 feeds L3 and L3 has no automatic next layer.
func NextTarget(current artifact.Target) (artifact.Target, bool) {
	switch current.LayerOrDefault() {
	case artifact.LayerL1:
		if strings.TrimSpace(current.ScopeKey) == "" {
			return artifact.Target{}, false
		}
		return L2TargetForDomain(current.ScopeKey, current.Topic), true
	case artifact.LayerL2:
		if strings.TrimSpace(current.ScopeKey) == "" {
			return artifact.Target{}, false
		}
		return L3Target(current.ScopeKey), true
	default:
		return artifact.Target{}, false
	}
}

// JobKind returns the persistent aggregation task kind for one layer.
func JobKind(layer artifact.Layer) task.Kind {
	switch layer {
	case artifact.LayerL1:
		return task.KindAggregateL1
	case artifact.LayerL2:
		return task.KindAggregateL2
	case artifact.LayerL3:
		return task.KindAggregateL3
	case artifact.LayerL4:
		return task.KindBuildGuidance
	default:
		return ""
	}
}

// NormalizeTarget fills the code-owned metadata that is not encoded in a target
// key. It never changes the key.
func NormalizeTarget(target artifact.Target) artifact.Target {
	if target.Layer == "" {
		target.Layer = target.LayerOrDefault()
	}
	switch target.Layer {
	case artifact.LayerL1:
		target.Kind = artifact.KindLocalSummary
		if strings.TrimSpace(target.Topic) == "" {
			target.Topic = DefaultDomain
		}
	case artifact.LayerL2:
		if target.Kind == "" {
			target.Kind = artifact.KindTopicSummary
		}
	case artifact.LayerL3:
		target.Kind = artifact.KindScopeSummary
	}
	target.ScopeKey = strings.TrimSpace(target.ScopeKey)
	target.Topic = strings.TrimSpace(target.Topic)
	target.Bucket = strings.TrimSpace(target.Bucket)
	target.Key = strings.TrimSpace(target.Key)
	return target
}
