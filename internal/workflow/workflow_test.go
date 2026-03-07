package workflow

import (
	"testing"
)

func TestParseWithFrontMatter(t *testing.T) {
	content := `---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: my-project
polling:
  interval_ms: 15000
---
You are working on {{ issue.identifier }}: {{ issue.title }}`

	def, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if def.Config == nil {
		t.Fatal("expected non-nil config")
	}

	tracker, ok := def.Config["tracker"].(map[string]interface{})
	if !ok {
		t.Fatal("expected tracker section")
	}
	if tracker["kind"] != "linear" {
		t.Errorf("tracker.kind: got %v, want linear", tracker["kind"])
	}
	if tracker["project_slug"] != "my-project" {
		t.Errorf("tracker.project_slug: got %v, want my-project", tracker["project_slug"])
	}

	if def.PromptTemplate == "" {
		t.Error("expected non-empty prompt template")
	}
	if def.PromptTemplate != "You are working on {{ issue.identifier }}: {{ issue.title }}" {
		t.Errorf("prompt template: got %q", def.PromptTemplate)
	}
}

func TestParseWithoutFrontMatter(t *testing.T) {
	content := `Just a plain prompt template with no YAML front matter.`

	def, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(def.Config) != 0 {
		t.Errorf("expected empty config, got %v", def.Config)
	}
	if def.PromptTemplate != content {
		t.Errorf("prompt template: got %q, want %q", def.PromptTemplate, content)
	}
}

func TestParseEmptyFrontMatter(t *testing.T) {
	content := `---
---
Hello world`

	def, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if def.Config == nil {
		t.Fatal("expected non-nil config")
	}
	if def.PromptTemplate != "Hello world" {
		t.Errorf("prompt template: got %q, want %q", def.PromptTemplate, "Hello world")
	}
}

func TestParseInvalidYAML(t *testing.T) {
	content := `---
tracker:
  kind: linear
  invalid: [
---
prompt here`

	_, err := Parse(content)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	we, ok := err.(*WorkflowError)
	if !ok {
		t.Fatalf("expected *WorkflowError, got %T", err)
	}
	if we.Kind != "workflow_parse_error" {
		t.Errorf("error kind: got %q, want %q", we.Kind, "workflow_parse_error")
	}
}

func TestParseUnclosedFrontMatter(t *testing.T) {
	content := `---
tracker:
  kind: linear
no closing delimiter`

	_, err := Parse(content)
	if err == nil {
		t.Fatal("expected error for unclosed front matter")
	}
}

func TestRenderPromptBasic(t *testing.T) {
	issue := &Issue{
		ID:         "id-123",
		Identifier: "MT-649",
		Title:      "Fix the login bug",
		State:      "Todo",
		Labels:     []string{"bug", "urgent"},
	}

	template := "Work on {{ issue.identifier }}: {{ issue.title }}"
	result, err := RenderPrompt(template, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Work on MT-649: Fix the login bug" {
		t.Errorf("rendered prompt: got %q", result)
	}
}

func TestRenderPromptWithAttempt(t *testing.T) {
	issue := &Issue{
		ID:         "id-123",
		Identifier: "MT-649",
		Title:      "Test",
	}

	template := "Attempt: {{ attempt }}"
	result, err := RenderPrompt(template, issue, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Attempt: 3" {
		t.Errorf("rendered prompt: got %q", result)
	}
}

func TestRenderPromptEmptyTemplate(t *testing.T) {
	issue := &Issue{
		ID:         "id-123",
		Identifier: "MT-649",
		Title:      "Test",
	}

	result, err := RenderPrompt("", issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty default prompt for empty template")
	}
}

func TestRenderPromptIssueFields(t *testing.T) {
	issue := &Issue{
		ID:          "id-123",
		Identifier:  "MT-649",
		Title:       "Fix Bug",
		Description: "Detailed description here",
		State:       "In Progress",
		URL:         "https://linear.app/team/MT-649",
	}

	template := "ID: {{ issue.id }}, State: {{ issue.state }}, URL: {{ issue.url }}"
	result, err := RenderPrompt(template, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "ID: id-123, State: In Progress, URL: https://linear.app/team/MT-649"
	if result != expected {
		t.Errorf("rendered prompt: got %q, want %q", result, expected)
	}
}

func TestIssueToMap(t *testing.T) {
	issue := &Issue{
		ID:         "id-1",
		Identifier: "MT-1",
		Title:      "Test",
		Labels:     []string{"bug"},
		BlockedBy:  []map[string]interface{}{{"id": "blocker-1"}},
	}

	m := issue.ToMap()
	if m["id"] != "id-1" {
		t.Errorf("id: got %v", m["id"])
	}
	if m["identifier"] != "MT-1" {
		t.Errorf("identifier: got %v", m["identifier"])
	}

	labels, ok := m["labels"].([]interface{})
	if !ok {
		t.Fatal("expected labels to be []interface{}")
	}
	if len(labels) != 1 || labels[0] != "bug" {
		t.Errorf("labels: got %v", labels)
	}
}

func TestWorkflowErrorString(t *testing.T) {
	err := &WorkflowError{Kind: "test_error", Message: "something went wrong"}
	if err.Error() != "test_error: something went wrong" {
		t.Errorf("error string: got %q", err.Error())
	}
}
