package httptransport

import (
	"encoding/json"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// The HTTP DTOs mirror the fixed OpenAPI shapes and keep transport concerns out
// of domain packages. All times are encoded in UTC RFC 3339 by encoding/json.

type submitResponse struct {
	SourceID string `json:"source_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
}

type memoryDTO struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Title          string    `json:"title"`
	Content        string    `json:"content"`
	State          string    `json:"state"`
	Version        int       `json:"version"`
	ObservedAt     time.Time `json:"observed_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	CreatedByApp   string    `json:"created_by_app,omitempty"`
	OriginSourceID string    `json:"origin_source_id,omitempty"`
}

type memoryListResponse struct {
	Items []memoryDTO `json:"items"`
}

type deleteResponse struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type purgeResponse struct {
	ID     string `json:"id"`
	Purged bool   `json:"purged"`
}

type locationDTO struct {
	Field string `json:"field,omitempty"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type evidenceDTO struct {
	ID         string       `json:"id"`
	SourceID   string       `json:"source_id"`
	MessageID  string       `json:"message_id,omitempty"`
	Quote      string       `json:"quote,omitempty"`
	Location   *locationDTO `json:"location,omitempty"`
	ModelName  string       `json:"model_name,omitempty"`
	Redacted   bool         `json:"redacted"`
	RedactedAt *time.Time   `json:"redacted_at,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

type evidenceListResponse struct {
	Items []evidenceDTO `json:"items"`
}

type revisionDTO struct {
	MemoryID  string        `json:"memory_id"`
	Version   int           `json:"version"`
	Operation string        `json:"operation"`
	Reason    string        `json:"reason,omitempty"`
	Snapshot  memoryDTO     `json:"snapshot"`
	Evidence  []evidenceDTO `json:"evidence"`
	CreatedAt time.Time     `json:"created_at"`
}

type revisionListResponse struct {
	Items []revisionDTO `json:"items"`
}

type evidenceRefDTO struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
}

type explanationDTO struct {
	Ranking      string `json:"ranking"`
	FieldWeights [2]int `json:"field_weights"`
}

type searchItemDTO struct {
	Memory       memoryDTO        `json:"memory"`
	Score        float64          `json:"score"`
	Snippet      string           `json:"snippet"`
	MatchedTerms []string         `json:"matched_terms"`
	EvidenceRefs []evidenceRefDTO `json:"evidence_refs"`
	Explanation  *explanationDTO  `json:"explanation,omitempty"`
}

type searchResponse struct {
	Items []searchItemDTO `json:"items"`
}

type sourceDTO struct {
	ID         string          `json:"id"`
	AppID      string          `json:"app_id,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Redacted   bool            `json:"redacted"`
	RedactedAt *time.Time      `json:"redacted_at,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type redactResponse struct {
	ID            string `json:"id"`
	Redacted      bool   `json:"redacted"`
	CancelledJobs int64  `json:"cancelled_jobs"`
}

type jobDTO struct {
	ID            string     `json:"id"`
	SourceID      string     `json:"source_id"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	Error         string     `json:"error,omitempty"`
	MemoryIDs     []string   `json:"memory_ids"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

func memoryFromDomain(item memory.Memory) memoryDTO {
	return memoryDTO{
		ID:             item.ID,
		Kind:           string(item.Kind),
		Title:          item.Title,
		Content:        item.Content,
		State:          string(item.State),
		Version:        item.Version,
		ObservedAt:     item.ObservedAt.UTC(),
		CreatedAt:      item.CreatedAt.UTC(),
		UpdatedAt:      item.UpdatedAt.UTC(),
		CreatedByApp:   item.CreatedByApp,
		OriginSourceID: item.OriginSourceID,
	}
}

func memoriesFromDomain(items []memory.Memory) []memoryDTO {
	result := make([]memoryDTO, 0, len(items))
	for _, item := range items {
		result = append(result, memoryFromDomain(item))
	}
	return result
}

func evidenceFromDomain(item source.Evidence) evidenceDTO {
	result := evidenceDTO{
		ID:        item.ID,
		SourceID:  item.SourceID,
		MessageID: item.MessageID,
		Quote:     item.Quote,
		ModelName: item.ModelName,
		Redacted:  item.Redacted,
		CreatedAt: item.CreatedAt.UTC(),
	}
	if item.Location != nil {
		result.Location = &locationDTO{
			Field: item.Location.Field,
			Start: item.Location.Start,
			End:   item.Location.End,
		}
	}
	if item.RedactedAt != nil {
		redactedAt := item.RedactedAt.UTC()
		result.RedactedAt = &redactedAt
	}
	return result
}

func evidenceFromDomainList(items []source.Evidence) []evidenceDTO {
	result := make([]evidenceDTO, 0, len(items))
	for _, item := range items {
		result = append(result, evidenceFromDomain(item))
	}
	return result
}

func revisionFromDomain(item memory.Revision) revisionDTO {
	return revisionDTO{
		MemoryID:  item.MemoryID,
		Version:   item.Version,
		Operation: string(item.Operation),
		Reason:    item.Reason,
		Snapshot:  memoryFromDomain(item.Snapshot),
		Evidence:  evidenceFromDomainList(item.Evidence),
		CreatedAt: item.CreatedAt.UTC(),
	}
}

func revisionsFromDomain(items []memory.Revision) []revisionDTO {
	result := make([]revisionDTO, 0, len(items))
	for _, item := range items {
		result = append(result, revisionFromDomain(item))
	}
	return result
}

func searchResultFromDomain(result retrieval.Result) searchResponse {
	items := make([]searchItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		matchedTerms := item.MatchedTerms
		if matchedTerms == nil {
			matchedTerms = []string{}
		}
		evidenceRefs := make([]evidenceRefDTO, 0, len(item.EvidenceRefs))
		for _, ref := range item.EvidenceRefs {
			evidenceRefs = append(evidenceRefs, evidenceRefDTO{ID: ref.ID, SourceID: ref.SourceID})
		}
		var explanation *explanationDTO
		if item.Explanation != nil {
			explanation = &explanationDTO{
				Ranking:      item.Explanation.Ranking,
				FieldWeights: item.Explanation.FieldWeights,
			}
		}
		items = append(items, searchItemDTO{
			Memory:       memoryFromDomain(item.Memory),
			Score:        item.Score,
			Snippet:      item.Snippet,
			MatchedTerms: matchedTerms,
			EvidenceRefs: evidenceRefs,
			Explanation:  explanation,
		})
	}
	return searchResponse{Items: items}
}

func sourceFromDomain(item source.Source) sourceDTO {
	result := sourceDTO{
		ID:         item.ID,
		AppID:      item.AppID,
		ExternalID: item.ExternalID,
		Content:    item.Content,
		Redacted:   item.Redacted,
		CreatedAt:  item.CreatedAt.UTC(),
	}
	if item.RedactedAt != nil {
		redactedAt := item.RedactedAt.UTC()
		result.RedactedAt = &redactedAt
	}
	return result
}

func redactFromDomain(item source.RedactResult) redactResponse {
	return redactResponse{
		ID:            item.SourceID,
		Redacted:      item.Redacted,
		CancelledJobs: item.CancelledJobs,
	}
}

func jobFromDomain(item ingest.Job) jobDTO {
	memoryIDs := item.MemoryIDs
	if memoryIDs == nil {
		memoryIDs = []string{}
	}
	result := jobDTO{
		ID:            item.ID,
		SourceID:      item.SourceID,
		Status:        string(item.Status),
		Attempts:      item.Attempts,
		NextAttemptAt: utcPointer(item.NextAttemptAt),
		Error:         item.Error,
		MemoryIDs:     memoryIDs,
		CreatedAt:     item.CreatedAt.UTC(),
		UpdatedAt:     item.UpdatedAt.UTC(),
		StartedAt:     utcPointer(item.StartedAt),
		FinishedAt:    utcPointer(item.FinishedAt),
	}
	return result
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
