// Package config provides typed configuration derived from WORKFLOW.md YAML front matter.
// It handles defaults, environment variable indirection ($VAR), path expansion,
// dynamic reload via fsnotify, and dispatch preflight validation.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Config holds the typed runtime configuration derived from WORKFLOW.md front matter.
type Config struct {
	mu sync.RWMutex
	// raw holds the parsed YAML front matter map.
	raw map[string]interface{}
}

// New creates a Config from a parsed YAML front matter map.
func New(raw map[string]interface{}) *Config {
	if raw == nil {
		raw = make(map[string]interface{})
	}
	return &Config{raw: raw}
}

// Update replaces the raw config atomically.
func (c *Config) Update(raw map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if raw == nil {
		raw = make(map[string]interface{})
	}
	c.raw = raw
}

// Raw returns a copy of the raw config map.
func (c *Config) Raw() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := make(map[string]interface{}, len(c.raw))
	for k, v := range c.raw {
		cp[k] = v
	}
	return cp
}

// --- Helper accessors ---

func (c *Config) getSection(key string) map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if v, ok := c.raw[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	return nil
}

func getStr(m map[string]interface{}, key, def string) string {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	s := fmt.Sprintf("%v", v)
	if s == "" {
		return def
	}
	return s
}

func getInt(m map[string]interface{}, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return def
}

func getStringList(m map[string]interface{}, key string, def []string) []string {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	switch val := v.(type) {
	case []interface{}:
		result := make([]string, 0, len(val))
		for _, item := range val {
			result = append(result, fmt.Sprintf("%v", item))
		}
		return result
	case string:
		parts := strings.Split(val, ",")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	}
	return def
}

// ResolveEnv resolves $VAR_NAME references in a string to environment variable values.
func ResolveEnv(s string) string {
	if strings.HasPrefix(s, "$") {
		varName := s[1:]
		return os.Getenv(varName)
	}
	return s
}

// ExpandPath expands ~ and $VAR in path strings.
func ExpandPath(s string) string {
	if s == "" {
		return s
	}
	// Resolve $VAR first
	if strings.HasPrefix(s, "$") {
		s = ResolveEnv(s)
	}
	// Expand ~
	if strings.HasPrefix(s, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			s = filepath.Join(home, s[1:])
		}
	}
	// If it contains path separators, make absolute
	if strings.Contains(s, string(filepath.Separator)) || strings.Contains(s, "/") {
		abs, err := filepath.Abs(s)
		if err == nil {
			s = abs
		}
	}
	return s
}

// --- Tracker config ---

// TrackerKind returns the tracker kind (e.g., "linear").
func (c *Config) TrackerKind() string {
	return getStr(c.getSection("tracker"), "kind", "")
}

// TrackerEndpoint returns the tracker API endpoint.
func (c *Config) TrackerEndpoint() string {
	kind := c.TrackerKind()
	def := ""
	if kind == "linear" {
		def = "https://api.linear.app/graphql"
	}
	return getStr(c.getSection("tracker"), "endpoint", def)
}

// TrackerAPIKey returns the resolved tracker API key.
func (c *Config) TrackerAPIKey() string {
	raw := getStr(c.getSection("tracker"), "api_key", "")
	if raw == "" {
		// Canonical env var for linear
		if c.TrackerKind() == "linear" {
			return os.Getenv("LINEAR_API_KEY")
		}
		return ""
	}
	resolved := ResolveEnv(raw)
	if resolved == "" {
		// If $VAR resolves to empty, treat as missing
		return ""
	}
	return resolved
}

// TrackerProjectSlug returns the project slug for filtering issues.
func (c *Config) TrackerProjectSlug() string {
	return getStr(c.getSection("tracker"), "project_slug", "")
}

// ActiveStates returns the list of active issue states.
func (c *Config) ActiveStates() []string {
	return getStringList(c.getSection("tracker"), "active_states", []string{"Todo", "In Progress"})
}

