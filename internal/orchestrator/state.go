// Package orchestrator implements the Symphony orchestration state machine.
// It manages the poll loop, issue dispatch, concurrency control, retry/backoff,
// reconciliation, and startup terminal workspace cleanup.
package orchestrator

import (
	"strings"
	"sync"
	"time"

	"github.com/courtyard-nft/symphony/internal/linear"
	"github.com/courtyard-nft/symphony/internal/runner"
)

// RunningEntry tracks a running agent session for an issue.
type RunningEntry struct {
	IssueID               string
	Identifier            string
	Issue                 linear.Issue
	WorkerCancel          func()
	SessionID             string
	CodexAppServerPID     string
	LastCodexMessage      string
	LastCodexEvent        string
	LastCodexTimestamp     *time.Time
	CodexInputTokens      int
	CodexOutputTokens     int
	CodexTotalTokens      int
	LastReportedInputTkns int
	LastReportedOutputTkns int
	LastReportedTotalTkns int
	RetryAttempt          *int
	StartedAt             time.Time
	TurnCount             int
}

// RetryEntry holds scheduled retry state for an issue.
type RetryEntry struct {
	IssueID    string
	Identifier string
	Attempt    int
	DueAtMS    int64 // monotonic ms
	Error      string
	Timer      *time.Timer
}

// CodexTotals holds aggregate token and runtime counters.
type CodexTotals struct {
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	TotalTokens    int     `json:"total_tokens"`
	SecondsRunning float64 `json:"seconds_running"`
}

// State is the single authoritative in-memory orchestrator state.
type State struct {
	mu                  sync.RWMutex
	PollIntervalMS      int
	MaxConcurrentAgents int
	Running             map[string]*RunningEntry   // issue_id -> entry
	Claimed             map[string]bool            // set of issue IDs
	RetryAttempts       map[string]*RetryEntry     // issue_id -> entry
	Completed           map[string]bool            // set of issue IDs (bookkeeping)
	CodexTotals         CodexTotals
	CodexRateLimits     map[string]interface{}
}

// NewState creates a fresh orchestrator state.
func NewState(pollIntervalMS, maxConcurrentAgents int) *State {
	return &State{
		PollIntervalMS:      pollIntervalMS,
		MaxConcurrentAgents: maxConcurrentAgents,
		Running:             make(map[string]*RunningEntry),
		Claimed:             make(map[string]bool),
		RetryAttempts:       make(map[string]*RetryEntry),
		Completed:           make(map[string]bool),
	}
}

// --- Thread-safe accessors ---

// RunningCount returns the number of currently running agents.
func (s *State) RunningCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Running)
}

// AvailableSlots returns how many more agents can be dispatched.
func (s *State) AvailableSlots() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	slots := s.MaxConcurrentAgents - len(s.Running)
	if slots < 0 {
		return 0
	}
	return slots
}

// RunningCountByState returns how many running issues are in a given state.
func (s *State) RunningCountByState(state string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, entry := range s.Running {
		if normalizeState(entry.Issue.State) == normalizeState(state) {
			count++
		}
	}
	return count
}

// IsRunning checks if an issue is currently running.
func (s *State) IsRunning(issueID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.Running[issueID]
	return ok
}

// IsClaimed checks if an issue is claimed.
func (s *State) IsClaimed(issueID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Claimed[issueID]
}

// AddRunning adds a running entry for an issue.
func (s *State) AddRunning(issueID string, entry *RunningEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Running[issueID] = entry
	s.Claimed[issueID] = true
	delete(s.RetryAttempts, issueID)
}

// RemoveRunning removes a running entry and returns it.
func (s *State) RemoveRunning(issueID string) *RunningEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.Running[issueID]
	if !ok {
		return nil
	}
	delete(s.Running, issueID)
	return entry
}

// AddRetry adds or replaces a retry entry for an issue.
func (s *State) AddRetry(entry *RetryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Cancel any existing timer
	if existing, ok := s.RetryAttempts[entry.IssueID]; ok && existing.Timer != nil {
		existing.Timer.Stop()
	}
	s.RetryAttempts[entry.IssueID] = entry
	s.Claimed[entry.IssueID] = true
}

// RemoveRetry removes a retry entry and returns it.
func (s *State) RemoveRetry(issueID string) *RetryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.RetryAttempts[issueID]
	if !ok {
		return nil
	}
	if entry.Timer != nil {
		entry.Timer.Stop()
	}
	delete(s.RetryAttempts, issueID)
	return entry
}

// Release removes all claims for an issue.
func (s *State) Release(issueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Running, issueID)
	delete(s.Claimed, issueID)
	if entry, ok := s.RetryAttempts[issueID]; ok && entry.Timer != nil {
		entry.Timer.Stop()
	}
	delete(s.RetryAttempts, issueID)
}

// MarkCompleted adds an issue to the completed set.
func (s *State) MarkCompleted(issueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Completed[issueID] = true
}

// AddRuntimeSeconds adds elapsed seconds to the aggregate total.
func (s *State) AddRuntimeSeconds(seconds float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CodexTotals.SecondsRunning += seconds
}

