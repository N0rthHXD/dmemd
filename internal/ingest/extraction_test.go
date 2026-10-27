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
			Evidence: []source.EvidenceReference{{Quote: "2026年10月8日在广州有广告片拍摄。"}}},
		{CandidateID: "c2", Content: "不需要记忆"},
	}
	issues := ValidateExtraction(src, candidates)
	if len(issues) != 1 || issues[0].CandidateID != "c2" || issues[0].Code != "explicit_exclusion" {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestNegationCheckAppliesToTheExtractedClause(t *testing.T) {
	text := "用户不吃花生，喜欢核桃"
	src := source.Source{Content: json.RawMessage(`"` + text + `"`)}
	safe := ValidateExtraction(src, []Candidate{{
		CandidateID: "c1", Content: "用户喜欢核桃",
		Evidence: []source.EvidenceReference{{Quote: text}},
	}})
	if hasIssue(safe, "negation_lost") {
		t.Fatalf("unrelated negated clause rejected: %+v", safe)
	}
	lost := ValidateExtraction(src, []Candidate{{
		CandidateID: "c1", Content: "用户吃花生",
		Evidence: []source.EvidenceReference{{Quote: text}},
	}})
	if !hasIssue(lost, "negation_lost") {
		t.Fatalf("lost negation not detected: %+v", lost)
	}
}

func TestValidateExtractionTreatsRepeatedQuoteAsAmbiguous(t *testing.T) {
	src := source.Source{Content: json.RawMessage(`{"messages":[{"content":"same"},{"content":"same"}]}`)}
	issues := ValidateExtraction(src, []Candidate{{CandidateID: "c1", Content: "same", Evidence: []source.EvidenceReference{{Quote: "same"}}}})
	if !hasIssue(issues, source.EvidenceQuoteAmbiguous) {
		t.Fatalf("issues = %+v", issues)
	}
	resolved := ValidateExtraction(src, []Candidate{{CandidateID: "c1", Content: "same", Evidence: []source.EvidenceReference{{SegmentID: "seg-0002", Quote: "same"}}}})
	if hasEvidenceIssue(resolved) {
		t.Fatalf("segment-bound evidence rejected: %+v", resolved)
	}
}

func TestValidateExtractionChecksEvidenceTemporalAndAtomicity(t *testing.T) {
	src := source.Source{Content: json.RawMessage(`{"text":"2026年10月8日计划先准备然后完成。"}`)}
	from := mustTime("2026-10-08T00:00:00Z")
	to := mustTime("2026-10-07T00:00:00Z")
	issues := ValidateExtraction(src, []Candidate{{CandidateID: "c1", Content: "计划准备，已经完成。", ValidFrom: &from, ValidTo: &to, ValidFromBasis: memory.BasisExplicit,
		Evidence: []source.EvidenceReference{{Quote: "不存在"}}}})
	if !hasIssue(issues, "invalid_interval") || !hasEvidenceIssue(issues) || !hasIssue(issues, "atomicity_violation") {
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

func hasEvidenceIssue(issues []ExtractionIssue) bool {
	for _, issue := range issues {
		switch issue.Code {
		case source.EvidenceEmptyQuote, source.EvidenceSegmentUnknown, source.EvidenceQuoteNotFound,
			source.EvidenceQuoteAmbiguous, source.EvidenceOffsetsInvalid:
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
		{"negation", "用户不吃花生", Candidate{Content: "用户吃花生", Evidence: []source.EvidenceReference{{Quote: "用户不吃花生"}}}, "negation_lost"},
		{"english_negation", "Never use production data", Candidate{Content: "Use production data", Evidence: []source.EvidenceReference{{Quote: "Never use production data"}}}, "negation_lost"},
		{"non_negated_word", "notice the settings", Candidate{Content: "notice the settings", Evidence: []source.EvidenceReference{{Quote: "notice the settings"}}}, ""},
		{"date_without_boundary", "2026年10月8日拍摄", Candidate{Content: "2026年10月8日拍摄", Evidence: []source.EvidenceReference{{Quote: "2026年10月8日拍摄"}}}, "missing_explicit_boundary"},
		{"wrong_date", "2026年10月8日拍摄", Candidate{Content: "2026年10月8日拍摄", ValidFrom: &wrongDate, ValidFromBasis: memory.BasisExplicit, Evidence: []source.EvidenceReference{{Quote: "2026年10月8日拍摄"}}}, "missing_date_evidence"},
		{"date_anchor_not_required", "2026年10月8日拍摄", Candidate{Content: "广州拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisExplicit, Evidence: []source.EvidenceReference{{Quote: "2026年10月8日拍摄"}}}, ""},
		{"fuzzy_time", "国庆拍摄", Candidate{Content: "国庆拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisInferred, Evidence: []source.EvidenceReference{{Quote: "国庆拍摄"}}}, "fabricated_precision"},
		{"month_is_not_day", "10月拍摄", Candidate{Content: "10月拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisInferred, Evidence: []source.EvidenceReference{{Quote: "10月拍摄"}}}, "fabricated_precision"},
		{"valid_date", "2026年10月8日拍摄", Candidate{Content: "2026年10月8日拍摄", ValidFrom: &date, ValidFromBasis: memory.BasisExplicit, Evidence: []source.EvidenceReference{{Quote: "2026年10月8日拍摄"}}}, ""},
		{"parallel_actions", "安装软件并且创建账号", Candidate{Content: "安装软件并且创建账号"}, "atomicity_violation"},
		{"state_evolution", "项目计划发布，已经完成", Candidate{Content: "项目计划发布，已经完成"}, "atomicity_violation"},
		{"one_planned_state", "计划完成培训。", Candidate{Content: "计划完成培训。"}, ""},
		{"procedure_action", "部署目标", Candidate{Kind: memory.KindProcedure, Content: "部署目标", Procedure: &ProcedureProposal{Preconditions: []string{"有权限"}}}, "procedure_without_action"},
		{"procedure_order", "先安装再检查", Candidate{Kind: memory.KindProcedure, Content: "检查配置，再安装软件", Procedure: &ProcedureProposal{Steps: []string{"安装软件", "检查配置"}}}, ""},
		{"procedure_conditions", "先安装再检查", Candidate{Kind: memory.KindProcedure, Content: "安装软件，再检查配置", Procedure: &ProcedureProposal{Steps: []string{"安装软件", "检查配置"}, FailureBranches: []string{"检查失败时停止"}}}, ""},
		{"coherent_procedure", "安装软件并且检查配置", Candidate{Kind: memory.KindProcedure, Content: "安装软件并且检查配置", Procedure: &ProcedureProposal{Steps: []string{"安装软件", "检查配置"}}}, ""},
		{"exclusion_outside_quote", "不要记：用户有临时疼痛", Candidate{Content: "用户有临时疼痛", Evidence: []source.EvidenceReference{{Quote: "用户有临时疼痛"}}}, "explicit_exclusion"},
		{"exclusion_after_fact", "用户有临时疼痛。这件事不要记。", Candidate{Content: "用户有临时疼痛", Evidence: []source.EvidenceReference{{Quote: "用户有临时疼痛"}}}, "explicit_exclusion"},
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
	for _, evidence := range []source.EvidenceReference{
		{Quote: "用户吃花生"},
		{SegmentID: "seg-0002", Quote: "用户不吃花生"},
		{Quote: "花生用户"},
		{Quote: "用户不吃花生用户吃核桃"},
	} {
		issues := ValidateExtraction(src, []Candidate{{Content: "用户不吃花生", Evidence: []source.EvidenceReference{evidence}}})
		if !hasEvidenceIssue(issues) {
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
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"用户偏好中文。安装软件并且创建账号。"`)})
	if err != nil || len(outcome.Candidates) != 3 || len(outcome.Warnings) != 0 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if outcome.Candidates[0].Content != good.Content || outcome.Candidates[1].Content != "安装软件" || outcome.Candidates[2].Content != "创建账号" {
		t.Fatalf("repair result: %+v", outcome.Candidates)
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
		outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"合格事实。安装软件并且创建账号。"`)})
		if err != nil || len(outcome.Candidates) != 1 || outcome.Candidates[0].Content != "合格事实" || !reflect.DeepEqual(outcome.Warnings, []string{WarningExtractionIsolated}) || stub.repairCalls != 1 {
			t.Fatalf("outcome=%+v err=%v calls=%d", outcome, err, stub.repairCalls)
		}
	}
}

func TestExtractionExclusionCannotBeRepairedAway(t *testing.T) {
	stub := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{
		{Content: "用户有临时疼痛", Evidence: []source.EvidenceReference{{Quote: "用户有临时疼痛"}}},
		{Content: "用户偏好中文", Evidence: []source.EvidenceReference{{Quote: "用户偏好中文"}}},
	}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	src := source.Source{Content: json.RawMessage(`{"messages":[{"id":"m1","content":"别记录：用户有临时疼痛"},{"id":"m2","content":"用户偏好中文"}]}`)}
	outcome, err := p.extractValidated(context.Background(), src)
	if err != nil || len(outcome.Candidates) != 1 || outcome.Candidates[0].Content != "用户偏好中文" || stub.repairCalls != 0 || !reflect.DeepEqual(outcome.Warnings, []string{WarningExtractionExcluded}) {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestExtractionProtocolRetryHasSingleRepairBudget(t *testing.T) {
	stub := &extractionStub{initialErr: &ProviderError{Operation: "protocol"}, repaired: ExtractionResult{Candidates: []Candidate{{Content: "", Kind: "invalid"}, {Content: "可用事实"}}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"可用事实"`)})
	if err != nil || len(outcome.Candidates) != 1 || len(outcome.Warnings) != 1 || stub.repairCalls != 1 || !stub.request.Retry {
		t.Fatalf("outcome=%+v err=%v request=%+v", outcome, err, stub.request)
	}
	for _, operation := range []string{"configuration", "credentials", "http"} {
		stub := &extractionStub{initialErr: &ProviderError{Operation: operation}}
		p := NewProcessor(nil, nil, nil, nil, stub)
		if _, err := p.extractValidated(context.Background(), source.Source{}); err == nil || stub.repairCalls != 0 {
			t.Fatalf("operation=%s err=%v calls=%d", operation, err, stub.repairCalls)
		}
	}
}

func TestExtractionQualityOutcomesAndRepairTimeout(t *testing.T) {
	good := Candidate{Content: "用户偏好中文", Kind: memory.KindPreference}
	bad := Candidate{Content: "安装软件并且创建账号", Kind: "made_up"}
	timeout := &extractionStub{
		initial:   ExtractionResult{Candidates: []Candidate{good, bad}},
		repairErr: context.DeadlineExceeded,
	}
	p := NewProcessor(nil, nil, nil, nil, timeout)
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"用户偏好中文。安装软件并且创建账号。"`)})
	if err != nil || len(outcome.Candidates) != 1 || outcome.Candidates[0].Content != good.Content {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if outcome.quality != QualityPartial || outcome.Stats.RepairTimedOut != 1 || outcome.Stats.RepairAttempts != 1 ||
		outcome.Stats.IsolatedCandidates != 1 || outcome.Stats.AcceptedCandidates != 1 {
		t.Fatalf("quality/statistics = %+v stats=%+v", outcome.quality, outcome.Stats)
	}

	allBad := &extractionStub{
		initial:   ExtractionResult{Candidates: []Candidate{bad}},
		repairErr: context.DeadlineExceeded,
	}
	p = NewProcessor(nil, nil, nil, nil, allBad)
	outcome, err = p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"安装软件并且创建账号。"`)})
	var qualityErr *ExtractionQualityError
	if !errors.As(err, &qualityErr) || qualityErr.Kind != "transient" || qualityErr.Quality != "" ||
		qualityErr.Stats.RepairTimedOut != 1 || len(outcome.Candidates) != 0 {
		t.Fatalf("timeout outcome=%+v err=%v quality=%+v", outcome, err, qualityErr)
	}

	complete := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{good}}}
	p = NewProcessor(nil, nil, nil, nil, complete)
	outcome, err = p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"用户偏好中文"`)})
	if err != nil || outcome.quality != QualityComplete || complete.repairCalls != 0 {
		t.Fatalf("complete outcome=%+v err=%v repair_calls=%d", outcome, err, complete.repairCalls)
	}

	empty := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{}}}
	p = NewProcessor(nil, nil, nil, nil, empty)
	outcome, err = p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"没有可提取内容"`)})
	if err != nil || outcome.quality != QualityEmpty || len(outcome.Candidates) != 0 {
		t.Fatalf("empty outcome=%+v err=%v", outcome, err)
	}
}

func TestExtractionPrivacyExclusionCompletesWithoutRepair(t *testing.T) {
	stub := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{
		{Content: "不需要记忆"},
		{Content: "用户偏好中文"},
	}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"不需要记忆。用户偏好中文。"`)})
	if err != nil || len(outcome.Candidates) != 1 || outcome.Candidates[0].Content != "用户偏好中文" ||
		stub.repairCalls != 0 || outcome.quality != QualityComplete ||
		outcome.Stats.ExcludedCandidates != 1 || outcome.Stats.AcceptedCandidates != 1 {
		t.Fatalf("outcome=%+v err=%v repair=%d", outcome, err, stub.repairCalls)
	}
	found := false
	for _, diagnostic := range outcome.Diagnostics {
		if diagnostic.Code == "explicit_exclusion" && diagnostic.Disposition == "excluded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("exclusion diagnostic missing: %+v", outcome.Diagnostics)
	}
}

func TestRepairChildIDKeepsServiceControlledLineage(t *testing.T) {
	for _, test := range []struct {
		id   string
		want bool
	}{
		{"c1", true}, {"c1.1", true}, {"c1.12", true},
		{"c1.01", false}, {"c1.evil", false}, {"c10", false}, {"c1.", false},
	} {
		if got := repairChildID("c1", test.id); got != test.want {
			t.Fatalf("repairChildID(%q) = %v, want %v", test.id, got, test.want)
		}
	}
}

func TestExtractionRejectsDuplicateCandidateIDs(t *testing.T) {
	src := source.Source{Content: json.RawMessage(`"a b"`)}
	issues := ValidateExtraction(src, []Candidate{
		{CandidateID: "c1", Content: "a"},
		{CandidateID: "c1", Content: "b"},
	})
	if !hasIssue(issues, "duplicate_candidate_id") {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestExtractionRecordsUniqueQuoteRecovery(t *testing.T) {
	stub := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{{
		Content: "用户偏好中文", Kind: memory.KindPreference,
		Evidence: []source.EvidenceReference{{Quote: "用户偏好中文"}},
	}}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"用户偏好中文"`)})
	if err != nil || len(outcome.Candidates) != 1 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	found := false
	for _, diagnostic := range outcome.Diagnostics {
		if diagnostic.Code == "unique_quote" && diagnostic.Field == "evidence[0].segment_id" &&
			diagnostic.Stage == "normalize" && diagnostic.Disposition == "accepted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("recovery diagnostic missing: %+v", outcome.Diagnostics)
	}
	if outcome.Stats.NormalizedCandidates == 0 || outcome.Stats.Normalizations == 0 {
		t.Fatalf("normalization statistics = %+v", outcome.Stats)
	}
}

func TestExtractionAllInvalidCandidatesBecomeUnusable(t *testing.T) {
	stub := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{{Content: "安装软件并且创建账号"}}}}
	stub.repaired = ExtractionResult{Candidates: []Candidate{{CandidateID: "c1", Content: "安装软件并且创建账号"}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"安装软件并且创建账号。"`)})
	var qualityErr *ExtractionQualityError
	if !errors.As(err, &qualityErr) || qualityErr.Quality != QualityUnusable ||
		qualityErr.Kind != "protocol" || qualityErr.Stats.AcceptedCandidates != 0 ||
		qualityErr.Stats.IsolatedCandidates != 1 || len(outcome.Candidates) != 0 {
		t.Fatalf("outcome=%+v err=%v quality=%+v", outcome, err, qualityErr)
	}
}

func TestExtractionWidespreadErrorsRequestStructuredRetry(t *testing.T) {
	stub := &extractionStub{initial: ExtractionResult{Candidates: []Candidate{{Content: "a", Kind: "bad"}, {Content: "b", Kind: "bad"}, {Content: "c", Kind: "bad"}}}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"a b c"`)})
	var qualityErr *ExtractionQualityError
	if !errors.As(err, &qualityErr) || qualityErr.Quality != QualityUnusable ||
		qualityErr.Stats.IsolatedCandidates != 3 || qualityErr.Stats.AcceptedCandidates != 0 ||
		stub.repairCalls != 1 || !stub.request.Retry || len(stub.request.Issues) != 3 ||
		len(outcome.Candidates) != 0 {
		t.Fatalf("err=%v outcome=%+v request=%+v", err, outcome, stub.request)
	}
}

type chunkExtractionStub struct {
	requests []ExtractionRequest
}

func (s *chunkExtractionStub) Extract(_ context.Context, request ExtractionRequest) (ExtractionResult, error) {
	s.requests = append(s.requests, request)
	if len(request.Segments) == 0 {
		return ExtractionResult{}, nil
	}
	candidates := make([]Candidate, 0, len(request.Segments))
	for _, segment := range request.Segments {
		candidates = append(candidates, Candidate{
			Kind: memory.KindFact, Content: segment.Text,
			Evidence: []source.EvidenceReference{{SegmentID: segment.ID, Quote: segment.Text}},
		})
	}
	return ExtractionResult{Candidates: candidates, RawCandidates: len(candidates)}, nil
}

func TestExtractionChunksLongSourceWithStableContext(t *testing.T) {
	stub := &chunkExtractionStub{}
	p := NewProcessor(nil, nil, nil, nil, stub, WithExtractionChunkRunes(10))
	src := source.Source{Content: json.RawMessage(`{"messages":[
		{"id":"m1","content":"第一段内容足够长"},
		{"id":"m2","content":"第二段内容也足够长"},
		{"id":"m3","content":"第三段内容同样长"}
	]}`)}
	outcome, err := p.extractValidated(context.Background(), src)
	if err != nil || len(outcome.Candidates) != 3 || stub.requests == nil {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if outcome.Stats.ExtractionChunks != 3 || len(stub.requests) != 3 {
		t.Fatalf("chunks = %+v requests=%d", outcome.Stats, len(stub.requests))
	}
	if len(stub.requests[1].ContextSegments) != 1 || stub.requests[1].ContextSegments[0].ID != "seg-0001" {
		t.Fatalf("second chunk context = %+v", stub.requests[1].ContextSegments)
	}
	if outcome.Stats.ContextOnlyCandidates != 0 || outcome.Stats.CrossChunkDuplicates != 0 {
		t.Fatalf("unexpected context statistics = %+v", outcome.Stats)
	}
}

func TestExtractionChunkContextIsNotExtractedTwice(t *testing.T) {
	stub := &duplicateChunkStub{}
	p := NewProcessor(nil, nil, nil, nil, stub, WithExtractionChunkRunes(10))
	src := source.Source{Content: json.RawMessage(`{"messages":[
		{"id":"m1","content":"第一段内容足够长"},
		{"id":"m2","content":"第二段内容也足够长"}
	]}`)}
	outcome, err := p.extractValidated(context.Background(), src)
	if err != nil || len(outcome.Candidates) != 1 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if outcome.Stats.CrossChunkDuplicates != 1 {
		t.Fatalf("duplicate statistics = %+v", outcome.Stats)
	}
	if outcome.Retention.NotRetainedByReason[RetentionNotCrossChunkDuplicate] != 1 {
		t.Fatalf("retention = %+v", outcome.Retention)
	}
}

type duplicateChunkStub struct{}

func (duplicateChunkStub) Extract(_ context.Context, request ExtractionRequest) (ExtractionResult, error) {
	if len(request.Segments) == 0 {
		return ExtractionResult{}, nil
	}
	segment := request.Segments[0]
	return ExtractionResult{Candidates: []Candidate{{
		Kind: memory.KindFact, Content: "同一事实", Evidence: []source.EvidenceReference{{SegmentID: segment.ID, Quote: segment.Text}},
	}}, RawCandidates: 1}, nil
}

func TestExtractionCandidateLimitKeepsSafeCandidatesAndDiagnosesOverflow(t *testing.T) {
	candidates := make([]Candidate, 70)
	for index := range candidates {
		candidates[index] = Candidate{Kind: memory.KindFact, Content: "独立事实 " + string(rune('A'+index))}
	}
	stub := &extractionStub{initial: ExtractionResult{Candidates: candidates, RawCandidates: len(candidates)}}
	p := NewProcessor(nil, nil, nil, nil, stub)
	outcome, err := p.extractValidated(context.Background(), source.Source{Content: json.RawMessage(`"原始来源"`)})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(outcome.Candidates) != maxCandidatesPerSource || outcome.quality != QualityPartial {
		t.Fatalf("candidates=%d quality=%q", len(outcome.Candidates), outcome.quality)
	}
	if outcome.Stats.DroppedCandidates != 6 || outcome.Stats.IsolatedCandidates != 6 ||
		outcome.Retention.NotRetainedByReason[RetentionNotCandidateLimit] != 6 {
		t.Fatalf("stats=%+v retention=%+v", outcome.Stats, outcome.Retention)
	}
}
