// Package workspace implements the workspace manager for Symphony.
// It handles per-issue workspace directory creation, reuse, cleanup,
// identifier sanitization, safety invariants, and lifecycle hooks.
package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// unsafeChars matches characters not in [A-Za-z0-9._-].
var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._\-]`)

// SanitizeIdentifier replaces any character not in [A-Za-z0-9._-] with _.
func SanitizeIdentifier(identifier string) string {
	return unsafeChars.ReplaceAllString(identifier, "_")
}

// Result represents the result of a workspace creation/reuse operation.
type Result struct {
	Path         string
	WorkspaceKey string
	CreatedNow   bool
}

// Manager handles workspace lifecycle operations.
type Manager struct {
	mu     sync.RWMutex
	root   string
	logger *slog.Logger
}

// NewManager creates a new workspace manager.
func NewManager(root string, logger *slog.Logger) *Manager {
	return &Manager{
		root:   root,
		logger: logger,
	}
}

// Root returns the workspace root path.
func (m *Manager) Root() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.root
}

// UpdateRoot updates the workspace root path (for dynamic config reload).
func (m *Manager) UpdateRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.root = root
}

// WorkspacePath computes the absolute workspace path for an issue identifier.
func (m *Manager) WorkspacePath(identifier string) (string, error) {
	m.mu.RLock()
	root := m.root
	m.mu.RUnlock()
	key := SanitizeIdentifier(identifier)
	wsPath := filepath.Join(root, key)

	// Safety invariant: workspace path must be under workspace root
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace root: %w", err)
	}
	absPath, err := filepath.Abs(wsPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace path: %w", err)
	}

	if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) && absPath != absRoot {
		return "", fmt.Errorf("workspace path %q escapes root %q", absPath, absRoot)
	}

	return absPath, nil
}

// EnsureWorkspace creates or reuses a workspace for the given issue identifier.
func (m *Manager) EnsureWorkspace(identifier string) (*Result, error) {
	key := SanitizeIdentifier(identifier)
	wsPath, err := m.WorkspacePath(identifier)
	if err != nil {
		return nil, err
	}

	// Check if it already exists
	info, err := os.Stat(wsPath)
	if err == nil {
		if !info.IsDir() {
			// Not a directory — remove and recreate
			if err := os.Remove(wsPath); err != nil {
				return nil, fmt.Errorf("failed to remove non-directory at workspace path: %w", err)
			}
			if err := os.MkdirAll(wsPath, 0o755); err != nil {
				return nil, fmt.Errorf("failed to create workspace directory: %w", err)
			}
			return &Result{Path: wsPath, WorkspaceKey: key, CreatedNow: true}, nil
		}
		// Directory exists — reuse
		m.logger.Info("reusing existing workspace", "identifier", identifier, "path", wsPath)
		return &Result{Path: wsPath, WorkspaceKey: key, CreatedNow: false}, nil
	}

	// Create new
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create workspace directory: %w", err)
	}
	m.logger.Info("created new workspace", "identifier", identifier, "path", wsPath)
	return &Result{Path: wsPath, WorkspaceKey: key, CreatedNow: true}, nil
}

// RemoveWorkspace removes a workspace directory, running before_remove hook first.
func (m *Manager) RemoveWorkspace(identifier string, beforeRemoveScript string, hookTimeoutMS int) error {
	wsPath, err := m.WorkspacePath(identifier)
	if err != nil {
		return err
	}

	info, statErr := os.Stat(wsPath)
	if statErr != nil || !info.IsDir() {
		return nil // nothing to remove
	}

	// Run before_remove hook (failure logged and ignored)
	if beforeRemoveScript != "" {
		if err := RunHook(wsPath, beforeRemoveScript, hookTimeoutMS, m.logger); err != nil {
			m.logger.Warn("before_remove hook failed", "identifier", identifier, "error", err)
		}
	}

	if err := os.RemoveAll(wsPath); err != nil {
		return fmt.Errorf("failed to remove workspace: %w", err)
	}
	m.logger.Info("removed workspace", "identifier", identifier, "path", wsPath)
	return nil
}

// ValidateWorkspacePath checks that the given path is a valid workspace under root.
func (m *Manager) ValidateWorkspacePath(wsPath string) error {
	m.mu.RLock()
	root := m.root
	m.mu.RUnlock()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("failed to resolve workspace root: %w", err)
	}
	absPath, err := filepath.Abs(wsPath)
	if err != nil {
		return fmt.Errorf("failed to resolve workspace path: %w", err)
	}

	if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) && absPath != absRoot {
		return fmt.Errorf("invalid_workspace_cwd: %q is not under workspace root %q", absPath, absRoot)
	}
	return nil
}

// RunHook executes a shell hook script in the given working directory with a timeout.
func RunHook(cwd, script string, timeoutMS int, logger *slog.Logger) error {
	if script == "" {
		return nil
	}
	if timeoutMS <= 0 {
		timeoutMS = 60000
	}

	logger.Info("running hook", "cwd", cwd, "timeout_ms", timeoutMS)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("hook timed out after %dms", timeoutMS)
		}
		return fmt.Errorf("hook failed: %w", err)
	}
	return nil
}
