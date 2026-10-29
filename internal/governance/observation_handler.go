package governance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	defaultObservationCandidates = 16
	defaultObservationTimeout    = time.Minute
	maxEvidenceHintRunes         = 240
	// maxRuleSourceObservations bounds the deterministic duplicate scan for one
	// source. Duplicate statements are almost always recent, so the scan walks
	// the source observations newest first.
	maxRuleSourceObservations = 256
	maxRulePriorMatches       = 32
)

// AggregationSink registers the derived targets affected by one governed
// observation. The handler never calls an aggregation model; the sink only
// records lower-layer work in the same transaction.
type AggregationSink interface {
	ObservationGoverned(ctx context.Context, tx Tx, observation observation.Observation, memoryIDs []string) error
}

// AggregationSinkFunc adapts one function to the aggregation sink. It lets the
// runtime bridge the governance transaction to the layer-aggregation trigger
// without making either package depend on the other.
type AggregationSinkFunc func(context.Context, Tx, observation.Observation, []string) error

// ObservationGoverned implements AggregationSink.
func (f AggregationSinkFunc) ObservationGoverned(ctx context.Context, tx Tx,
	item observation.Observation, memoryIDs []string) error {
	return f(ctx, tx, item, memoryIDs)
}

// ObservationGovernanceHandler turns one L0 observation into a typed relation
// outcome and a current canonical view. Deterministic identity rules run before
// any model call; the model only receives the remaining ambiguous same-scope
// candidate set.
type ObservationGovernanceHandler struct {
	reader        ObservationGovernanceReader
	store         Store
	materializer  MemoryMaterializer
	provider      RelationProvider
	limiter       *Limiter
	sink          AggregationSink
	now           func() time.Time
	timeout       time.Duration
	maxCandidates int
	logger        *slog.Logger
}

// ObservationGovernanceOption customizes the observation handler.
type ObservationGovernanceOption func(*ObservationGovernanceHandler)