// TerminalStates returns the list of terminal issue states.
func (c *Config) TerminalStates() []string {
	return getStringList(c.getSection("tracker"), "terminal_states", []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"})
}

// --- Polling config ---

// PollIntervalMS returns the polling interval in milliseconds.
func (c *Config) PollIntervalMS() int {
	return getInt(c.getSection("polling"), "interval_ms", 30000)
}

// --- Workspace config ---

// WorkspaceRoot returns the expanded workspace root path.
func (c *Config) WorkspaceRoot() string {
	raw := getStr(c.getSection("workspace"), "root", "")
	if raw == "" {
		return filepath.Join(os.TempDir(), "symphony_workspaces")
	}
	return ExpandPath(raw)
}

// --- Hooks config ---

// HookAfterCreate returns the after_create hook script, or empty string.
func (c *Config) HookAfterCreate() string {
	return getStr(c.getSection("hooks"), "after_create", "")
}

// HookBeforeRun returns the before_run hook script, or empty string.
func (c *Config) HookBeforeRun() string {
	return getStr(c.getSection("hooks"), "before_run", "")
}

// HookAfterRun returns the after_run hook script, or empty string.
func (c *Config) HookAfterRun() string {
	return getStr(c.getSection("hooks"), "after_run", "")
}

// HookBeforeRemove returns the before_remove hook script, or empty string.
func (c *Config) HookBeforeRemove() string {
	return getStr(c.getSection("hooks"), "before_remove", "")
}

// HookTimeoutMS returns the hook timeout in milliseconds.
func (c *Config) HookTimeoutMS() int {
	val := getInt(c.getSection("hooks"), "timeout_ms", 60000)
	if val <= 0 {
		return 60000
	}
	return val
}

// --- Agent config ---

// MaxConcurrentAgents returns the global concurrency limit.
func (c *Config) MaxConcurrentAgents() int {
	return getInt(c.getSection("agent"), "max_concurrent_agents", 10)
}

// MaxTurns returns the maximum number of turns per worker session.
func (c *Config) MaxTurns() int {
	return getInt(c.getSection("agent"), "max_turns", 20)
}

// MaxRetryBackoffMS returns the maximum retry backoff in milliseconds.
func (c *Config) MaxRetryBackoffMS() int {
	return getInt(c.getSection("agent"), "max_retry_backoff_ms", 300000)
}

// MaxConcurrentAgentsByState returns per-state concurrency limits.
func (c *Config) MaxConcurrentAgentsByState() map[string]int {
	section := c.getSection("agent")
	if section == nil {
		return nil
	}
	v, ok := section["max_concurrent_agents_by_state"]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	result := make(map[string]int, len(m))
	for k, val := range m {
		normalized := strings.ToLower(strings.TrimSpace(k))
		switch n := val.(type) {
		case int:
			if n > 0 {
				result[normalized] = n
			}
		case int64:
			if n > 0 {
				result[normalized] = int(n)
			}
		case float64:
			if int(n) > 0 {
				result[normalized] = int(n)
			}
		case string:
			if i, err := strconv.Atoi(n); err == nil && i > 0 {
				result[normalized] = i
			}
		}
	}
	return result
}

// --- Codex config ---

// CodexCommand returns the codex command string.
func (c *Config) CodexCommand() string {
	return getStr(c.getSection("codex"), "command", "codex app-server")
}

// CodexApprovalPolicy returns the approval policy value.
func (c *Config) CodexApprovalPolicy() string {
	return getStr(c.getSection("codex"), "approval_policy", "never")
}

// CodexThreadSandbox returns the thread sandbox mode value.
func (c *Config) CodexThreadSandbox() string {
	return getStr(c.getSection("codex"), "thread_sandbox", "workspace-write")
}

// CodexTurnSandboxPolicy returns the turn sandbox policy.
func (c *Config) CodexTurnSandboxPolicy() map[string]interface{} {
	section := c.getSection("codex")
	if section == nil {
		return map[string]interface{}{"type": "workspaceWrite"}
	}
	v, ok := section["turn_sandbox_policy"]
	if !ok {
		return map[string]interface{}{"type": "workspaceWrite"}
	}
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	if s, ok := v.(string); ok {
		return map[string]interface{}{"type": s}
	}
	return map[string]interface{}{"type": "workspaceWrite"}
}

// CodexTurnTimeoutMS returns the turn timeout in milliseconds.
func (c *Config) CodexTurnTimeoutMS() int {
	return getInt(c.getSection("codex"), "turn_timeout_ms", 3600000)
}

// CodexReadTimeoutMS returns the read timeout in milliseconds.
func (c *Config) CodexReadTimeoutMS() int {
	return getInt(c.getSection("codex"), "read_timeout_ms", 5000)
}

// CodexStallTimeoutMS returns the stall timeout in milliseconds.
func (c *Config) CodexStallTimeoutMS() int {
	return getInt(c.getSection("codex"), "stall_timeout_ms", 300000)
}

// --- Server config (extension) ---

// ServerPort returns the server port from the extension config, or 0 if not set.
func (c *Config) ServerPort() int {
	return getInt(c.getSection("server"), "port", 0)
}

// --- Validation ---

// ValidationError represents a preflight validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config validation: %s: %s", e.Field, e.Message)
}

// ValidateDispatch performs dispatch preflight validation per spec s6.3.
func (c *Config) ValidateDispatch() error {
	if c.TrackerKind() == "" {
		return &ValidationError{Field: "tracker.kind", Message: "required but missing"}
	}
	if c.TrackerKind() != "linear" {
		return &ValidationError{Field: "tracker.kind", Message: fmt.Sprintf("unsupported tracker kind: %s", c.TrackerKind())}
	}
	if c.TrackerAPIKey() == "" {
		return &ValidationError{Field: "tracker.api_key", Message: "required but missing after $VAR resolution"}
	}
	if c.TrackerKind() == "linear" && c.TrackerProjectSlug() == "" {
		return &ValidationError{Field: "tracker.project_slug", Message: "required for linear tracker kind"}
	}
	if c.CodexCommand() == "" {
		return &ValidationError{Field: "codex.command", Message: "required but empty"}
	}
	return nil
}

// NormalizeState normalizes a state name by trimming whitespace and lowercasing.
func NormalizeState(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// IsActiveState checks if a state is in the active states list.
func (c *Config) IsActiveState(state string) bool {
	normalized := NormalizeState(state)
	for _, s := range c.ActiveStates() {
		if NormalizeState(s) == normalized {
			return true
		}
	}
	return false
}

// IsTerminalState checks if a state is in the terminal states list.
func (c *Config) IsTerminalState(state string) bool {
	normalized := NormalizeState(state)
	for _, s := range c.TerminalStates() {
		if NormalizeState(s) == normalized {
			return true
		}
	}
	return false
}
