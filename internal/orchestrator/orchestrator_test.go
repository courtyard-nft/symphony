package orchestrator

import (
	"math"
	"testing"
	"time"

	"github.com/courtyard-nft/symphony/internal/linear"
	"github.com/courtyard-nft/symphony/internal/runner"
)

func TestNewState(t *testing.T) {
	state := NewState(30000, 10)
	if state.PollIntervalMS != 30000 {
		t.Errorf("PollIntervalMS: got %d, want 30000", state.PollIntervalMS)
	}
	if state.MaxConcurrentAgents != 10 {
		t.Errorf("MaxConcurrentAgents: got %d, want 10", state.MaxConcurrentAgents)
	}
	if len(state.Running) != 0 {
		t.Errorf("Running should be empty")
	}
	if len(state.Claimed) != 0 {
		t.Errorf("Claimed should be empty")
	}
}

func TestRunningLifecycle(t *testing.T) {
	state := NewState(30000, 10)

	entry := &RunningEntry{
		IssueID:    "issue-1",
		Identifier: "MT-1",
		StartedAt:  time.Now(),
	}

	// Add
	state.AddRunning("issue-1", entry)
	if !state.IsRunning("issue-1") {
		t.Error("issue should be running after AddRunning")
	}
	if !state.IsClaimed("issue-1") {
		t.Error("issue should be claimed after AddRunning")
	}
	if state.RunningCount() != 1 {
		t.Errorf("RunningCount: got %d, want 1", state.RunningCount())
	}

	// Remove
	removed := state.RemoveRunning("issue-1")
	if removed == nil {
		t.Fatal("RemoveRunning should return the entry")
	}
	if state.IsRunning("issue-1") {
		t.Error("issue should not be running after RemoveRunning")
	}
	if state.RunningCount() != 0 {
		t.Errorf("RunningCount: got %d, want 0", state.RunningCount())
	}
}

func TestAvailableSlots(t *testing.T) {
	state := NewState(30000, 3)

	if state.AvailableSlots() != 3 {
		t.Errorf("initial slots: got %d, want 3", state.AvailableSlots())
	}

	state.AddRunning("a", &RunningEntry{IssueID: "a", StartedAt: time.Now()})
	if state.AvailableSlots() != 2 {
		t.Errorf("after 1 running: got %d, want 2", state.AvailableSlots())
	}

	state.AddRunning("b", &RunningEntry{IssueID: "b", StartedAt: time.Now()})
	state.AddRunning("c", &RunningEntry{IssueID: "c", StartedAt: time.Now()})
	if state.AvailableSlots() != 0 {
		t.Errorf("at max: got %d, want 0", state.AvailableSlots())
	}
}

func TestRetryLifecycle(t *testing.T) {
	state := NewState(30000, 10)

	timer := time.NewTimer(time.Hour) // dummy
	entry := &RetryEntry{
		IssueID:    "issue-2",
		Identifier: "MT-2",
		Attempt:    1,
		DueAtMS:    time.Now().Add(time.Second).UnixMilli(),
		Error:      "test error",
		Timer:      timer,
	}

	state.AddRetry(entry)
	if !state.IsClaimed("issue-2") {
		t.Error("issue should be claimed after AddRetry")
	}

	removed := state.RemoveRetry("issue-2")
	if removed == nil {
		t.Fatal("RemoveRetry should return the entry")
	}
	if removed.Attempt != 1 {
		t.Errorf("attempt: got %d, want 1", removed.Attempt)
	}
}

func TestRetryReplace(t *testing.T) {
	state := NewState(30000, 10)

	timer1 := time.NewTimer(time.Hour)
	entry1 := &RetryEntry{
		IssueID:    "issue-3",
		Identifier: "MT-3",
		Attempt:    1,
		Timer:      timer1,
	}
	state.AddRetry(entry1)

	timer2 := time.NewTimer(time.Hour)
	entry2 := &RetryEntry{
		IssueID:    "issue-3",
		Identifier: "MT-3",
		Attempt:    2,
		Timer:      timer2,
	}
	state.AddRetry(entry2)

	removed := state.RemoveRetry("issue-3")
	if removed.Attempt != 2 {
		t.Errorf("should have replaced, attempt: got %d, want 2", removed.Attempt)
	}
}

