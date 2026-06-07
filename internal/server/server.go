// Package server implements the optional HTTP server extension for Symphony.
// It provides a JSON REST API for runtime state inspection and a human-readable
// HTML dashboard at the root path.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/courtyard-nft/symphony/internal/orchestrator"
)

// Server is the optional HTTP observability server.
type Server struct {
	orch   *orchestrator.Orchestrator
	logger *slog.Logger
	server *http.Server
	addr   string
}

// NewServer creates a new HTTP server.
func NewServer(orch *orchestrator.Orchestrator, logger *slog.Logger) *Server {
	return &Server{
		orch:   orch,
		logger: logger,
	}
}

// Start starts the HTTP server on the given port.
func (s *Server) Start(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/api/v1/state", s.handleAPIState)
	mux.HandleFunc("/api/v1/refresh", s.handleAPIRefresh)
	mux.HandleFunc("/api/v1/", s.handleAPIIssue)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.addr = ln.Addr().String()
	s.logger.Info("HTTP server started", "addr", s.addr)

	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error("HTTP server error", "error", err)
		}
	}()

	return nil
}

// Addr returns the server's listen address.
func (s *Server) Addr() string {
	return s.addr
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// handleStatus returns JSON runtime snapshot (legacy endpoint).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	snap := s.orch.State().Snapshot()
	s.writeJSON(w, http.StatusOK, snap)
}

// handleAPIState returns the full state summary.
func (s *Server) handleAPIState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	snap := s.orch.State().Snapshot()
	s.writeJSON(w, http.StatusOK, snap)
}

// handleAPIRefresh triggers an immediate poll+reconcile cycle.
func (s *Server) handleAPIRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is supported")
		return
	}
	s.orch.TriggerRefresh(r.Context())
	resp := map[string]interface{}{
		"queued":       true,
		"coalesced":    false,
		"requested_at": time.Now().UTC().Format(time.RFC3339),
		"operations":   []string{"poll", "reconcile"},
	}
	s.writeJSON(w, http.StatusAccepted, resp)
}

// handleAPIIssue handles GET /api/v1/<issue_identifier>.
func (s *Server) handleAPIIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}

	// Extract identifier from path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	path = strings.TrimSuffix(path, "/")
	if path == "" || path == "state" || path == "refresh" {
		// These are handled by other handlers
		s.writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}

	identifier := path
	snap := s.orch.State().Snapshot()

	// Search in running
	for _, entry := range snap.Running {
		if entry.IssueIdentifier == identifier {
			s.writeJSON(w, http.StatusOK, map[string]interface{}{
				"issue_identifier": entry.IssueIdentifier,
				"issue_id":         entry.IssueID,
				"status":           "running",
				"running": map[string]interface{}{
					"session_id":    entry.SessionID,
					"turn_count":    entry.TurnCount,
					"state":         entry.State,
					"started_at":    entry.StartedAt,
					"last_event":    entry.LastEvent,
					"last_message":  entry.LastMessage,
					"last_event_at": entry.LastEventAt,
					"tokens":        entry.Tokens,
				},
				"retry": nil,
			})
			return
		}
	}

	// Search in retrying
	for _, entry := range snap.Retrying {
		if entry.IssueIdentifier == identifier {
			s.writeJSON(w, http.StatusOK, map[string]interface{}{
				"issue_identifier": entry.IssueIdentifier,
				"issue_id":         entry.IssueID,
				"status":           "retrying",
				"running":          nil,
				"retry": map[string]interface{}{
					"attempt": entry.Attempt,
					"due_at":  entry.DueAt,
					"error":   entry.Error,
				},
			})
			return
		}
	}

	s.writeError(w, http.StatusNotFound, "issue_not_found", fmt.Sprintf("issue %q not found in current state", identifier))
}

// handleDashboard serves the human-readable HTML dashboard.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}

	snap := s.orch.State().Snapshot()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTmpl.Execute(w, snap); err != nil {
		s.logger.Error("dashboard render failed", "error", err)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("failed to encode JSON response", "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html>
<head>
  <title>Symphony Dashboard</title>
  <meta charset="utf-8">
  <meta http-equiv="refresh" content="10">
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 2rem; background: #f5f5f5; }
    h1 { color: #333; }
    .card { background: white; border-radius: 8px; padding: 1rem; margin: 1rem 0; box-shadow: 0 1px 3px rgba(0,0,0,0.12); }
    table { width: 100%; border-collapse: collapse; }
    th, td { text-align: left; padding: 0.5rem; border-bottom: 1px solid #eee; }
    th { font-weight: 600; color: #555; }
    .metric { display: inline-block; margin-right: 2rem; }
    .metric-value { font-size: 1.5rem; font-weight: 700; color: #333; }
    .metric-label { font-size: 0.8rem; color: #888; }
    .status-running { color: #2196F3; }
    .status-retrying { color: #FF9800; }
  </style>
</head>
<body>
  <h1>Symphony Dashboard</h1>
  <p>Generated: {{.GeneratedAt.Format "2006-01-02T15:04:05Z07:00"}}</p>

  <div class="card">
    <div class="metric">
      <div class="metric-value">{{.Counts.Running}}</div>
      <div class="metric-label">Running</div>
    </div>
    <div class="metric">
      <div class="metric-value">{{.Counts.Retrying}}</div>
      <div class="metric-label">Retrying</div>
    </div>
    <div class="metric">
      <div class="metric-value">{{.CodexTotals.TotalTokens}}</div>
      <div class="metric-label">Total Tokens</div>
    </div>
    <div class="metric">
      <div class="metric-value">{{printf "%.1f" .CodexTotals.SecondsRunning}}s</div>
      <div class="metric-label">Runtime</div>
    </div>
  </div>

  {{if .Running}}
  <div class="card">
    <h2 class="status-running">Running Sessions</h2>
    <table>
      <tr>
        <th>Issue</th><th>State</th><th>Session</th><th>Turns</th><th>Last Event</th><th>Started</th><th>Tokens</th>
      </tr>
      {{range .Running}}
      <tr>
        <td>{{.IssueIdentifier}}</td>
        <td>{{.State}}</td>
        <td>{{.SessionID}}</td>
        <td>{{.TurnCount}}</td>
        <td>{{.LastEvent}}</td>
        <td>{{.StartedAt.Format "15:04:05"}}</td>
        <td>{{.Tokens.TotalTokens}}</td>
      </tr>
      {{end}}
    </table>
  </div>
  {{end}}

  {{if .Retrying}}
  <div class="card">
    <h2 class="status-retrying">Retry Queue</h2>
    <table>
      <tr>
        <th>Issue</th><th>Attempt</th><th>Due At</th><th>Error</th>
      </tr>
      {{range .Retrying}}
      <tr>
        <td>{{.IssueIdentifier}}</td>
        <td>{{.Attempt}}</td>
        <td>{{.DueAt.Format "15:04:05"}}</td>
        <td>{{.Error}}</td>
      </tr>
      {{end}}
    </table>
  </div>
  {{end}}
</body>
</html>`))
