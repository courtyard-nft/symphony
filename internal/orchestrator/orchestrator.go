package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/courtyard-nft/symphony/internal/config"
	"github.com/courtyard-nft/symphony/internal/linear"
	"github.com/courtyard-nft/symphony/internal/runner"
	"github.com/courtyard-nft/symphony/internal/workflow"
	"github.com/courtyard-nft/symphony/internal/workspace"
)

// Orchestrator manages the poll loop, dispatch, retry, and reconciliation.
type Orchestrator struct {
	state     *State
	cfg       *config.Config
	wfLoader  *workflow.Loader
	tracker   *linear.Client
	wsMgr     *workspace.Manager
	logger    *slog.Logger
	cancel    context.CancelFunc
	observers []func(*Snapshot)
}

// NewOrchestrator creates a new orchestrator.
func NewOrchestrator(
	cfg *config.Config,
	wfLoader *workflow.Loader,
	tracker *linear.Client,
	wsMgr *workspace.Manager,
	logger *slog.Logger,
) *Orchestrator {
	state := NewState(cfg.PollIntervalMS(), cfg.MaxConcurrentAgents())
	return &Orchestrator{
		state:    state,
		cfg:      cfg,
		wfLoader: wfLoader,
		tracker:  tracker,
		wsMgr:    wsMgr,
		logger:   logger,
	}
}

// State returns the orchestrator state for external observation.
func (o *Orchestrator) State() *State {
	return o.state
}

// AddObserver adds a function that will be called on state changes.
func (o *Orchestrator) AddObserver(fn func(*Snapshot)) {
	o.observers = append(o.observers, fn)
}

func (o *Orchestrator) notifyObservers() {
	snap := o.state.Snapshot()
	for _, fn := range o.observers {
		fn(snap)
	}
}

// Start begins the orchestration loop.
func (o *Orchestrator) Start(ctx context.Context) error {
	// Validate config before starting
	if err := o.cfg.ValidateDispatch(); err != nil {
		return fmt.Errorf("startup validation failed: %w", err)
	}

	// Startup terminal workspace cleanup
	o.startupTerminalCleanup(ctx)

	ctx, cancel := context.WithCancel(ctx)
	o.cancel = cancel

	// Immediate first tick
	o.tick(ctx)

	// Start poll loop
	go o.pollLoop(ctx)

	return nil
}

// Stop halts the orchestration loop.
func (o *Orchestrator) Stop() {
	if o.cancel != nil {
		o.cancel()
	}
	// Terminate all running workers
	for id, entry := range o.state.Running {
		if entry.WorkerCancel != nil {
			entry.WorkerCancel()
		}
		o.logger.Info("stopping running worker", "issue_id", id, "issue_identifier", entry.Identifier)
	}
}

// TriggerRefresh queues an immediate poll+reconciliation cycle.
func (o *Orchestrator) TriggerRefresh(ctx context.Context) {
	go o.tick(ctx)
}

func (o *Orchestrator) pollLoop(ctx context.Context) {
	for {
		interval := time.Duration(o.state.PollIntervalMS) * time.Millisecond
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			o.tick(ctx)
		}
	}
}

func (o *Orchestrator) tick(ctx context.Context) {
	// 1. Reconcile running issues
	o.reconcile(ctx)

	// 2. Dispatch preflight validation
	if err := o.cfg.ValidateDispatch(); err != nil {
		o.logger.Error("dispatch preflight validation failed", "error", err)
		o.notifyObservers()
		return
	}

	// 3. Fetch candidate issues
	issues, err := o.tracker.FetchCandidateIssues(ctx, o.cfg.TrackerProjectSlug(), o.cfg.ActiveStates())
	if err != nil {
		o.logger.Error("failed to fetch candidate issues", "error", err)
		o.notifyObservers()
		return
	}

	// 4. Sort by dispatch priority
	sortForDispatch(issues)

	// 5. Dispatch eligible issues
	for _, issue := range issues {
		if o.state.AvailableSlots() <= 0 {
			break
		}
		if o.shouldDispatch(issue) {
			o.dispatchIssue(ctx, issue, nil)
		}
	}

	// 6. Notify observers
	o.notifyObservers()

	// Update poll interval from config (dynamic reload)
	o.state.PollIntervalMS = o.cfg.PollIntervalMS()
	o.state.MaxConcurrentAgents = o.cfg.MaxConcurrentAgents()
}

