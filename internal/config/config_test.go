package config

import (
	"os"
	"testing"
)

func TestNew(t *testing.T) {
	cfg := New(nil)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestDefaults(t *testing.T) {
	cfg := New(map[string]interface{}{})

	if got := cfg.PollIntervalMS(); got != 30000 {
		t.Errorf("PollIntervalMS: got %d, want 30000", got)
	}
	if got := cfg.MaxConcurrentAgents(); got != 10 {
		t.Errorf("MaxConcurrentAgents: got %d, want 10", got)
	}
	if got := cfg.MaxTurns(); got != 20 {
		t.Errorf("MaxTurns: got %d, want 20", got)
	}
	if got := cfg.MaxRetryBackoffMS(); got != 300000 {
		t.Errorf("MaxRetryBackoffMS: got %d, want 300000", got)
	}
	if got := cfg.HookTimeoutMS(); got != 60000 {
		t.Errorf("HookTimeoutMS: got %d, want 60000", got)
	}
	if got := cfg.CodexCommand(); got != "codex app-server" {
		t.Errorf("CodexCommand: got %q, want %q", got, "codex app-server")
	}
	if got := cfg.CodexTurnTimeoutMS(); got != 3600000 {
		t.Errorf("CodexTurnTimeoutMS: got %d, want 3600000", got)
	}
	if got := cfg.CodexReadTimeoutMS(); got != 5000 {
		t.Errorf("CodexReadTimeoutMS: got %d, want 5000", got)
	}
	if got := cfg.CodexStallTimeoutMS(); got != 300000 {
		t.Errorf("CodexStallTimeoutMS: got %d, want 300000", got)
	}

	activeStates := cfg.ActiveStates()
	if len(activeStates) != 2 || activeStates[0] != "Todo" || activeStates[1] != "In Progress" {
		t.Errorf("ActiveStates: got %v, want [Todo, In Progress]", activeStates)
	}

	terminalStates := cfg.TerminalStates()
	if len(terminalStates) != 5 {
		t.Errorf("TerminalStates: got %d elements, want 5", len(terminalStates))
	}
}

func TestTrackerConfigParsing(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"api_key":      "test-key-123",
			"project_slug": "my-project",
			"endpoint":     "https://custom.endpoint/graphql",
		},
	}
	cfg := New(raw)

	if got := cfg.TrackerKind(); got != "linear" {
		t.Errorf("TrackerKind: got %q, want %q", got, "linear")
	}
	if got := cfg.TrackerAPIKey(); got != "test-key-123" {
		t.Errorf("TrackerAPIKey: got %q, want %q", got, "test-key-123")
	}
	if got := cfg.TrackerProjectSlug(); got != "my-project" {
		t.Errorf("TrackerProjectSlug: got %q, want %q", got, "my-project")
	}
	if got := cfg.TrackerEndpoint(); got != "https://custom.endpoint/graphql" {
		t.Errorf("TrackerEndpoint: got %q, want %q", got, "https://custom.endpoint/graphql")
	}
}

func TestTrackerDefaultEndpoint(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind": "linear",
		},
	}
	cfg := New(raw)
	if got := cfg.TrackerEndpoint(); got != "https://api.linear.app/graphql" {
		t.Errorf("TrackerEndpoint default: got %q, want %q", got, "https://api.linear.app/graphql")
	}
}

func TestEnvResolution(t *testing.T) {
	os.Setenv("TEST_SYMPHONY_KEY", "resolved-value")
	defer os.Unsetenv("TEST_SYMPHONY_KEY")

	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"api_key":      "$TEST_SYMPHONY_KEY",
			"project_slug": "test",
		},
	}
	cfg := New(raw)

	if got := cfg.TrackerAPIKey(); got != "resolved-value" {
		t.Errorf("TrackerAPIKey with $VAR: got %q, want %q", got, "resolved-value")
	}
}

func TestEnvResolutionCanonicalLinear(t *testing.T) {
	os.Setenv("LINEAR_API_KEY", "canonical-key")
	defer os.Unsetenv("LINEAR_API_KEY")

	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"project_slug": "test",
		},
	}
	cfg := New(raw)

	if got := cfg.TrackerAPIKey(); got != "canonical-key" {
		t.Errorf("TrackerAPIKey canonical: got %q, want %q", got, "canonical-key")
	}
}

func TestActiveStatesCustom(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"active_states": []interface{}{"Ready", "Working"},
		},
	}
	cfg := New(raw)
	states := cfg.ActiveStates()
	if len(states) != 2 || states[0] != "Ready" || states[1] != "Working" {
		t.Errorf("ActiveStates custom: got %v", states)
	}
}

func TestActiveStatesString(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"active_states": "Ready, Working",
		},
	}
	cfg := New(raw)
	states := cfg.ActiveStates()
	if len(states) != 2 || states[0] != "Ready" || states[1] != "Working" {
		t.Errorf("ActiveStates string: got %v", states)
	}
}

