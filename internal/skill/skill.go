// Package skill owns guidance-skill input/output types, SKILL.md rendering and
// the read-only content view. Skills are guidance text with traceable sources;
// the service never claims execution verification or grants tool permissions.
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/artifact"
)

// Request is the bounded input for one L4 skill-generation call. Inputs carry
// only stable procedure memories, L1/L2 aggregates and application-reported
// experience runs; raw observations are never selected for an L4 skill.
type Request struct {
	ScopeKey  string              `json:"scope_key"`
	Topic     string              `json:"topic,omitempty"`
	Inputs    []artifact.InputRef `json:"inputs"`
	Truncated bool                `json:"truncated"`
}

// Draft is the tolerant parsed model output. At least Content or one usable
// structured section must be present; auxiliary fields may be missing.
type Draft struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Content          string   `json:"content"`
	Preconditions    []string `json:"preconditions"`
	Steps            []string `json:"steps"`
	StopConditions   []string `json:"stop_conditions"`
	CompletionChecks []string `json:"completion_checks"`
	Counterexamples  []string `json:"counterexamples"`
}

// Usable reports whether the draft contains actionable guidance that can be
// saved. A bare paragraph is not enough: at least one step or a structured
// heading/list is required so a name-only or description-only response cannot
// become an active skill.
func (d Draft) Usable() bool {
	if len(clean(d.Steps)) > 0 {
		return true
	}
	return hasGuidanceStructure(d.Content)
}

// Issue returns a fixed content-check code for a draft that is structurally
// usable but still not eligible for activation. It never echoes model text.
func (d Draft) Issue() string {
	if len(clean(d.Steps)) > 0 {
		return ""
	}
	content := strings.TrimSpace(d.Content)
	if content == "" {
		return "empty_content"
	}
	if !hasGuidanceStructure(content) {
		return "missing_steps"
	}
	return ""
}

func hasGuidanceStructure(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- ") ||
			strings.HasPrefix(line, "* ") {
			return true
		}
		digits := 0
		for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
			digits++
		}
		if digits > 0 && digits < len(line) && (line[digits] == '.' || line[digits] == ')') {
			return true
		}
	}
	return false
}

// Content is one rendered SKILL.md plus the exact version metadata. State is
// included so an explicit historical read cannot be mistaken for active advice.
type Content struct {
	ArtifactID  string               `json:"id"`
	Version     int                  `json:"version"`
	State       string               `json:"state"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Content     string               `json:"content"`
	Truncated   bool                 `json:"truncated"`
	MemoryRefs  []artifact.MemoryRef `json:"memory_refs"`
	RunRefs     []artifact.RunRef    `json:"run_refs"`
}

// Render produces the single SKILL.md guidance document. The stored artifact
// content is the body; frontmatter is generated here so name and description are
// always present and cannot be confused with executable metadata.
func Render(name, description, body string) string {
	name = singleLine(name)
	description = singleLine(description)
	if name == "" {
		name = "guidance"
	}
	if description == "" {
		description = name
	}
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("name: ")
	builder.WriteString(strconv.Quote(name))
	builder.WriteString("\ndescription: ")
	builder.WriteString(strconv.Quote(description))
	builder.WriteString("\n---\n\n")
	builder.WriteString(strings.TrimSpace(body))
	builder.WriteString("\n")
	return builder.String()
}

// reader is the artifact read capability used by the content endpoint.
type reader interface {
	Get(ctx context.Context, id string) (artifact.Detail, error)
	GetVersion(ctx context.Context, id string, version int) (artifact.Detail, error)
}

// Service renders active current skill content and explicit historical versions.
type Service struct {
	artifacts reader
}

// NewService assembles the skill content view.
func NewService(artifacts reader) *Service {
	return &Service{artifacts: artifacts}
}

// Get returns the active current skill by default. An explicit positive version
// returns that immutable revision and marks its owning artifact state.
func (s *Service) Get(ctx context.Context, id string, version int) (Content, error) {
	if s == nil || s.artifacts == nil {
		return Content{}, fmt.Errorf("skill: artifact reader not configured")
	}
	var detail artifact.Detail
	var err error
	if version > 0 {
		detail, err = s.artifacts.GetVersion(ctx, id, version)
	} else {
		detail, err = s.artifacts.Get(ctx, id)
		if err == nil && detail.DerivedMemory.State != artifact.StateActive {
			return Content{}, fmt.Errorf("%w: %s", artifact.ErrNotFound, id)
		}
	}
	if err != nil {
		return Content{}, err
	}
	if detail.DerivedMemory.Kind != artifact.KindSkill || detail.Revision == nil ||
		detail.DerivedMemory.CurrentVersion == 0 && version <= 0 {
		return Content{}, fmt.Errorf("%w: %s", artifact.ErrNotFound, id)
	}
	description := payloadDescription(detail.Revision.Payload)
	if description == "" {
		description = detail.Revision.Title
	}
	return Content{
		ArtifactID:  detail.DerivedMemory.ID,
		Version:     detail.Revision.Version,
		State:       string(detail.DerivedMemory.State),
		Name:        detail.Revision.Title,
		Description: description,
		Content:     Render(detail.Revision.Title, description, detail.Revision.Content),
		Truncated:   detail.Revision.Truncated,
	}, nil
}

// ContentService is the HTTP-facing read capability.
type ContentService interface {
	Get(ctx context.Context, id string, version int) (Content, error)
}

var _ ContentService = (*Service)(nil)

func payloadDescription(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var fields struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return ""
	}
	return strings.TrimSpace(fields.Description)
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func clean(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	return result
}
