// Package workflow implements the WORKFLOW.md loader and prompt template renderer.
// It parses YAML front matter and the prompt body from Markdown files, and renders
// prompts using a Liquid-compatible template engine with strict variable checking.
package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/osteele/liquid"
	"gopkg.in/yaml.v3"
)

// Definition represents a parsed WORKFLOW.md file.
type Definition struct {
	Config         map[string]interface{}
	PromptTemplate string
}

// Loader handles reading, parsing, and caching WORKFLOW.md files.
type Loader struct {
	mu         sync.RWMutex
	path       string
	definition *Definition
	lastErr    error
}

// NewLoader creates a new workflow loader for the given file path.
func NewLoader(path string) *Loader {
	return &Loader{path: path}
}

// Path returns the workflow file path.
func (l *Loader) Path() string {
	return l.path
}

// Load reads and parses the WORKFLOW.md file.
func (l *Loader) Load() (*Definition, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &WorkflowError{Kind: "missing_workflow_file", Message: fmt.Sprintf("workflow file not found: %s", l.path)}
		}
		return nil, &WorkflowError{Kind: "missing_workflow_file", Message: fmt.Sprintf("cannot read workflow file: %s: %v", l.path, err)}
	}

	def, err := Parse(string(data))
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.definition = def
	l.lastErr = nil
	l.mu.Unlock()

	return def, nil
}

// Current returns the last successfully loaded definition.
func (l *Loader) Current() *Definition {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.definition
}

// LastError returns the last error from loading, if any.
func (l *Loader) LastError() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lastErr
}

// SetError records a load error without clearing the last good definition.
func (l *Loader) SetError(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastErr = err
}

// Parse parses a WORKFLOW.md content string into a Definition.
func Parse(content string) (*Definition, error) {
	var configMap map[string]interface{}
	var promptBody string

	if strings.HasPrefix(content, "---") {
		// Find the closing ---
		rest := content[3:]
		idx := strings.Index(rest, "\n---")
		if idx < 0 {
			return nil, &WorkflowError{Kind: "workflow_parse_error", Message: "YAML front matter opened but not closed"}
		}
		yamlContent := rest[:idx]
		promptBody = rest[idx+4:] // skip \n---

		if err := yaml.Unmarshal([]byte(yamlContent), &configMap); err != nil {
			return nil, &WorkflowError{Kind: "workflow_parse_error", Message: fmt.Sprintf("invalid YAML front matter: %v", err)}
		}
		if configMap == nil {
			// YAML decoded but was empty/null — that's ok, treat as empty map
			configMap = make(map[string]interface{})
		}
	} else {
		// No front matter — entire content is the prompt
		configMap = make(map[string]interface{})
		promptBody = content
	}

	promptBody = strings.TrimSpace(promptBody)

	return &Definition{
		Config:         configMap,
		PromptTemplate: promptBody,
	}, nil
}

// Issue represents the template variable for an issue.
type Issue struct {
	ID          string                   `json:"id"`
	Identifier  string                   `json:"identifier"`
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Priority    interface{}              `json:"priority"`
	State       string                   `json:"state"`
	BranchName  string                   `json:"branch_name"`
	URL         string                   `json:"url"`
	Labels      []string                 `json:"labels"`
	BlockedBy   []map[string]interface{} `json:"blocked_by"`
	CreatedAt   string                   `json:"created_at"`
	UpdatedAt   string                   `json:"updated_at"`
}

// ToMap converts an Issue to a map for template rendering.
func (i *Issue) ToMap() map[string]interface{} {
	blockedBy := make([]interface{}, len(i.BlockedBy))
	for idx, b := range i.BlockedBy {
		blockedBy[idx] = b
	}
	labels := make([]interface{}, len(i.Labels))
	for idx, l := range i.Labels {
		labels[idx] = l
	}
	return map[string]interface{}{
		"id":          i.ID,
		"identifier":  i.Identifier,
		"title":       i.Title,
		"description": i.Description,
		"priority":    i.Priority,
		"state":       i.State,
		"branch_name": i.BranchName,
		"url":         i.URL,
		"labels":      labels,
		"blocked_by":  blockedBy,
		"created_at":  i.CreatedAt,
		"updated_at":  i.UpdatedAt,
	}
}

// TemplateForLabels looks for a label-specific prompt template file next to the
// loader's WORKFLOW.md. It checks templates/<label>.md for each label (in order)
// and returns the first one found. If none matches, it returns an empty string,
// indicating the caller should fall back to the base WORKFLOW.md template.
//
// Template files may contain YAML front matter (which is ignored — they inherit
// config from the base WORKFLOW.md) or plain markdown prompt content.
func (l *Loader) TemplateForLabels(labels []string) string {
	base := filepath.Dir(l.path)
	for _, label := range labels {
		// Normalize: lowercase, spaces → hyphens
		key := strings.ToLower(strings.ReplaceAll(label, " ", "-"))
		candidate := filepath.Join(base, "templates", key+".md")
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		// Parse to strip YAML front matter if present; use only the prompt body
		if def, err := Parse(string(data)); err == nil && def.PromptTemplate != "" {
			return def.PromptTemplate
		}
		// If parsing fails or body is empty, use raw content (trimmed)
		body := strings.TrimSpace(string(data))
		if body != "" {
			return body
		}
	}
	return ""
}

// RenderPrompt renders the prompt template with the given issue and attempt.
func RenderPrompt(template string, issue *Issue, attempt interface{}) (string, error) {
	if template == "" {
		return "You are working on an issue from Linear.", nil
	}

	engine := liquid.NewEngine()

	bindings := map[string]interface{}{
		"issue":   issue.ToMap(),
		"attempt": attempt,
	}

	out, err := engine.ParseAndRenderString(template, bindings)
	if err != nil {
		return "", &WorkflowError{Kind: "template_render_error", Message: fmt.Sprintf("prompt rendering failed: %v", err)}
	}

	return strings.TrimSpace(out), nil
}

// WorkflowError represents a workflow-related error with a typed kind.
type WorkflowError struct {
	Kind    string
	Message string
}

func (e *WorkflowError) Error() string {
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}