// shouldDispatch checks candidate selection rules per spec s8.2.
func (o *Orchestrator) shouldDispatch(issue linear.Issue) bool {
	// Must have required fields
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}

	// Must be in active states and not terminal
	if !o.cfg.IsActiveState(issue.State) {
		return false
	}
	if o.cfg.IsTerminalState(issue.State) {
		return false
	}

	// Not already running or claimed
	if o.state.IsRunning(issue.ID) {
		return false
	}
	if o.state.IsClaimed(issue.ID) {
		return false
	}

	// Global concurrency slots
	if o.state.AvailableSlots() <= 0 {
		return false
	}

	// Per-state concurrency limits
	perStateLimits := o.cfg.MaxConcurrentAgentsByState()
	if perStateLimits != nil {
		normalized := strings.ToLower(strings.TrimSpace(issue.State))
		if limit, ok := perStateLimits[normalized]; ok {
			if o.state.RunningCountByState(issue.State) >= limit {
				return false
			}
		}
	}

	// Blocker rule for Todo state
	if strings.EqualFold(issue.State, "Todo") {
		for _, blocker := range issue.BlockedBy {
			if blocker.State != nil && !o.cfg.IsTerminalState(*blocker.State) {
				return false // Has non-terminal blocker
			}
		}
	}

	return true
}

// sortForDispatch sorts issues by dispatch priority per spec s8.2.
func sortForDispatch(issues []linear.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]

		// priority ascending (null/unknown sorts last)
		aPri := priorityVal(a.Priority)
		bPri := priorityVal(b.Priority)
		if aPri != bPri {
			return aPri < bPri
		}

		// created_at oldest first
		aTime := timeVal(a.CreatedAt)
		bTime := timeVal(b.CreatedAt)
		if !aTime.Equal(bTime) {
			return aTime.Before(bTime)
		}

		// identifier lexicographic
		return a.Identifier < b.Identifier
	})
}

func priorityVal(p *int) int {
	if p == nil {
		return 999 // null sorts last
	}
	return *p
}

