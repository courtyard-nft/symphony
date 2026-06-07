package workspace

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeIdentifier(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"ABC-123", "ABC-123"},
		{"simple", "simple"},
		{"with spaces", "with_spaces"},
		{"with/slashes", "with_slashes"},
		{"with@special#chars!", "with_special_chars_"},
		{"dots.and-dashes_ok", "dots.and-dashes_ok"},
		{"日本語", "___"},
		{"", ""},
		{"a.b-c_d", "a.b-c_d"},
		{"MT-649", "MT-649"},
		{"PROJECT/ISSUE-1", "PROJECT_ISSUE-1"},
	}

	for _, tt := range tests {
		got := SanitizeIdentifier(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeIdentifier(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWorkspacePath(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	path, err := mgr.WorkspacePath("MT-649")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(root, "MT-649")
	absExpected, _ := filepath.Abs(expected)
	if path != absExpected {
		t.Errorf("WorkspacePath: got %q, want %q", path, absExpected)
	}
}

func TestWorkspacePathDeterministic(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	path1, err := mgr.WorkspacePath("MT-649")
	if err != nil {
		t.Fatal(err)
	}
	path2, err := mgr.WorkspacePath("MT-649")
	if err != nil {
		t.Fatal(err)
	}
	if path1 != path2 {
		t.Errorf("workspace paths should be deterministic: %q != %q", path1, path2)
	}
}

func TestWorkspacePathSanitized(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	path, err := mgr.WorkspacePath("PROJECT/ISSUE-1")
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(root, "PROJECT_ISSUE-1")
	absExpected, _ := filepath.Abs(expected)
	if path != absExpected {
		t.Errorf("WorkspacePath sanitized: got %q, want %q", path, absExpected)
	}
}

func TestEnsureWorkspaceCreate(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	result, err := mgr.EnsureWorkspace("MT-650")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.CreatedNow {
		t.Error("expected CreatedNow=true for new workspace")
	}
	if result.WorkspaceKey != "MT-650" {
		t.Errorf("WorkspaceKey: got %q, want %q", result.WorkspaceKey, "MT-650")
	}

	// Verify directory exists
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatalf("workspace dir should exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("workspace should be a directory")
	}
}

func TestEnsureWorkspaceReuse(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Create first
	result1, err := mgr.EnsureWorkspace("MT-651")
	if err != nil {
		t.Fatal(err)
	}
	if !result1.CreatedNow {
		t.Error("first call should create")
	}

	// Reuse
	result2, err := mgr.EnsureWorkspace("MT-651")
	if err != nil {
		t.Fatal(err)
	}
	if result2.CreatedNow {
		t.Error("second call should reuse, not create")
	}
	if result1.Path != result2.Path {
		t.Error("paths should be identical on reuse")
	}
}

func TestWorkspacePathEscapeDetection(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Attempt path traversal — the sanitizer will replace .. chars with _
	// but we test ValidateWorkspacePath directly
	outsidePath := filepath.Join(root, "..", "outside")
	err := mgr.ValidateWorkspacePath(outsidePath)
	if err == nil {
		t.Error("expected error for path outside root")
	}
}

func TestValidateWorkspacePathInside(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	insidePath := filepath.Join(root, "valid-workspace")
	err := mgr.ValidateWorkspacePath(insidePath)
	if err != nil {
		t.Errorf("expected valid path inside root, got error: %v", err)
	}
}

func TestRemoveWorkspace(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Create workspace
	result, err := mgr.EnsureWorkspace("MT-652")
	if err != nil {
		t.Fatal(err)
	}

	// Verify it exists
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatal("workspace should exist before removal")
	}

	// Remove it
	if err := mgr.RemoveWorkspace("MT-652", "", 60000); err != nil {
		t.Fatalf("unexpected error removing workspace: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Error("workspace should not exist after removal")
	}
}

func TestRemoveWorkspaceNonexistent(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Should not error on non-existent workspace
	if err := mgr.RemoveWorkspace("NONEXISTENT", "", 60000); err != nil {
		t.Errorf("unexpected error removing non-existent workspace: %v", err)
	}
}