func TestValidateDispatchMissingKind(t *testing.T) {
	cfg := New(map[string]interface{}{})
	err := cfg.ValidateDispatch()
	if err == nil {
		t.Fatal("expected validation error for missing tracker.kind")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "tracker.kind" {
		t.Errorf("expected field tracker.kind, got %s", ve.Field)
	}
}

func TestValidateDispatchUnsupportedKind(t *testing.T) {
	cfg := New(map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind": "jira",
		},
	})
	err := cfg.ValidateDispatch()
	if err == nil {
		t.Fatal("expected validation error for unsupported tracker.kind")
	}
}

func TestValidateDispatchMissingAPIKey(t *testing.T) {
	os.Unsetenv("LINEAR_API_KEY")
	cfg := New(map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"project_slug": "test",
		},
	})
	err := cfg.ValidateDispatch()
	if err == nil {
		t.Fatal("expected validation error for missing api_key")
	}
}

func TestValidateDispatchMissingSlug(t *testing.T) {
	cfg := New(map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":    "linear",
			"api_key": "test-key",
		},
	})
	err := cfg.ValidateDispatch()
	if err == nil {
		t.Fatal("expected validation error for missing project_slug")
	}
}

func TestValidateDispatchSuccess(t *testing.T) {
	cfg := New(map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"api_key":      "test-key",
			"project_slug": "test",
		},
	})
	if err := cfg.ValidateDispatch(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestIsActiveState(t *testing.T) {
	cfg := New(map[string]interface{}{})
	if !cfg.IsActiveState("Todo") {
		t.Error("expected Todo to be active")
	}
	if !cfg.IsActiveState("In Progress") {
		t.Error("expected In Progress to be active")
	}
	if !cfg.IsActiveState("todo") {
		t.Error("expected todo (lowercase) to be active")
	}
	if cfg.IsActiveState("Done") {
		t.Error("expected Done to not be active")
	}
}

func TestIsTerminalState(t *testing.T) {
	cfg := New(map[string]interface{}{})
	if !cfg.IsTerminalState("Closed") {
		t.Error("expected Closed to be terminal")
	}
	if !cfg.IsTerminalState("Done") {
		t.Error("expected Done to be terminal")
	}
	if !cfg.IsTerminalState("cancelled") {
		t.Error("expected cancelled (lowercase) to be terminal")
	}
	if cfg.IsTerminalState("Todo") {
		t.Error("expected Todo to not be terminal")
	}
}

func TestPerStateConcurrency(t *testing.T) {
	raw := map[string]interface{}{
		"agent": map[string]interface{}{
			"max_concurrent_agents_by_state": map[string]interface{}{
				"Todo":        2,
				"In Progress": 5,
				"invalid":     -1,
			},
		},
	}
	cfg := New(raw)
	limits := cfg.MaxConcurrentAgentsByState()
	if limits == nil {
		t.Fatal("expected non-nil per-state limits")
	}
	if got := limits["todo"]; got != 2 {
		t.Errorf("todo limit: got %d, want 2", got)
	}
	if got := limits["in progress"]; got != 5 {
		t.Errorf("in progress limit: got %d, want 5", got)
	}
	if _, ok := limits["invalid"]; ok {
		t.Error("expected invalid limit to be filtered out")
	}
}

func TestUpdate(t *testing.T) {
	cfg := New(map[string]interface{}{
		"polling": map[string]interface{}{
			"interval_ms": 1000,
		},
	})
	if got := cfg.PollIntervalMS(); got != 1000 {
		t.Errorf("initial: got %d, want 1000", got)
	}

	cfg.Update(map[string]interface{}{
		"polling": map[string]interface{}{
			"interval_ms": 5000,
		},
	})
	if got := cfg.PollIntervalMS(); got != 5000 {
		t.Errorf("after update: got %d, want 5000", got)
	}
}

func TestResolveEnv(t *testing.T) {
	os.Setenv("RESOLVE_TEST_VAR", "hello")
	defer os.Unsetenv("RESOLVE_TEST_VAR")

	if got := ResolveEnv("$RESOLVE_TEST_VAR"); got != "hello" {
		t.Errorf("ResolveEnv: got %q, want %q", got, "hello")
	}
	if got := ResolveEnv("plain-value"); got != "plain-value" {
		t.Errorf("ResolveEnv plain: got %q, want %q", got, "plain-value")
	}
}

func TestNormalizeState(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Todo", "todo"},
		{" In Progress ", "in progress"},
		{"DONE", "done"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeState(tt.input); got != tt.want {
			t.Errorf("NormalizeState(%q): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWorkspaceRootDefault(t *testing.T) {
	cfg := New(map[string]interface{}{})
	root := cfg.WorkspaceRoot()
	if root == "" {
		t.Error("expected non-empty default workspace root")
	}
}

func TestServerPort(t *testing.T) {
	cfg := New(map[string]interface{}{
		"server": map[string]interface{}{
			"port": 8080,
		},
	})
	if got := cfg.ServerPort(); got != 8080 {
		t.Errorf("ServerPort: got %d, want 8080", got)
	}
}

func TestServerPortDefault(t *testing.T) {
	cfg := New(map[string]interface{}{})
	if got := cfg.ServerPort(); got != 0 {
		t.Errorf("ServerPort default: got %d, want 0", got)
	}
}