func timeVal(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// dispatchIssue launches a worker for the given issue.
func (o *Orchestrator) dispatchIssue(ctx context.Context, issue linear.Issue, attempt *int) {
	workerCtx, workerCancel := context.WithCancel(ctx)

	entry := &RunningEntry{
		IssueID:      issue.ID,
		Identifier:   issue.Identifier,
		Issue:        issue,
		WorkerCancel: workerCancel,
		StartedAt:    time.Now().UTC(),
		RetryAttempt: attempt,
	}

	o.state.AddRunning(issue.ID, entry)
	o.logger.Info("dispatching issue", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", issue.State)

	go o.runWorker(workerCtx, issue, attempt, entry)
}

// runWorker runs the agent worker lifecycle for a single issue.
func (o *Orchestrator) runWorker(ctx context.Context, issue linear.Issue, attempt *int, entry *RunningEntry) {
	logger := o.logger.With("issue_id", issue.ID, "issue_identifier", issue.Identifier)

	defer func() {
		if r := recover(); r != nil {
			logger.Error("worker panicked", "error", r)
			o.onWorkerExit(issue.ID, false, entry)
		}
	}()

	// 1. Create/reuse workspace
	wsResult, err := o.wsMgr.EnsureWorkspace(issue.Identifier)
	if err != nil {
		logger.Error("workspace creation failed", "error", err)
		o.onWorkerExit(issue.ID, false, entry)
		return
	}

	// Run after_create hook if new
	if wsResult.CreatedNow && o.cfg.HookAfterCreate() != "" {
		if err := workspace.RunHook(wsResult.Path, o.cfg.HookAfterCreate(), o.cfg.HookTimeoutMS(), logger); err != nil {
			logger.Error("after_create hook failed", "error", err)
			// Fatal to workspace creation — remove and fail
			_ = o.wsMgr.RemoveWorkspace(issue.Identifier, o.cfg.HookBeforeRemove(), o.cfg.HookTimeoutMS())
			o.onWorkerExit(issue.ID, false, entry)
			return
		}
	}

	// Validate workspace path safety
	if err := o.wsMgr.ValidateWorkspacePath(wsResult.Path); err != nil {
		logger.Error("invalid workspace path", "error", err)
		o.onWorkerExit(issue.ID, false, entry)
		return
	}

	// Run before_run hook
	if o.cfg.HookBeforeRun() != "" {
		if err := workspace.RunHook(wsResult.Path, o.cfg.HookBeforeRun(), o.cfg.HookTimeoutMS(), logger); err != nil {
			logger.Error("before_run hook failed", "error", err)
			o.onWorkerExit(issue.ID, false, entry)
			return
		}
	}

	// 2. Build prompt
	wfDef := o.wfLoader.Current()
	promptTemplate := ""
	if wfDef != nil {
		promptTemplate = wfDef.PromptTemplate
	}

	wfIssue := linearIssueToWorkflowIssue(issue)
	prompt, err := workflow.RenderPrompt(promptTemplate, wfIssue, attempt)
	if err != nil {
		logger.Error("prompt rendering failed", "error", err)
		o.runAfterRunHookBestEffort(wsResult.Path, logger)
		o.onWorkerExit(issue.ID, false, entry)
		return
	}

	// 3. Start app-server session
	runnerCfg := runner.Config{
		Command:           o.cfg.CodexCommand(),
		ApprovalPolicy:    o.cfg.CodexApprovalPolicy(),
		ThreadSandbox:     o.cfg.CodexThreadSandbox(),
		TurnSandboxPolicy: o.cfg.CodexTurnSandboxPolicy(),
		ReadTimeoutMS:     o.cfg.CodexReadTimeoutMS(),
		TurnTimeoutMS:     o.cfg.CodexTurnTimeoutMS(),
	}
	agentRunner := runner.NewRunner(runnerCfg, logger)

	if err := agentRunner.Start(ctx, wsResult.Path); err != nil {
		logger.Error("agent start failed", "error", err)
		o.runAfterRunHookBestEffort(wsResult.Path, logger)
		o.onWorkerExit(issue.ID, false, entry)
		return
	}
	defer agentRunner.Stop()

	entry.CodexAppServerPID = agentRunner.PID()

	// Handshake
	threadID, err := agentRunner.Handshake(ctx, wsResult.Path)
	if err != nil {
		logger.Error("handshake failed", "error", err)
		o.runAfterRunHookBestEffort(wsResult.Path, logger)
		o.onWorkerExit(issue.ID, false, entry)
		return
	}

	maxTurns := o.cfg.MaxTurns()
	turnNumber := 1
	currentIssue := issue

	for {
		// Build turn prompt (first turn: full prompt, continuation: guidance only)
		turnPrompt := prompt
		if turnNumber > 1 {
			turnPrompt = "Continue working on the issue. Check the current state and proceed with any remaining tasks."
		}

		title := fmt.Sprintf("%s: %s", currentIssue.Identifier, currentIssue.Title)

		// 4. Start turn
		turnID, err := agentRunner.StartTurn(ctx, threadID, turnPrompt, wsResult.Path, title)
		if err != nil {
			logger.Error("turn start failed", "error", err, "turn_number", turnNumber)
			break
		}

		sessionID := fmt.Sprintf("%s-%s", threadID, turnID)
		entry.SessionID = sessionID
		entry.TurnCount = turnNumber

		logger.Info("turn started", "session_id", sessionID, "turn_number", turnNumber)

		// Emit session_started event
		onEvent := func(evt runner.RunnerEvent) {
			now := time.Now().UTC()
			entry.LastCodexEvent = evt.Event
			entry.LastCodexTimestamp = &now
			entry.LastCodexMessage = evt.Message
			if evt.Usage != nil {
				o.state.UpdateTokens(entry, evt.Usage)
			}
			if evt.RateLimits != nil {
				o.state.UpdateRateLimits(evt.RateLimits)
			}
		}

		// 5. Stream turn
		result, err := agentRunner.StreamTurn(ctx, onEvent)
		if err != nil {
			logger.Error("turn streaming failed", "error", err, "turn_number", turnNumber)
			break
		}

		if result != nil && !result.Success {
			logger.Warn("turn ended with failure", "event", result.Event, "error", result.Error, "turn_number", turnNumber)
			break
		}

		// Turn completed successfully — check if issue is still active
		refreshed, err := o.tracker.FetchIssueStatesByIDs(ctx, []string{currentIssue.ID})
		if err != nil {
			logger.Error("issue state refresh failed", "error", err)
			break
		}
		if len(refreshed) > 0 {
			currentIssue = linear.Issue{
				ID:         refreshed[0].ID,
				Identifier: refreshed[0].Identifier,
				Title:      refreshed[0].Title,
				State:      refreshed[0].State,
			}
		}

		if !o.cfg.IsActiveState(currentIssue.State) {
			logger.Info("issue no longer active after turn", "state", currentIssue.State, "turn_number", turnNumber)
			break
		}

		if turnNumber >= maxTurns {
			logger.Info("max turns reached", "max_turns", maxTurns)
			break
		}

		turnNumber++
	}

	// Run after_run hook (best effort)
	o.runAfterRunHookBestEffort(wsResult.Path, logger)

	// Normal exit
	o.onWorkerExit(issue.ID, true, entry)
}

func (o *Orchestrator) runAfterRunHookBestEffort(wsPath string, logger *slog.Logger) {
	if o.cfg.HookAfterRun() != "" {
		if err := workspace.RunHook(wsPath, o.cfg.HookAfterRun(), o.cfg.HookTimeoutMS(), logger); err != nil {
			logger.Warn("after_run hook failed", "error", err)
		}
	}
}

// onWorkerExit handles the transition when a worker finishes.
func (o *Orchestrator) onWorkerExit(issueID string, normal bool, entry *RunningEntry) {
	runningEntry := o.state.RemoveRunning(issueID)
	if runningEntry == nil {
		return
	}

	// Add runtime seconds
	elapsed := time.Since(runningEntry.StartedAt).Seconds()
	o.state.AddRuntimeSeconds(elapsed)

	if normal {
		o.state.MarkCompleted(issueID)
		// Schedule continuation retry (attempt 1) after 1000ms
		o.scheduleRetry(issueID, runningEntry.Identifier, 1, "continuation", 1000)
	} else {
		// Abnormal exit — exponential backoff retry
		nextAttempt := 1
		if entry.RetryAttempt != nil {
			nextAttempt = *entry.RetryAttempt + 1
		}
		delay := backoffDelay(nextAttempt, o.cfg.MaxRetryBackoffMS())
		o.scheduleRetry(issueID, runningEntry.Identifier, nextAttempt, "worker exited abnormally", delay)
	}

	o.notifyObservers()
}

// backoffDelay computes retry delay per spec: min(10000 * 2^(attempt-1), max_retry_backoff_ms).
func backoffDelay(attempt, maxBackoffMS int) int {
	delay := int(10000.0 * math.Pow(2.0, float64(attempt-1)))
	if delay > maxBackoffMS {
		delay = maxBackoffMS
	}
	return delay
}

// scheduleRetry creates a retry timer for the given issue.
func (o *Orchestrator) scheduleRetry(issueID, identifier string, attempt int, errMsg string, delayMS int) {
	dueAt := time.Now().UnixMilli() + int64(delayMS)

	retryEntry := &RetryEntry{
		IssueID:    issueID,
		Identifier: identifier,
		Attempt:    attempt,
		DueAtMS:    dueAt,
		Error:      errMsg,
	}

	timer := time.AfterFunc(time.Duration(delayMS)*time.Millisecond, func() {
		o.onRetryTimer(issueID)
	})
	retryEntry.Timer = timer

	o.state.AddRetry(retryEntry)
	o.logger.Info("scheduled retry", "issue_id", issueID, "attempt", attempt, "delay_ms", delayMS)
}

// onRetryTimer handles a retry timer firing per spec s16.6.
func (o *Orchestrator) onRetryTimer(issueID string) {
	retryEntry := o.state.RemoveRetry(issueID)
	if retryEntry == nil {
		return
	}

	ctx := context.Background()

	// Fetch active candidates
	candidates, err := o.tracker.FetchCandidateIssues(ctx, o.cfg.TrackerProjectSlug(), o.cfg.ActiveStates())
	if err != nil {
		o.logger.Error("retry poll failed", "issue_id", issueID, "error", err)
		o.scheduleRetry(issueID, retryEntry.Identifier, retryEntry.Attempt+1, "retry poll failed", backoffDelay(retryEntry.Attempt+1, o.cfg.MaxRetryBackoffMS()))
		return
	}

	// Find the specific issue
	var found *linear.Issue
	for _, c := range candidates {
		if c.ID == issueID {
			found = &c
			break
		}
	}

	if found == nil {
		// Issue no longer a candidate — release claim
		o.state.Release(issueID)
		o.logger.Info("retry: issue no longer candidate, releasing", "issue_id", issueID)
		return
	}

	// Check if slots are available
	if o.state.AvailableSlots() <= 0 {
		o.scheduleRetry(issueID, retryEntry.Identifier, retryEntry.Attempt+1, "no available orchestrator slots", backoffDelay(retryEntry.Attempt+1, o.cfg.MaxRetryBackoffMS()))
		return
	}

	// Re-dispatch
	attempt := retryEntry.Attempt
	o.dispatchIssue(ctx, *found, &attempt)
}

// reconcile runs active-run reconciliation per spec s8.5.
func (o *Orchestrator) reconcile(ctx context.Context) {
	// Part A: Stall detection
	o.reconcileStalls(ctx)

	// Part B: Tracker state refresh
	runningIDs := o.state.RunningIssueIDs()
	if len(runningIDs) == 0 {
		return
	}

	refreshed, err := o.tracker.FetchIssueStatesByIDs(ctx, runningIDs)
	if err != nil {
		o.logger.Debug("reconciliation state refresh failed, keeping workers running", "error", err)
		return
	}

	// Build map of refreshed states
	stateMap := make(map[string]linear.Issue, len(refreshed))
	for _, issue := range refreshed {
		stateMap[issue.ID] = issue
	}

	for _, id := range runningIDs {
		issue, ok := stateMap[id]
		if !ok {
			continue
		}

		if o.cfg.IsTerminalState(issue.State) {
			// Terminal: terminate worker and clean workspace
			o.terminateRunning(id, true)
		} else if o.cfg.IsActiveState(issue.State) {
			// Active: update in-memory snapshot
			entry := o.state.GetRunning(id)
			if entry != nil {
				entry.Issue = issue
			}
		} else {
			// Neither active nor terminal: terminate without cleanup
			o.terminateRunning(id, false)
		}
	}
}

// reconcileStalls checks for stalled sessions per spec s8.5 Part A.
func (o *Orchestrator) reconcileStalls(ctx context.Context) {
	stallTimeoutMS := o.cfg.CodexStallTimeoutMS()
	if stallTimeoutMS <= 0 {
		return // Stall detection disabled
	}

	now := time.Now()
	o.state.mu.RLock()
	var stalledIDs []string
	for id, entry := range o.state.Running {
		var refTime time.Time
		if entry.LastCodexTimestamp != nil {
			refTime = *entry.LastCodexTimestamp
		} else {
			refTime = entry.StartedAt
		}
		elapsedMS := now.Sub(refTime).Milliseconds()
		if elapsedMS > int64(stallTimeoutMS) {
			stalledIDs = append(stalledIDs, id)
		}
	}
	o.state.mu.RUnlock()

	for _, id := range stalledIDs {
		o.logger.Warn("stall detected, terminating", "issue_id", id)
		entry := o.state.GetRunning(id)
		if entry != nil {
			if entry.WorkerCancel != nil {
				entry.WorkerCancel()
			}
			runningEntry := o.state.RemoveRunning(id)
			if runningEntry != nil {
				elapsed := time.Since(runningEntry.StartedAt).Seconds()
				o.state.AddRuntimeSeconds(elapsed)
				attempt := 1
				if runningEntry.RetryAttempt != nil {
					attempt = *runningEntry.RetryAttempt + 1
				}
				o.scheduleRetry(id, runningEntry.Identifier, attempt, "stall_timeout", backoffDelay(attempt, o.cfg.MaxRetryBackoffMS()))
			}
		}
	}
}

// terminateRunning stops a running worker and optionally cleans the workspace.
func (o *Orchestrator) terminateRunning(issueID string, cleanupWorkspace bool) {
	entry := o.state.GetRunning(issueID)
	if entry == nil {
		return
	}

	o.logger.Info("terminating running issue", "issue_id", issueID, "issue_identifier", entry.Identifier, "cleanup", cleanupWorkspace)

	if entry.WorkerCancel != nil {
		entry.WorkerCancel()
	}

	o.state.RemoveRunning(issueID)
	o.state.Release(issueID)

	if cleanupWorkspace {
		if err := o.wsMgr.RemoveWorkspace(entry.Identifier, o.cfg.HookBeforeRemove(), o.cfg.HookTimeoutMS()); err != nil {
			o.logger.Warn("workspace cleanup failed", "issue_id", issueID, "error", err)
		}
	}
}

// startupTerminalCleanup removes workspaces for issues in terminal states.
func (o *Orchestrator) startupTerminalCleanup(ctx context.Context) {
	terminalIssues, err := o.tracker.FetchIssuesByStates(ctx, o.cfg.TrackerProjectSlug(), o.cfg.TerminalStates())
	if err != nil {
		o.logger.Warn("startup terminal cleanup failed", "error", err)
		return
	}

	for _, issue := range terminalIssues {
		key := workspace.SanitizeIdentifier(issue.ID)
		_ = o.wsMgr.RemoveWorkspace(key, o.cfg.HookBeforeRemove(), o.cfg.HookTimeoutMS())
	}

	o.logger.Info("startup terminal cleanup complete", "cleaned", len(terminalIssues))
}

func linearIssueToWorkflowIssue(issue linear.Issue) *workflow.Issue {
	blockedBy := make([]map[string]interface{}, 0, len(issue.BlockedBy))
	for _, b := range issue.BlockedBy {
		m := make(map[string]interface{})
		if b.ID != nil {
			m["id"] = *b.ID
		}
		if b.Identifier != nil {
			m["identifier"] = *b.Identifier
		}
		if b.State != nil {
			m["state"] = *b.State
		}
		blockedBy = append(blockedBy, m)
	}

	desc := ""
	if issue.Description != nil {
		desc = *issue.Description
	}
	branchName := ""
	if issue.BranchName != nil {
		branchName = *issue.BranchName
	}
	url := ""
	if issue.URL != nil {
		url = *issue.URL
	}
	createdAt := ""
	if issue.CreatedAt != nil {
		createdAt = issue.CreatedAt.Format(time.RFC3339)
	}
	updatedAt := ""
	if issue.UpdatedAt != nil {
		updatedAt = issue.UpdatedAt.Format(time.RFC3339)
	}

	pri := interface{}(nil)
	if issue.Priority != nil {
		pri = *issue.Priority
	}

	return &workflow.Issue{
		ID:          issue.ID,
		Identifier:  issue.Identifier,
		Title:       issue.Title,
		Description: desc,
		Priority:    pri,
		State:       issue.State,
		BranchName:  branchName,
		URL:         url,
		Labels:      issue.Labels,
		BlockedBy:   blockedBy,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}
