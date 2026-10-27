package skill

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/artifact"
)

type fakeReader struct {
	detail artifact.Detail
	err    error
	gotVer int
}

func (f *fakeReader) Get(context.Context, string) (artifact.Detail, error) {
	return f.detail, f.err
}

func (f *fakeReader) GetVersion(_ context.Context, _ string, version int) (artifact.Detail, error) {
	f.gotVer = version
	return f.detail, f.err
}

func TestDraftRequiresActionableGuidance(t *testing.T) {
	for _, draft := range []Draft{
		{},
		{Name: "只有名字"},
		{Content: "这是一段没有步骤的说明。"},
	} {
		if draft.Usable() {
			t.Fatalf("draft without actionable guidance was usable: %+v", draft)
		}
	}
	for _, draft := range []Draft{
		{Steps: []string{"运行 go test"}},
		{Content: "## 步骤\n1. 运行 go test。"},
		{Content: "- 先检查前置条件"},
	} {
		if !draft.Usable() || draft.Issue() != "" {
			t.Fatalf("structured draft was rejected: %+v issue=%q", draft, draft.Issue())
		}
	}
}

func TestRenderProducesSingleLineFrontmatter(t *testing.T) {
	content := Render("验证\n工作流", "修改后\n验证", "## 步骤\n1. 运行 go test")
	if !strings.HasPrefix(content, "---\nname: \"验证 工作流\"\ndescription: \"修改后 验证\"\n---\n\n") {
		t.Fatalf("content = %q", content)
	}
	if !strings.HasSuffix(content, "运行 go test\n") {
		t.Fatalf("content suffix = %q", content)
	}
}

func TestServiceServesOnlyActiveCurrentByDefault(t *testing.T) {
	revision := &artifact.Revision{
		ArtifactID: "skill-1", Version: 2, Title: "验证工作流", Content: "步骤",
		Payload: []byte(`{"description":"修改后的验证"}`),
	}
	reader := &fakeReader{detail: artifact.Detail{
		Artifact: artifact.Artifact{
			ID: "skill-1", Kind: artifact.KindSkill, State: artifact.StateStale, CurrentVersion: 2,
		},
		Revision: revision,
	}}
	service := NewService(reader)
	if _, err := service.Get(context.Background(), "skill-1", 0); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("stale default error = %v", err)
	}
	content, err := service.Get(context.Background(), "skill-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if reader.gotVer != 2 || content.State != "stale" || content.Version != 2 ||
		content.Description != "修改后的验证" || !strings.Contains(content.Content, "name:") {
		t.Fatalf("content = %+v", content)
	}

	reader.detail.Artifact.State = artifact.StateActive
	content, err = service.Get(context.Background(), "skill-1", 0)
	if err != nil || content.State != "active" {
		t.Fatalf("active content = %+v err=%v", content, err)
	}
}