// WithObservationGovernanceClock overrides the handler clock.
func WithObservationGovernanceClock(now func() time.Time) ObservationGovernanceOption {
	return func(handler *ObservationGovernanceHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

// WithObservationGovernanceTimeout bounds one relation model call.
func WithObservationGovernanceTimeout(timeout time.Duration) ObservationGovernanceOption {
	return func(handler *ObservationGovernanceHandler) {
		if timeout > 0 {
			handler.timeout = timeout
		}
	}
}

// WithObservationGovernanceMaxCandidates bounds the same-scope candidate set.
func WithObservationGovernanceMaxCandidates(limit int) ObservationGovernanceOption {
	return func(handler *ObservationGovernanceHandler) {
		if limit > 0 {
			handler.maxCandidates = limit
		}
	}
}

// WithObservationGovernanceLogger installs a content-free structured logger.
func WithObservationGovernanceLogger(logger *slog.Logger) ObservationGovernanceOption {
	return func(handler *ObservationGovernanceHandler) {
		if logger != nil {
			handler.logger = logger
		}
	}
}

// WithObservationAggregationSink registers the next-stage aggregation work.
func WithObservationAggregationSink(sink AggregationSink) ObservationGovernanceOption {
	return func(handler *ObservationGovernanceHandler) {
		if sink != nil {
			handler.sink = sink
		}
	}
}

// NewObservationGovernanceHandler assembles the L0 governance processor.
func NewObservationGovernanceHandler(reader ObservationGovernanceReader, store Store,
	materializer MemoryMaterializer, provider RelationProvider, limiter *Limiter,
	options ...ObservationGovernanceOption) *ObservationGovernanceHandler {
	handler := &ObservationGovernanceHandler{
		reader:        reader,
		store:         store,
		materializer:  materializer,
		provider:      provider,
		limiter:       limiter,
		now:           time.Now,
		timeout:       defaultObservationTimeout,
		maxCandidates: defaultObservationCandidates,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

type candidateState struct {
	Ref       string
	Memory    memory.Memory
	Refs      []string
	Relations []Relation
}

// Process implements Handler.
func (h *ObservationGovernanceHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindGovernObservation {
		return fmt.Errorf("observation governance handler received kind %s", job.Kind)
	}
	if h.reader == nil || h.store == nil || h.materializer == nil {
		return errors.New("governance: observation handler dependencies not configured")
	}
	observationID, ok := task.ParseObservationKey(job.TargetKey)
	if !ok {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false,
			Err: fmt.Errorf("invalid observation target %q", job.TargetKey)}
	}
	now := h.now().UTC()
	current, err := h.reader.GetObservation(ctx, observationID)
	if errors.Is(err, observation.ErrNotFound) {
		return CancelMissingTask(ctx, h.store, job, now)
	}
	if err != nil {
		return fmt.Errorf("读取待治理观察: %w", err)
	}
	if current.Status == observation.StatusDeleted || current.Status == observation.StatusRetracted {
		return CancelMissingTask(ctx, h.store, job, now)
	}
	src, err := h.reader.GetSource(ctx, current.SourceID)
	if errors.Is(err, source.ErrNotFound) {
		return CancelMissingTask(ctx, h.store, job, now)
	}
	if err != nil {
		return fmt.Errorf("读取观察来源: %w", err)
	}
	if src.Redacted {
		return CancelMissingTask(ctx, h.store, job, now)
	}

	evidence, err := h.reader.ListObservationEvidence(ctx, current.ID)
	if err != nil {
		return err
	}
	candidates, err := h.loadCandidates(ctx, current)
	if err != nil {
		return err
	}
	ruleInput, err := h.buildRuleInput(ctx, current, evidence, candidates)
	if err != nil {
		return err
	}
	decision := MatchRules(ruleInput)
	if decision.Skip {
		h.logger.Debug("observation governance idempotent", "observation_id", current.ID,
			"reason", decision.Reason)
		return h.finishIdempotent(ctx, job, observationID)
	}

	var outcomes []relationOutcome
	if decision.Matched {
		h.logger.Debug("observation governance rule matched", "observation_id", current.ID,
			"kind", decision.Kind, "reason", decision.Reason)
		outcomes = []relationOutcome{{Kind: decision.Kind, TargetID: decision.TargetID}}
	} else {
		request, refIDs := h.buildRequest(ctx, current, evidence, candidates)
		proposals, usage, err := h.relate(ctx, request, current)
		if err != nil {
			return err
		}
		if usage.Calls > 0 {
			h.logger.Debug("observation relation model completed",
				"observation_id", current.ID, "candidates", len(candidates), "relations", len(proposals))
		}
		outcomes = make([]relationOutcome, 0, len(proposals))
		for _, proposal := range proposals {
			outcome := relationOutcome{Kind: proposal.Kind}
			if targetID, ok := refIDs[proposal.ToRef]; ok {
				outcome.TargetID = targetID
			}
			if outcome.TargetID == "" {
				switch proposal.Kind {
				case RelationUnresolved, RelationIndependent, RelationSeparate:
				default:
					outcome.Kind = RelationUnresolved
				}
			}
			outcomes = append(outcomes, outcome)
		}
		if len(outcomes) == 0 {
			outcomes = append(outcomes, relationOutcome{Kind: RelationUnresolved})
		}
	}

	now = h.now().UTC()
	return h.commit(ctx, job, observationID, outcomes, now)
}

type relationOutcome struct {
	Kind     RelationKind
	TargetID string
}

// finishIdempotent completes a retry whose observation was already materialized.
// The conditional finish still loses safely against a newer task revision, and a
// source redacted after the pre-check cancels the task instead of completing it.
func (h *ObservationGovernanceHandler) finishIdempotent(ctx context.Context, job task.Job, observationID string) error {
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		now := h.now().UTC()
		latest, err := tx.GetObservation(ctx, observationID)
		if err != nil {
			if errors.Is(err, observation.ErrNotFound) {
				_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
				return cancelErr
			}
			return err
		}
		if latest.Status == observation.StatusDeleted || latest.Status == observation.StatusRetracted {
			_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
			return cancelErr
		}
		src, err := tx.GetSource(ctx, latest.SourceID)
		if err != nil {
			if errors.Is(err, source.ErrNotFound) {
				_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
				return cancelErr
			}
			return err
		}
		if src.Redacted {
			_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
			return cancelErr
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

// commit revalidates every target, persists the relation facts, materializes the
// primary canonical change, registers aggregation work and finishes the task in
// one short transaction.
func (h *ObservationGovernanceHandler) commit(ctx context.Context, job task.Job, observationID string,
	outcomes []relationOutcome, now time.Time) error {
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		latest, err := tx.GetObservation(ctx, observationID)
		if err != nil {
			if errors.Is(err, observation.ErrNotFound) {
				_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
				return cancelErr
			}
			return err
		}
		if latest.Status == observation.StatusDeleted || latest.Status == observation.StatusRetracted {
			_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
			return cancelErr
		}
		latestSource, err := tx.GetSource(ctx, latest.SourceID)
		if err != nil {
			if errors.Is(err, source.ErrNotFound) {
				_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
				return cancelErr
			}
			return err
		}
		if latestSource.Redacted {
			_, cancelErr := tx.CancelJob(ctx, job.Kind, job.TargetKey, now)
			return cancelErr
		}

		targets := make(map[string]memory.Memory)
		for index := range outcomes {
			if outcomes[index].TargetID == "" {
				continue
			}
			item, err := tx.GetMemory(ctx, outcomes[index].TargetID)
			if errors.Is(err, memory.ErrNotFound) {
				outcomes[index].Kind = RelationUnresolved
				outcomes[index].TargetID = ""
				continue
			}
			if err != nil {
				return err
			}
			// Rule 4: a deleted or rejected target is never silently
			// reactivated. The observation stays unresolved and may become a
			// pending view only.
			if item.State == memory.StateDeleted || item.AssertionStatus == memory.AssertionRejected {
				outcomes[index].Kind = RelationUnresolved
				outcomes[index].TargetID = ""
				continue
			}
			targets[item.ID] = item
		}
		if len(outcomes) == 0 {
			outcomes = append(outcomes, relationOutcome{Kind: RelationUnresolved})
		}

		for _, outcome := range outcomes {
			target, ok := targets[outcome.TargetID]
			if !ok {
				continue
			}
			relation := Relation{
				ID:        idgen.NewUUID(),
				Kind:      outcome.Kind,
				FromType:  EndpointObservation,
				FromID:    latest.ID,
				ToType:    EndpointMemory,
				ToID:      target.ID,
				ToVersion: target.Version,
				CreatedAt: now,
			}
			if err := relation.Validate(); err != nil {
				return err
			}
			if err := tx.InsertGovernanceRelation(ctx, relation); err != nil {
				return err
			}
		}

		primary := selectPrimaryOutcome(outcomes)
		var target *memory.Memory
		if item, ok := targets[primary.TargetID]; ok {
			targetCopy := item
			target = &targetCopy
		}
		result, err := h.materializer.Apply(ctx, tx, MaterializeInput{
			Observation: latest,
			Relation:    Relation{Kind: primary.Kind, FromType: EndpointObservation, FromID: latest.ID},
			Target:      target,
		})
		if err != nil {
			return err
		}
		if h.sink != nil && len(result.MemoryIDs) > 0 {
			if err := h.sink.ObservationGoverned(ctx, tx, latest, result.MemoryIDs); err != nil {
				return err
			}
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, h.now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

func selectPrimaryOutcome(outcomes []relationOutcome) relationOutcome {
	rank := func(kind RelationKind) int {
		switch kind {
		case RelationSupersedes:
			return 7
		case RelationContradicts:
			return 6
		case RelationEnriches:
			return 5
		case RelationDuplicate:
			return 4
		case RelationSupports:
			return 3
		case RelationSeparate:
			return 2
		case RelationIndependent:
			return 1
		default:
			return 0
		}
	}
	best := outcomes[0]
	for _, outcome := range outcomes[1:] {
		if rank(outcome.Kind) > rank(best.Kind) {
			best = outcome
		}
	}
	return best
}

func (h *ObservationGovernanceHandler) loadCandidates(ctx context.Context, current observation.Observation) ([]candidateState, error) {
	items, err := h.reader.ListMemoryCandidates(ctx, current.ScopeKey, h.maxCandidates)
	if err != nil {
		return nil, err
	}
	candidates := make([]candidateState, 0, len(items))
	for index, item := range items {
		refs, err := h.reader.ListMemoryObservationRefs(ctx, item.ID, item.Version)
		if err != nil {
			return nil, err
		}
		relations, err := h.reader.ListGovernanceRelationsForEndpoint(ctx, EndpointMemory, item.ID)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidateState{
			Ref:       fmt.Sprintf("existing_%d", index+1),
			Memory:    item,
			Refs:      refs,
			Relations: relations,
		})
	}
	return candidates, nil
}

// buildRuleInput gathers deterministic identity material: current memory refs,
// same-source prior observations and the bounded current candidates.
func (h *ObservationGovernanceHandler) buildRuleInput(ctx context.Context, current observation.Observation,
	evidence []source.Evidence, candidates []candidateState) (RuleInput, error) {
	input := RuleInput{
		Observation:  current,
		Evidence:     evidence,
		MemoriesByID: make(map[string]memory.Memory, len(candidates)),
	}
	for _, candidate := range candidates {
		input.Candidates = append(input.Candidates, RuleCandidate{
			Ref:            candidate.Ref,
			Memory:         candidate.Memory,
			ObservationIDs: candidate.Refs,
		})
		input.MemoriesByID[candidate.Memory.ID] = candidate.Memory
	}
	existing, err := h.reader.ListMemoryIDsForObservation(ctx, current.ID)
	if err != nil {
		return RuleInput{}, err
	}
	input.ExistingMemoryIDs = existing
	if len(existing) > 0 {
		return input, nil
	}

	prior, err := h.reader.ListObservationsBySource(ctx, current.SourceID)
	if err != nil {
		return RuleInput{}, err
	}
	body := normalizeBody(current.Content)
	scanned := 0
	matches := 0
	for index := len(prior) - 1; index >= 0; index-- {
		if scanned >= maxRuleSourceObservations || matches >= maxRulePriorMatches {
			break
		}
		item := prior[index]
		if item.ID == "" || item.ID == current.ID {
			continue
		}
		if item.Status == observation.StatusDeleted || item.Status == observation.StatusRetracted {
			continue
		}
		scanned++
		if body == "" || normalizeBody(item.Content) != body {
			continue
		}
		itemEvidence, err := h.reader.ListObservationEvidence(ctx, item.ID)
		if err != nil {
			return RuleInput{}, err
		}
		memoryIDs, err := h.reader.ListMemoryIDsForObservation(ctx, item.ID)
		if err != nil {
			return RuleInput{}, err
		}
		for _, memoryID := range memoryIDs {
			if _, ok := input.MemoriesByID[memoryID]; ok {
				continue
			}
			item, err := h.reader.GetMemory(ctx, memoryID)
			if errors.Is(err, memory.ErrNotFound) {
				continue
			}
			if err != nil {
				return RuleInput{}, err
			}
			input.MemoriesByID[memoryID] = item
		}
		input.PriorObservations = append(input.PriorObservations, RuleObservation{
			Observation: item,
			SegmentIDs:  evidenceSegmentIDs(itemEvidence),
			MemoryIDs:   memoryIDs,
		})
		matches++
	}
	return input, nil
}

func (h *ObservationGovernanceHandler) buildRequest(ctx context.Context, current observation.Observation,
	evidence []source.Evidence, candidates []candidateState) (RelationRequest, map[string]string) {
	request := RelationRequest{
		NewObservation: RelationCandidate{
			Ref:          "new",
			Type:         string(EndpointObservation),
			Kind:         string(current.Kind),
			ScopeKey:     current.ScopeKey,
			Content:      current.Content,
			EvidenceHint: evidenceHint(evidence),
		},
		Existing: make([]RelationCandidate, 0, len(candidates)),
	}
	refIDs := make(map[string]string, len(candidates))
	known := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		hint := ""
		if len(candidate.Refs) > 0 {
			if item, err := h.reader.GetObservation(ctx, candidate.Refs[0]); err == nil {
				hint = truncateRunes(strings.TrimSpace(item.Content), maxEvidenceHintRunes)
			}
		}
		request.Existing = append(request.Existing, RelationCandidate{
			Ref:          candidate.Ref,
			Type:         string(EndpointMemory),
			Kind:         string(candidate.Memory.Kind),
			ScopeKey:     candidate.Memory.ScopeKey,
			Content:      candidate.Memory.Content,
			EvidenceHint: hint,
		})
		refIDs[candidate.Ref] = candidate.Memory.ID
		for _, relation := range candidate.Relations {
			known = append(known, string(relation.Kind))
		}
	}
	if len(candidates) > 0 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.Memory.ID)
		}
		if conflicts, err := h.reader.ListOpenConflictsForMemories(ctx, ids); err == nil {
			for _, conflict := range conflicts {
				known = append(known, "open_conflict:"+string(conflict.ReasonCode))
			}
		}
	}
	sort.Strings(known)
	request.KnownRelations = uniqueStrings(known)
	return request, refIDs
}

func (h *ObservationGovernanceHandler) relate(ctx context.Context, request RelationRequest,
	current observation.Observation) ([]RelationProposal, ingest.ModelUsage, error) {
	if h.provider == nil {
		return nil, ingest.ModelUsage{}, &ingest.ProviderError{
			Operation: "configuration", Retryable: true,
			Err: errors.New("关系模型未配置"),
		}
	}
	now := h.now().UTC()
	if h.limiter != nil {
		allowed, next := h.limiter.Acquire(now)
		if !allowed {
			return nil, ingest.ModelUsage{}, &RetryError{At: next, Code: task.ErrorRateLimited, Reason: task.WaitRateLimited}
		}
	}
	callCtx := ctx
	cancel := func() {}
	if h.timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, h.timeout)
	}
	started := time.Now()
	result, err := h.provider.Relate(callCtx, request)
	cancel()
	if h.limiter != nil {
		h.limiter.Record("relation", len(request.Existing), len(current.Content), started, err)
	}
	if err != nil {
		return nil, result.Usage, err
	}
	return normalizeProposals(result.Relations, request), result.Usage, nil
}

