package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestValidateExtractionRejectsUnsafeCandidateOnly(t *testing.T) {
	src := source.Source{ID: "s1", Content: json.RawMessage(`{"messages":[{"id":"m1","content":"2026年10月8日在广州有广告片拍摄。"}]}`)}
	date := mustTime("2026-10-08T00:00:00Z")
	candidates := []Candidate{
		{CandidateID: "c1", Kind: memory.KindEvent, Content: "2026年10月8日在广州有广告片拍摄。", ValidFrom: &date, ValidFromBasis: memory.BasisExplicit,
			Evidence: []source.Evidence{{MessageID: "m1", Quote: "2026年10月8日在广州有广告片拍摄。"}}},
		{CandidateID: "c2", Content: "不需要记忆"},
	}
	issues := ValidateExtraction(src, candidates)
	if len(issues) != 1 || issues[0].CandidateID != "c2" || issues[0].Code != "explicit_exclusion" {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestValidateExtractionChecksEvidenceTemporalAndAtomicity(t *testing.T) {
	src := source.Source{Content: json.RawMessage(`{"text":"2026年10月8日计划先准备然后完成。"}`)}
	from := mustTime("2026-10-08T00:00:00Z")
	to := mustTime("2026-10-07T00:00:00Z")
	issues := ValidateExtraction(src, []Candidate{{CandidateID: "c1", Content: "计划准备，已经完成。", ValidFrom: &from, ValidTo: &to, ValidFromBasis: memory.BasisExplicit,
		Evidence: []source.Evidence{{Quote: "不存在"}}}})
	if !hasIssue(issues, "invalid_interval") || !hasIssue(issues, "evidence_not_found") || !hasIssue(issues, "atomicity_violation") {
		t.Fatalf("issues = %+v", issues)
	}
}

func mustTime(value string) (result time.Time) {
	result, _ = time.Parse(time.RFC3339, value)
	return result
}

func hasIssue(issues []ExtractionIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestExtractionValidationSafeguards(t *testing.T) {
	date := mustTime("2026-10-08T00:00:00Z")
	wrongDate := mustTime("2027-10-08T00:00:00Z")
	tests := []struct {
		name      string
		text      string
		candidate Candidate
		code      string
	}{
		{"kind", "正文", Candidate{Kind: "made_up", Content: "正文"}, "invalid_enum"},
		{"phase", "正文", Candidate{Content: "正文", Phase: "made_up"}, "invalid_enum"},
		{"durability", "正文", Candidate{Content: "正文", Durability: "made_up"}, "invalid_enum"},
		{"basis_without_boundary", "正文", Candidate{Content: "正文", ValidFromBasis: "made_up"}, "invalid_enum"},
		{"unknown_boundary", "正文", Candidate{Content: "正文", ValidFrom: &date, ValidFromBasis: memory.BasisUnknown}, "invalid_temporal_basis"},
		{"empty", "正文", Candidate{Content: " "}, "empty_content"},
		{"negation", "用户不吃花生", Candidate{Content: "用户吃花生", Evidence: []source.Evidence{{Quote: "用户不吃花生"}}}, "negation_lost"},
		{"english_negation", "Never use production data", Candidate{Content: "Use production data", Evidence: []source.Evidence{{Quote: "Never use production data"}}}, "negation_lost"},
		{"non_negated_word", "notice the settings", Candidate{Content: "notice the settings", Evidence: []source.Evidence{{Quote: "notice the settings"}}}, ""},
		{"date_without_boundary", "2026年10月8日拍摄", Candidate{Content: "2026年10月8日拍摄", Evidence: []source.Evidence{{Quote: "2026年10月8日拍摄"}}}, "missing_explicit_boundary"},
		{"wrong_date", "2026年10月8日拍摄", Candidate{Content: "2026年10月8日拍摄", ValidFrom: &wrongDate, ValidFromBasis: memory.BasisExplicit, Evidence: []source.Evidence{{Quote: "2026年10月8日拍摄"}}}, "missing_date_evidence"},
		{"date_anchor", "2026年10月8日拍摄", Candidate{Content: "广州拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisExplicit, Evidence: []source.Evidence{{Quote: "2026年10月8日拍摄"}}}, "date_anchor_lost"},
		{"fuzzy_time", "国庆拍摄", Candidate{Content: "国庆拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisInferred, Evidence: []source.Evidence{{Quote: "国庆拍摄"}}}, "fabricated_precision"},
		{"month_is_not_day", "10月拍摄", Candidate{Content: "10月拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisInferred, Evidence: []source.Evidence{{Quote: "10月拍摄"}}}, "fabricated_precision"},
		{"valid_date", "2026年10月8日拍摄", Candidate{Content: "2026年10月8日拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisExplicit, Evidence: []source.Evidence{{Quote: "2026年10月8日拍摄"}}}, ""},
		{"parallel_actions", "安装软件并且创建账号", Candidate{Content: "安装软件并且创建账号"}, "atomicity_violation"},
		{"state_evolution", "项目计划发布，已经完成", Candidate{Content: "项目计划发布，已经完成"}, "atomicity_violation"},
		{"one_planned_state", "计划完成培训。", Candidate{Content: "计划完成培训。"}, ""},
		{"procedure_action", "部署目标", Candidate{Kind: memory.KindProcedure, Content: "部署目标", Procedure: &ProcedureProposal{Preconditions: []string{"有权限"}}}, "procedure_without_action"},
		{"procedure_order", "先安装再检查", Candidate{Kind: memory.KindProcedure, Content: "检查配置，再安装软件", Procedure: &ProcedureProposal{Steps: []string{"安装软件", "检查配置"}}}, "procedure_details_lost"},
		{"procedure_conditions", "先安装再检查", Candidate{Kind: memory.KindProcedure, Content: "安装软件，再检查配置", Procedure: &ProcedureProposal{Steps: []string{"安装软件", "检查配置"}, FailureBranches: []string{"检查失败时停止"}}}, "procedure_details_lost"},
		{"coherent_procedure", "安装软件并且检查配置", Candidate{Kind: memory.KindProcedure, Content: "安装软件并且检查配置", Procedure: &ProcedureProposal{Steps: []string{"安装软件", "检查配置"}}}, ""},
		{"exclusion_outside_quote", "不要记：用户有临时疼痛", Candidate{Content: "用户有临时疼痛", Evidence: []source.Evidence{{Quote: "用户有临时疼痛"}}}, "explicit_exclusion"},
		{"exclusion_after_fact", "用户有临时疼痛。这件事不要记。", Candidate{Content: "用户有临时疼痛", Evidence: []source.Evidence{{Quote: "用户有临时疼痛"}}}, "explicit_exclusion"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(test.text)
			issues := ValidateExtraction(source.Source{Content: body}, []Candidate{test.candidate})
			if test.code == "" && len(issues) != 0 {
				t.Fatalf("unexpected issues: %+v", issues)
			}
			if test.code != "" && !hasIssue(issues, test.code) {
				t.Fatalf("missing %s: %+v", test.code, issues)
			}
		})
	}
}

func TestExtractionEvidenceIsVerbatimAndMessageBound(t *testing.T) {
	src := source.Source{Content: json.RawMessage(`{"messages":[{"id":"m1","content":"用户不吃花生"},{"id":"m2","content":"用户吃核桃"}]}`)}
	for _, evidence := range []source.Evidence{
		{MessageID: "m1", Quote: "用户吃花生"},
		{MessageID: "m2", Quote: "用户不吃花生"},
		{Quote: "花生用户"},
		{Quote: "用户不吃花生用户吃核桃"},
	} {
		issues := ValidateExtraction(src, []Candidate{{Content: "用户不吃花生", Evidence: []source.Evidence{evidence}}})
		if !hasIssue(issues, "evidence_not_found") {
			t.Fatalf("accepted fabricated quote %+v: %+v", evidence, issues)
		}
	}
}

type extractionStub struct {
	initial      ExtractionResult
	initialErr   error
	repaired     ExtractionResult
	repairErr    error
	extractCalls int
	repairCalls  int
	request      ExtractionRepairRequest
}

func (s *extractionStub) Extract(context.Context, ExtractionRequest) (ExtractionResult, error) {
	s.extractCalls++
	return s.initial, s.initialErr
}

func (s *extractionStub) RepairExtraction(_ context.Context, request ExtractionRepairRequest) (ExtractionResult, error) {
	s.repairCalls++
	s.request = request
	return s.repaired, s.repairErr
}

func TestExtractionTargetedRepairKeepsValidCandidateImmutable(t *testing.T) {
	good := Candidate{Content: "用户偏好中文", Kind: memory.KindPreference}
	bad := Candidate{Content: "安装软件并且创建账号"}
	stub := &extractionStub{
		initial: ExtractionResult{Candidates: []Candidate{good, bad}},
		repaired: ExtractionResult{Candidates: []Candidate{
			{CandidateID: "c1", Content: "恶意改写合格候选"},
			{CandidateID: "c2.1", Content: "安装软件"},
			{CandidateID: "c2.2", Content: "创建账号"},
			{CandidateID: "c99", Content: "未请求的新事实"},
		}},
	}
	p := NewProcessor(nil, nil, nil, nil, stub)
	got, warnings, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"用户偏好中文。安装软件并且创建账号。"`)})
	if err != nil || len(got) != 3 || len(warnings) != 0 {
		t.Fatalf("got=%+v warnings=%v err=%v", got, warnings, err)
	}
	if got[0].Content != good.Content || got[1].Content != "安装软件" || got[2].Content != "创建账号" {
		t.Fatalf("repair result: %+v", got)
	}
	if stub.extractCalls != 1 || stub.repairCalls != 1 || len(stub.request.Candidates) != 1 || stub.request.Candidates[0].CandidateID != "c2" || stub.request.Retry {
		t.Fatalf("request=%+v calls=%d/%d", stub.request, stub.extractCalls, stub.repairCalls)
	}
}

func TestExtractionFailedRepairIsolatesOnlyInvalidCandidates(t *testing.T) {
	for _, repairErr := range []error{nil, errors.New("repair unavailable")} {
		stub := &extractionStub{
			initial:  ExtractionResult{Candidates: []Candidate{{Content: "合格事实"}, {Content: "安装软件并且创建账号"}}},
			repaired: ExtractionResult{Candidates: []Candidate{{CandidateID: "c2", Content: "安装软件并且创建账号"}}}, repairErr: repairErr,
		}
		p := NewProcessor(nil, nil, nil, nil, stub)
		got, warnings, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"合格事实。安装软件并且创建账号。"`)})
		if err != nil || len(got) != 1 || got[0].Content != "合格事实" || !reflect.DeepEqual(warnings, []string{WarningExtractionIsolated}) || stub.repairCalls != 1 {
			t.Fatalf("got=%+v warnings=%v err=%v calls=%d", got, warnings, err, stub.repairCalls)
		}
	}
}

func TestExtractionExclusionCannotBeRepairedAway(t *testing.T) {
	stub := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{
		{Content: "用户有临时疼痛", Evidence: []source.Evidence{{MessageID: "m1", Quote: "用户有临时疼痛"}}},
		{Content: "用户偏好中文", Evidence: []source.Evidence{{MessageID: "m2", Quote: "用户偏好中文"}}},
	}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	src := source.Source{Content: json.RawMessage(`{"messages":[{"id":"m1","content":"别记录：用户有临时疼痛"},{"id":"m2","content":"用户偏好中文"}]}`)}
	got, warnings, err := p.extractValidated(context.Background(), src)
	if err != nil || len(got) != 1 || got[0].Content != "用户偏好中文" || stub.repairCalls != 0 || !reflect.DeepEqual(warnings, []string{WarningExtractionExcluded}) {
		t.Fatalf("got=%+v warnings=%v err=%v", got, warnings, err)
	}
}

func TestExtractionProtocolRetryHasSingleRepairBudget(t *testing.T) {
	stub := &extractionStub{initialErr: &ProviderError{Operation: "protocol"}, repaired: ExtractionResult{Candidates: []Candidate{{Content: "", Kind: "invalid"}, {Content: "可用事实"}}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	got, warnings, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"可用事实"`)})
	if err != nil || len(got) != 1 || len(warnings) != 1 || stub.repairCalls != 1 || !stub.request.Retry {
		t.Fatalf("got=%+v warnings=%v err=%v request=%+v", got, warnings, err, stub.request)
	}
	for _, operation := range []string{"configuration", "credentials", "http"} {
		stub := &extractionStub{initialErr: &ProviderError{Operation: operation}}
		p := NewProcessor(nil, nil, nil, nil, stub)
		if _, _, err := p.extractValidated(context.Background(), source.Source{}); err == nil || stub.repairCalls != 0 {
			t.Fatalf("operation=%s err=%v calls=%d", operation, err, stub.repairCalls)
		}
	}
}

func TestExtractionWidespreadErrorsRequestStructuredRetry(t *testing.T) {
	stub := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{{Content: "a", Kind: "bad"}, {Content: "b", Kind: "bad"}, {Content: "c", Kind: "bad"}}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	_, _, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"a b c"`)})
	if err != nil || stub.repairCalls != 1 || !stub.request.Retry || len(stub.request.Issues) != 3 {
		t.Fatalf("err=%v request=%+v", err, stub.request)
	}
}