// UpdateTokens updates aggregate token counts using delta tracking.
func (s *State) UpdateTokens(entry *RunningEntry, usage *runner.TokenUsage) {
	if usage == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Compute deltas relative to last reported to avoid double-counting
	inputDelta := usage.InputTokens - entry.LastReportedInputTkns
	outputDelta := usage.OutputTokens - entry.LastReportedOutputTkns
	totalDelta := usage.TotalTokens - entry.LastReportedTotalTkns

	if inputDelta > 0 {
		s.CodexTotals.InputTokens += inputDelta
	}
	if outputDelta > 0 {
		s.CodexTotals.OutputTokens += outputDelta
	}
	if totalDelta > 0 {
		s.CodexTotals.TotalTokens += totalDelta
	}

	entry.CodexInputTokens = usage.InputTokens
	entry.CodexOutputTokens = usage.OutputTokens
	entry.CodexTotalTokens = usage.TotalTokens
	entry.LastReportedInputTkns = usage.InputTokens
	entry.LastReportedOutputTkns = usage.OutputTokens
	entry.LastReportedTotalTkns = usage.TotalTokens
}

// UpdateRateLimits stores the latest rate limit data.
func (s *State) UpdateRateLimits(rl map[string]interface{}) {
	if rl == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CodexRateLimits = rl
}

// RunningIssueIDs returns the IDs of all currently running issues.
func (s *State) RunningIssueIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.Running))
	for id := range s.Running {
		ids = append(ids, id)
	}
	return ids
}

// GetRunning returns a copy of the running entry for an issue.
func (s *State) GetRunning(issueID string) *RunningEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Running[issueID]
}

// Snapshot returns a point-in-time snapshot of the state for observability.
func (s *State) Snapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().UTC()

	running := make([]SnapshotRunning, 0, len(s.Running))
	for _, entry := range s.Running {
		running = append(running, SnapshotRunning{
			IssueID:        entry.IssueID,
			IssueIdentifier: entry.Identifier,
			State:          entry.Issue.State,
			SessionID:      entry.SessionID,
			TurnCount:      entry.TurnCount,
			LastEvent:      entry.LastCodexEvent,
			LastMessage:    entry.LastCodexMessage,
			StartedAt:      entry.StartedAt,
			LastEventAt:    entry.LastCodexTimestamp,
			Tokens: SnapshotTokens{
				InputTokens:  entry.CodexInputTokens,
				OutputTokens: entry.CodexOutputTokens,
				TotalTokens:  entry.CodexTotalTokens,
			},
		})
	}

	retrying := make([]SnapshotRetrying, 0, len(s.RetryAttempts))
	for _, entry := range s.RetryAttempts {
		dueAt := time.UnixMilli(entry.DueAtMS)
		retrying = append(retrying, SnapshotRetrying{
			IssueID:        entry.IssueID,
			IssueIdentifier: entry.Identifier,
			Attempt:        entry.Attempt,
			DueAt:          dueAt,
			Error:          entry.Error,
		})
	}

	// Compute live seconds_running including active sessions
	liveSeconds := s.CodexTotals.SecondsRunning
	for _, entry := range s.Running {
		liveSeconds += now.Sub(entry.StartedAt).Seconds()
	}

	return &Snapshot{
		GeneratedAt: now,
		Counts: SnapshotCounts{
			Running:  len(s.Running),
			Retrying: len(s.RetryAttempts),
		},
		Running:  running,
		Retrying: retrying,
		CodexTotals: SnapshotCodexTotals{
			InputTokens:    s.CodexTotals.InputTokens,
			OutputTokens:   s.CodexTotals.OutputTokens,
			TotalTokens:    s.CodexTotals.TotalTokens,
			SecondsRunning: liveSeconds,
		},
		RateLimits: s.CodexRateLimits,
	}
}

// --- Snapshot types ---

// Snapshot represents a point-in-time view of the orchestrator state.
type Snapshot struct {
	GeneratedAt time.Time              `json:"generated_at"`
	Counts      SnapshotCounts         `json:"counts"`
	Running     []SnapshotRunning      `json:"running"`
	Retrying    []SnapshotRetrying     `json:"retrying"`
	CodexTotals SnapshotCodexTotals    `json:"codex_totals"`
	RateLimits  map[string]interface{} `json:"rate_limits"`
}

// SnapshotCounts holds summary counts.
type SnapshotCounts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
}

// SnapshotRunning holds info about a running session.
type SnapshotRunning struct {
	IssueID         string          `json:"issue_id"`
	IssueIdentifier string          `json:"issue_identifier"`
	State           string          `json:"state"`
	SessionID       string          `json:"session_id"`
	TurnCount       int             `json:"turn_count"`
	LastEvent       string          `json:"last_event"`
	LastMessage     string          `json:"last_message"`
	StartedAt       time.Time       `json:"started_at"`
	LastEventAt     *time.Time      `json:"last_event_at"`
	Tokens          SnapshotTokens  `json:"tokens"`
}

// SnapshotTokens holds token counts.
type SnapshotTokens struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// SnapshotRetrying holds info about a retry entry.
type SnapshotRetrying struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	Attempt         int       `json:"attempt"`
	DueAt           time.Time `json:"due_at"`
	Error           string    `json:"error"`
}

// SnapshotCodexTotals holds aggregate totals.
type SnapshotCodexTotals struct {
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	TotalTokens    int     `json:"total_tokens"`
	SecondsRunning float64 `json:"seconds_running"`
}

func normalizeState(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