func TestRelease(t *testing.T) {
	state := NewState(30000, 10)

	state.AddRunning("issue-4", &RunningEntry{IssueID: "issue-4", StartedAt: time.Now()})
	state.AddRetry(&RetryEntry{IssueID: "issue-4", Timer: time.NewTimer(time.Hour)})

	state.Release("issue-4")

	if state.IsRunning("issue-4") {
		t.Error("should not be running after release")
	}
	if state.IsClaimed("issue-4") {
		t.Error("should not be claimed after release")
	}
}

func TestRunningCountByState(t *testing.T) {
	state := NewState(30000, 10)

	state.AddRunning("a", &RunningEntry{
		IssueID: "a",
		Issue:   linear.Issue{State: "Todo"},
		StartedAt: time.Now(),
	})
	state.AddRunning("b", &RunningEntry{
		IssueID: "b",
		Issue:   linear.Issue{State: "In Progress"},
		StartedAt: time.Now(),
	})
	state.AddRunning("c", &RunningEntry{
		IssueID: "c",
		Issue:   linear.Issue{State: "todo"},
		StartedAt: time.Now(),
	})

	if got := state.RunningCountByState("Todo"); got != 2 {
		t.Errorf("Todo count: got %d, want 2", got)
	}
	if got := state.RunningCountByState("In Progress"); got != 1 {
		t.Errorf("In Progress count: got %d, want 1", got)
	}
	if got := state.RunningCountByState("Done"); got != 0 {
		t.Errorf("Done count: got %d, want 0", got)
	}
}

func TestUpdateTokens(t *testing.T) {
	state := NewState(30000, 10)

	entry := &RunningEntry{
		IssueID:   "issue-5",
		StartedAt: time.Now(),
	}
	state.AddRunning("issue-5", entry)

	// First update
	usage1 := &runner.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	state.UpdateTokens(entry, usage1)

	if state.CodexTotals.InputTokens != 100 {
		t.Errorf("input tokens: got %d, want 100", state.CodexTotals.InputTokens)
	}
	if state.CodexTotals.TotalTokens != 150 {
		t.Errorf("total tokens: got %d, want 150", state.CodexTotals.TotalTokens)
	}

	// Second update (absolute totals — delta tracking)
	usage2 := &runner.TokenUsage{InputTokens: 200, OutputTokens: 100, TotalTokens: 300}
	state.UpdateTokens(entry, usage2)

	if state.CodexTotals.InputTokens != 200 {
		t.Errorf("input tokens after update: got %d, want 200", state.CodexTotals.InputTokens)
	}
	if state.CodexTotals.TotalTokens != 300 {
		t.Errorf("total tokens after update: got %d, want 300", state.CodexTotals.TotalTokens)
	}
}

func TestSnapshot(t *testing.T) {
	state := NewState(30000, 10)

	state.AddRunning("a", &RunningEntry{
		IssueID:    "a",
		Identifier: "MT-1",
		Issue:      linear.Issue{State: "Todo"},
		SessionID:  "session-1",
		TurnCount:  3,
		StartedAt:  time.Now().Add(-time.Minute),
	})

	timer := time.NewTimer(time.Hour)
	state.AddRetry(&RetryEntry{
		IssueID:    "b",
		Identifier: "MT-2",
		Attempt:    2,
		DueAtMS:    time.Now().Add(30 * time.Second).UnixMilli(),
		Error:      "test error",
		Timer:      timer,
	})

	snap := state.Snapshot()
	if snap.Counts.Running != 1 {
		t.Errorf("snapshot running count: got %d, want 1", snap.Counts.Running)
	}
	if snap.Counts.Retrying != 1 {
		t.Errorf("snapshot retrying count: got %d, want 1", snap.Counts.Retrying)
	}
	if len(snap.Running) != 1 {
		t.Fatalf("snapshot running entries: got %d, want 1", len(snap.Running))
	}
	if snap.Running[0].IssueIdentifier != "MT-1" {
		t.Errorf("running identifier: got %q, want %q", snap.Running[0].IssueIdentifier, "MT-1")
	}
	if snap.Running[0].TurnCount != 3 {
		t.Errorf("running turn count: got %d, want 3", snap.Running[0].TurnCount)
	}
}