func normalizeProposals(items []RelationProposal, request RelationRequest) []RelationProposal {
	allowed := make(map[string]struct{}, len(request.Existing))
	for _, candidate := range request.Existing {
		allowed[candidate.Ref] = struct{}{}
	}
	result := make([]RelationProposal, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		from := strings.TrimSpace(item.FromRef)
		if from != "" && from != request.NewObservation.Ref {
			continue
		}
		kind := item.Kind
		if !kind.Known() {
			continue
		}
		to := strings.TrimSpace(item.ToRef)
		if to != "" {
			if _, ok := allowed[to]; !ok {
				to = ""
			}
		}
		if to == "" && kind.RequiresTarget() {
			kind = RelationUnresolved
		}
		key := to + "\x00" + string(kind)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, RelationProposal{FromRef: request.NewObservation.Ref, ToRef: to, Kind: kind})
	}
	if len(result) == 0 {
		return []RelationProposal{{FromRef: request.NewObservation.Ref, Kind: RelationUnresolved}}
	}
	return result
}

func evidenceHint(items []source.Evidence) string {
	for _, item := range items {
		if item.Redacted {
			continue
		}
		quote := strings.TrimSpace(item.Quote)
		if quote != "" {
			return truncateRunes(quote, maxEvidenceHintRunes)
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
