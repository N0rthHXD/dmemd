//go:build legacy

package quality_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/skill"
)

// TestLiveModelQuality is the explicit, opt-in semantic evaluation entry. It is
// skipped by default because CI must not call a real model. It never replaces
// the deterministic labeled suite; it reports how one configured model performs
// against the same fixed samples.
func TestLiveModelQuality(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("DMEMD_EVAL_BASE_URL"))
	if baseURL == "" {
		t.Skip("set DMEMD_EVAL_BASE_URL (and optionally DMEMD_EVAL_MODEL/API_KEY) to run live semantic evaluation")
	}
	timeout := model.DefaultTimeout
	if value := strings.TrimSpace(os.Getenv("DMEMD_EVAL_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("DMEMD_EVAL_TIMEOUT: %v", err)
		}
		timeout = parsed
	}
	provider, err := model.NewProvider(model.Config{
		Name:    strings.TrimSpace(os.Getenv("DMEMD_EVAL_MODEL")),
		BaseURL: baseURL,
		APIKey:  os.Getenv("DMEMD_EVAL_API_KEY"),
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("construct eval provider: %v", err)
	}
	data := loadDataset(t)
	ctx := context.Background()

	classificationPassed := 0
	for _, testCase := range data.Classification {
		topics, err := provider.SuggestTopics(ctx, testCase.Title, testCase.Content, testCase.ScopeKey)
		if err != nil {
			t.Errorf("classification %s: %v", testCase.Name, err)
			continue
		}
		if reflect.DeepEqual(memory.NormalizeTopics(topics), memory.NormalizeTopics(testCase.ExpectedTopics)) {
			classificationPassed++
		} else {
			t.Errorf("classification %s: topics=%v, want %v", testCase.Name, topics, testCase.ExpectedTopics)
		}
	}

	summaryPassed := 0
	for _, testCase := range data.Summaries {
		result, err := provider.Summarize(ctx, artifact.AggregateRequest{
			Kind:      testCase.Kind,
			ScopeKey:  testCase.ScopeKey,
			Topic:     testCase.Topic,
			Truncated: testCase.MaxInputs > 0,
			Memories:  memoryInputs(testCase),
		})
		if err != nil {
			t.Errorf("summary %s: %v", testCase.Name, err)
			continue
		}
		if ok, problems := checkTerms(testCase.Name, result.Content,
			testCase.RequiredTerms, testCase.ForbiddenTerms); ok {
			summaryPassed++
		} else {
			t.Errorf("%s", strings.Join(problems, "\n"))
		}
	}

	skillPassed := 0
	for _, testCase := range data.Skills {
		draft, err := provider.ComposeSkill(ctx, skill.Request{
			ScopeKey:    testCase.ScopeKey,
			Topic:       testCase.Topic,
			Procedures:  procedureInputs(testCase),
			Experiences: skillExperienceInputs(testCase),
			Truncated:   testCase.MaxInputs > 0,
		})
		if err != nil {
			t.Errorf("skill %s: %v", testCase.Name, err)
			continue
		}
		text := skillRenderText(draft)
		ok, problems := checkTerms(testCase.Name, text, testCase.RequiredTerms, testCase.ForbiddenTerms)
		if testCase.ForbidCodeFence && strings.Contains(text, "```") {
			ok = false
			problems = append(problems, testCase.Name+": contains a code block")
		}
		if ok {
			skillPassed++
		} else {
			t.Errorf("%s", strings.Join(problems, "\n"))
		}
	}

	t.Logf("live model quality: classification %d/%d, summaries %d/%d, skills %d/%d",
		classificationPassed, len(data.Classification),
		summaryPassed, len(data.Summaries),
		skillPassed, len(data.Skills))
	if classificationPassed+summaryPassed+skillPassed !=
		len(data.Classification)+len(data.Summaries)+len(data.Skills) {
		t.Error("live model did not satisfy all fixed labeled constraints")
	}
}

func skillRenderText(draft skill.Draft) string {
	parts := []string{strings.TrimSpace(draft.Content)}
	appendSection := func(title string, values []string) {
		if len(values) == 0 {
			return
		}
		parts = append(parts, "## "+title)
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				parts = append(parts, "- "+strings.TrimSpace(value))
			}
		}
	}
	appendSection("前置条件", draft.Preconditions)
	appendSection("步骤", draft.Steps)
	appendSection("停止条件", draft.StopConditions)
	appendSection("完成检查", draft.CompletionChecks)
	appendSection("反例", draft.Counterexamples)
	return strings.Join(parts, "\n")
}