func TestBackoffDelay(t *testing.T) {
	maxBackoff := 300000 // 5 minutes

	tests := []struct {
		attempt int
		want    int
	}{
		{1, 10000},         // 10s * 2^0 = 10s
		{2, 20000},         // 10s * 2^1 = 20s
		{3, 40000},         // 10s * 2^2 = 40s
		{4, 80000},         // 10s * 2^3 = 80s
		{5, 160000},        // 10s * 2^4 = 160s
		{6, 300000},        // capped at 300s
		{10, 300000},       // still capped
	}

	for _, tt := range tests {
		got := backoffDelay(tt.attempt, maxBackoff)
		if got != tt.want {
			t.Errorf("backoffDelay(%d, %d): got %d, want %d", tt.attempt, maxBackoff, got, tt.want)
		}
	}
}

func TestBackoffFormula(t *testing.T) {
	// Verify the formula matches spec: min(10000 * 2^(attempt-1), max_retry_backoff_ms)
	for attempt := 1; attempt <= 10; attempt++ {
		expected := int(math.Min(10000*math.Pow(2, float64(attempt-1)), 300000))
		got := backoffDelay(attempt, 300000)
		if got != expected {
			t.Errorf("attempt %d: got %d, want %d", attempt, got, expected)
		}
	}
}

func TestSortForDispatch(t *testing.T) {
	now := time.Now()
	p1 := 1
	p2 := 2
	old := now.Add(-time.Hour)
	recent := now

	issues := []linear.Issue{
		{ID: "c", Identifier: "C-1", Priority: &p2, CreatedAt: &old},
		{ID: "a", Identifier: "A-1", Priority: &p1, CreatedAt: &recent},
		{ID: "b", Identifier: "B-1", Priority: &p1, CreatedAt: &old},
		{ID: "d", Identifier: "D-1", Priority: nil, CreatedAt: &old},
	}

	sortForDispatch(issues)

	// Expected order: B-1 (p1, old), A-1 (p1, recent), C-1 (p2, old), D-1 (nil priority last)
	expected := []string{"B-1", "A-1", "C-1", "D-1"}
	for i, want := range expected {
		if issues[i].Identifier != want {
			t.Errorf("position %d: got %q, want %q", i, issues[i].Identifier, want)
		}
	}
}

func TestSortForDispatchNullPriority(t *testing.T) {
	now := time.Now()
	p1 := 1

	issues := []linear.Issue{
		{ID: "a", Identifier: "A-1", Priority: nil, CreatedAt: &now},
		{ID: "b", Identifier: "B-1", Priority: &p1, CreatedAt: &now},
	}

	sortForDispatch(issues)

	if issues[0].Identifier != "B-1" {
		t.Errorf("priority issue should come first: got %q", issues[0].Identifier)
	}
	if issues[1].Identifier != "A-1" {
		t.Errorf("null priority should come last: got %q", issues[1].Identifier)
	}
}

func TestMarkCompleted(t *testing.T) {
	state := NewState(30000, 10)
	state.MarkCompleted("issue-x")

	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.Completed["issue-x"] {
		t.Error("issue should be in completed set")
	}
}

func TestAddRuntimeSeconds(t *testing.T) {
	state := NewState(30000, 10)
	state.AddRuntimeSeconds(10.5)
	state.AddRuntimeSeconds(20.0)

	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.CodexTotals.SecondsRunning != 30.5 {
		t.Errorf("seconds running: got %f, want 30.5", state.CodexTotals.SecondsRunning)
	}
}
