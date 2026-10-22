package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/skill"
)

type stubExperiences struct {
	governance.ExperienceService
	submit  func(context.Context, governance.ExperienceSubmission) (governance.ExperienceReceipt, error)
	retract func(context.Context, string) (governance.ExperienceReceipt, error)
}

func (s stubExperiences) Submit(ctx context.Context, submission governance.ExperienceSubmission) (governance.ExperienceReceipt, error) {
	return s.submit(ctx, submission)
}

func (s stubExperiences) Retract(ctx context.Context, id string) (governance.ExperienceReceipt, error) {
	return s.retract(ctx, id)
}

type stubSkills struct {
	skill.ContentService
	get func(context.Context, string, int) (skill.Content, error)
}

func (s stubSkills) Get(ctx context.Context, id string, version int) (skill.Content, error) {
	return s.get(ctx, id, version)
}

func TestExperienceSubmissionMappingAndReceipt(t *testing.T) {
	created := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	var captured governance.ExperienceSubmission
	services := Services{Experiences: stubExperiences{submit: func(_ context.Context,
		submission governance.ExperienceSubmission) (governance.ExperienceReceipt, error) {
		captured = submission
		return governance.ExperienceReceipt{
			ID: "experience-1", SourceID: "source-1", RunID: submission.RunID,
			Outcome: submission.Outcome, State: governance.ExperienceRecorded,
			CreatedAt: created, UpdatedAt: created,
		}, nil
	}}}

	body := `{"run_id":"client-run","app_id":"desktop","scope_key":"project:dmemd","goal":"验证修改",` +
		`"conditions":["依赖可用"],"actions":["运行 go test"],"outcome":"success",` +
		`"skill_id":"skill-1","skill_version":2,"content":{"report":"ok"}}`
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/experiences", strings.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d %q", response.Code, response.Body.String())
	}
	if captured.RunID != "client-run" || captured.AppID != "desktop" || captured.ScopeKey != "project:dmemd" ||
		captured.Goal != "验证修改" || captured.Outcome != governance.OutcomeSuccess ||
		captured.SkillID != "skill-1" || captured.SkillVersion != 2 ||
		len(captured.Conditions) != 1 || len(captured.Actions) != 1 ||
		string(captured.Content) != `{"report":"ok"}` {
		t.Fatalf("captured submission = %+v", captured)
	}
	var receipt struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ID != "experience-1" || receipt.State != "recorded" || receipt.Outcome != "success" {
		t.Fatalf("receipt = %+v", receipt)
	}

	services = Services{Experiences: stubExperiences{submit: func(context.Context,
		governance.ExperienceSubmission) (governance.ExperienceReceipt, error) {
		return governance.ExperienceReceipt{}, governance.ErrInvalidExperience
	}}}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/experiences",
		strings.NewReader(`{"goal":"","outcome":"maybe"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid feedback status = %d %q", response.Code, response.Body.String())
	}
}

func TestExperienceRetractAndSkillContent(t *testing.T) {
	services := Services{Experiences: stubExperiences{
		retract: func(_ context.Context, id string) (governance.ExperienceReceipt, error) {
			if id == "missing" {
				return governance.ExperienceReceipt{}, governance.ErrExperienceNotFound
			}
			return governance.ExperienceReceipt{ID: id, State: governance.ExperienceRevoked}, nil
		},
	}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/experiences/e1/retract", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"revoked"`) {
		t.Fatalf("retract = %d %q", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/experiences/missing/retract", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing retract = %d %q", response.Code, response.Body.String())
	}

	var capturedVersion int
	services = Services{Skills: stubSkills{get: func(_ context.Context, id string, version int) (skill.Content, error) {
		capturedVersion = version
		if id == "missing" {
			return skill.Content{}, artifact.ErrNotFound
		}
		return skill.Content{
			ArtifactID: id, Version: 3, State: "active", Name: "验证工作流",
			Description: "修改后的验证", Content: "---\nname: \"验证工作流\"\n---\n\n步骤",
			MemoryRefs: []artifact.MemoryRef{{MemoryID: "m1", Version: 2}},
			RunRefs:    []artifact.RunRef{{ExperienceID: "e1"}},
		}, nil
	}}}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/skills/skill-1/content?version=3", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("skill content = %d %q", response.Code, response.Body.String())
	}
	if capturedVersion != 3 {
		t.Fatalf("version = %d", capturedVersion)
	}
	var content struct {
		ID         string `json:"id"`
		Version    int    `json:"version"`
		State      string `json:"state"`
		Content    string `json:"content"`
		MemoryRefs []any  `json:"memory_refs"`
		RunRefs    []any  `json:"run_refs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.ID != "skill-1" || content.Version != 3 || content.State != "active" ||
		!strings.Contains(content.Content, "name:") || len(content.MemoryRefs) != 1 || len(content.RunRefs) != 1 {
		t.Fatalf("skill content = %+v", content)
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/skills/skill-1/content?version=bad", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid version = %d %q", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/skills/missing/content", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing skill = %d %q", response.Code, response.Body.String())
	}
}

func TestExperienceInvalidOutcomeReachesService(t *testing.T) {
	services := Services{Experiences: stubExperiences{submit: func(_ context.Context,
		submission governance.ExperienceSubmission) (governance.ExperienceReceipt, error) {
		if submission.Outcome != governance.ExperienceOutcome("maybe") {
			t.Fatalf("outcome = %q", submission.Outcome)
		}
		return governance.ExperienceReceipt{}, governance.ErrInvalidExperience
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/experiences",
		strings.NewReader(`{"goal":"x","outcome":"maybe"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %q", response.Code, response.Body.String())
	}
}

var _ governance.ExperienceService = stubExperiences{}
var _ skill.ContentService = stubSkills{}
