// Package runner implements the agent runner which manages the codex app-server
// subprocess lifecycle. It handles JSON-RPC handshake (initialize, initialized,
// thread/start, turn/start), streaming stdout parsing, approval handling,
// turn continuation, and timeout enforcement.
package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event types emitted by the runner to the orchestrator.
const (
	EventSessionStarted    = "session_started"
	EventStartupFailed     = "startup_failed"
	EventTurnCompleted     = "turn_completed"
	EventTurnFailed        = "turn_failed"
	EventTurnCancelled     = "turn_cancelled"
	EventTurnEndedError    = "turn_ended_with_error"
	EventTurnInputRequired = "turn_input_required"
	EventApprovalAutoApproved = "approval_auto_approved"
	EventUnsupportedToolCall  = "unsupported_tool_call"
	EventNotification      = "notification"
	EventOtherMessage      = "other_message"
	EventMalformed         = "malformed"
)

// RunnerEvent is emitted from the runner to the orchestrator callback.
type RunnerEvent struct {
	Event              string                 `json:"event"`
	Timestamp          time.Time              `json:"timestamp"`
	CodexAppServerPID  string                 `json:"codex_app_server_pid,omitempty"`
	SessionID          string                 `json:"session_id,omitempty"`
	Usage              *TokenUsage            `json:"usage,omitempty"`
	RateLimits         map[string]interface{} `json:"rate_limits,omitempty"`
	Payload            map[string]interface{} `json:"payload,omitempty"`
	Message            string                 `json:"message,omitempty"`
}

// TokenUsage holds token count information.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// SessionResult holds the outcome of an agent session.
type SessionResult struct {
	ThreadID  string
	TurnID    string
	SessionID string
	Success   bool
	Error     string
	Event     string // terminal event type
}

// Config holds runner configuration.
type Config struct {
	Command           string
	ApprovalPolicy    string
	ThreadSandbox     string
	TurnSandboxPolicy map[string]interface{}
	ReadTimeoutMS     int
	TurnTimeoutMS     int
}

// Runner manages a codex app-server subprocess.
type Runner struct {
	config    Config
	logger    *slog.Logger
	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	scanner   *bufio.Scanner // shared scanner for stdout to avoid data loss
	pid       string
	requestID int
}

// NewRunner creates a new agent runner.
func NewRunner(config Config, logger *slog.Logger) *Runner {
	return &Runner{
		config:    config,
		logger:    logger,
		requestID: 0,
	}
}

// nextID returns the next JSON-RPC request ID.
func (r *Runner) nextID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestID++
	return r.requestID
}

// Start launches the codex app-server subprocess and performs the handshake.
func (r *Runner) Start(ctx context.Context, workspacePath string) error {
	r.logger.Info("launching agent", "command", r.config.Command, "cwd", workspacePath)

	cmd := exec.CommandContext(ctx, "bash", "-lc", r.config.Command)
	cmd.Dir = workspacePath

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("codex_not_found: failed to create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("codex_not_found: failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("codex_not_found: failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("codex_not_found: failed to start codex: %w", err)
	}

	r.cmd = cmd
	r.stdin = stdin
	r.stdout = stdout
	r.stderr = stderr
	if cmd.Process != nil {
		r.pid = fmt.Sprintf("%d", cmd.Process.Pid)
	}

	// Create a single shared scanner for stdout
	r.scanner = bufio.NewScanner(r.stdout)
	r.scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024) // 10MB max line

	// Drain stderr in background
	go r.drainStderr()

	return nil
}

// drainStderr reads and logs stderr without attempting JSON parse.
func (r *Runner) drainStderr() {
	scanner := bufio.NewScanner(r.stderr)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024) // 10MB max line
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			r.logger.Debug("codex stderr", "line", truncate(line, 500))
		}
	}
}

// Handshake performs the JSON-RPC initialization handshake.
func (r *Runner) Handshake(ctx context.Context, workspacePath string) (string, error) {
	readTimeout := time.Duration(r.config.ReadTimeoutMS) * time.Millisecond

	// 1. Send initialize request
	initID := r.nextID()
	initReq := map[string]interface{}{
		"id":     initID,
		"method": "initialize",
		"params": map[string]interface{}{
			"clientInfo": map[string]interface{}{
				"name":    "symphony",
				"version": "1.0",
			},
			"capabilities": map[string]interface{}{},
		},
	}
	if err := r.sendJSON(initReq); err != nil {
		return "", fmt.Errorf("response_timeout: failed to send initialize: %w", err)
	}

	// Wait for initialize response
	if _, err := r.readResponseWithTimeout(ctx, initID, readTimeout); err != nil {
		return "", fmt.Errorf("response_timeout: initialize response: %w", err)
	}

	// 2. Send initialized notification
	initializedNotif := map[string]interface{}{
		"method": "initialized",
		"params": map[string]interface{}{},
	}
	if err := r.sendJSON(initializedNotif); err != nil {
		return "", fmt.Errorf("response_timeout: failed to send initialized: %w", err)
	}

	// 3. Send thread/start
	threadID := r.nextID()
	threadReq := map[string]interface{}{
		"id":     threadID,
		"method": "thread/start",
		"params": map[string]interface{}{
			"approvalPolicy": r.config.ApprovalPolicy,
			"sandbox":        r.config.ThreadSandbox,
			"cwd":            workspacePath,
		},
	}
	if err := r.sendJSON(threadReq); err != nil {
		return "", fmt.Errorf("response_timeout: failed to send thread/start: %w", err)
	}

	threadResp, err := r.readResponseWithTimeout(ctx, threadID, readTimeout)
	if err != nil {
		return "", fmt.Errorf("response_timeout: thread/start response: %w", err)
	}

	// Extract threadId from result
	threadIDStr := extractNestedString(threadResp, "result", "threadId")
	if threadIDStr == "" {
		threadIDStr = extractNestedString(threadResp, "result", "thread_id")
	}
	if threadIDStr == "" {
		// Try top-level
		if id, ok := threadResp["threadId"].(string); ok {
			threadIDStr = id
		}
	}

	return threadIDStr, nil
}

// StartTurn sends a turn/start request and returns the turn ID.
func (r *Runner) StartTurn(ctx context.Context, threadID, prompt, workspacePath, title string) (string, error) {
	readTimeout := time.Duration(r.config.ReadTimeoutMS) * time.Millisecond

	turnID := r.nextID()
	turnReq := map[string]interface{}{
		"id":     turnID,
		"method": "turn/start",
		"params": map[string]interface{}{
			"threadId": threadID,
			"input": []map[string]interface{}{
				{"type": "text", "text": prompt},
			},
			"cwd":            workspacePath,
			"title":          title,
			"approvalPolicy": r.config.ApprovalPolicy,
			"sandboxPolicy":  r.config.TurnSandboxPolicy,
		},
	}
	if err := r.sendJSON(turnReq); err != nil {
		return "", fmt.Errorf("response_timeout: failed to send turn/start: %w", err)
	}

	turnResp, err := r.readResponseWithTimeout(ctx, turnID, readTimeout)
	if err != nil {
		return "", fmt.Errorf("response_timeout: turn/start response: %w", err)
	}

	turnIDStr := extractNestedString(turnResp, "result", "turnId")
	if turnIDStr == "" {
		turnIDStr = extractNestedString(turnResp, "result", "turn_id")
	}
	if turnIDStr == "" {
		if id, ok := turnResp["turnId"].(string); ok {
			turnIDStr = id
		}
	}

	return turnIDStr, nil
}

// StreamTurn reads turn events until completion, failure, or cancellation.
// It calls onEvent for each event received.
func (r *Runner) StreamTurn(ctx context.Context, onEvent func(RunnerEvent)) (*SessionResult, error) {
	turnTimeout := time.Duration(r.config.TurnTimeoutMS) * time.Millisecond
	turnCtx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	// Use the shared scanner to maintain continuity with handshake reads
	for {
		select {
		case <-turnCtx.Done():
			if turnCtx.Err() == context.DeadlineExceeded {
				return &SessionResult{Error: "turn_timeout", Event: EventTurnFailed}, fmt.Errorf("turn_timeout")
			}
			return &SessionResult{Error: "cancelled", Event: EventTurnCancelled}, turnCtx.Err()
		default:
		}

		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return &SessionResult{Error: fmt.Sprintf("port_exit: %v", err), Event: EventTurnFailed}, err
			}
			// EOF — process exited
			return &SessionResult{Error: "port_exit", Event: EventTurnFailed}, fmt.Errorf("port_exit: codex process exited")
		}

		line := r.scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			onEvent(RunnerEvent{
				Event:     EventMalformed,
				Timestamp: time.Now().UTC(),
				Message:   truncate(line, 200),
			})
			continue
		}

		event := r.processMessage(msg, onEvent)
		if event != nil {
			return event, nil
		}
	}
}

// processMessage handles a single JSON message from stdout.
func (r *Runner) processMessage(msg map[string]interface{}, onEvent func(RunnerEvent)) *SessionResult {
	method, _ := msg["method"].(string)
	now := time.Now().UTC()

	// Extract usage if present
	usage := extractUsage(msg)
	rateLimits := extractRateLimits(msg)

	switch {
	case method == "turn/completed":
		evt := RunnerEvent{
			Event:     EventTurnCompleted,
			Timestamp: now,
			Usage:     usage,
			RateLimits: rateLimits,
			CodexAppServerPID: r.pid,
		}
		onEvent(evt)
		return &SessionResult{Success: true, Event: EventTurnCompleted}

	case method == "turn/failed":
		errMsg := extractErrorMessage(msg)
		evt := RunnerEvent{
			Event:     EventTurnFailed,
			Timestamp: now,
			Message:   errMsg,
			Usage:     usage,
			CodexAppServerPID: r.pid,
		}
		onEvent(evt)
		return &SessionResult{Error: errMsg, Event: EventTurnFailed}

	case method == "turn/cancelled":
		evt := RunnerEvent{
			Event:     EventTurnCancelled,
			Timestamp: now,
			Usage:     usage,
			CodexAppServerPID: r.pid,
		}
		onEvent(evt)
		return &SessionResult{Error: "turn_cancelled", Event: EventTurnCancelled}

	case isApprovalRequest(msg):
		// Auto-approve command/file approvals
		r.autoApprove(msg)
		onEvent(RunnerEvent{
			Event:     EventApprovalAutoApproved,
			Timestamp: now,
			CodexAppServerPID: r.pid,
		})

	case isUserInputRequired(msg):
		// Hard fail on user input required
		evt := RunnerEvent{
			Event:     EventTurnInputRequired,
			Timestamp: now,
			Message:   "user input required - hard fail",
			CodexAppServerPID: r.pid,
		}
		onEvent(evt)
		return &SessionResult{Error: "turn_input_required", Event: EventTurnInputRequired}

	case isUnsupportedToolCall(msg):
		// Return failure result and continue
		r.rejectToolCall(msg)
		onEvent(RunnerEvent{
			Event:     EventUnsupportedToolCall,
			Timestamp: now,
			CodexAppServerPID: r.pid,
		})

	default:
		// Notification or other message
		eventType := EventOtherMessage
		if method != "" && (strings.Contains(method, "notification") || strings.HasPrefix(method, "item/")) {
			eventType = EventNotification
		}
		summary := summarizeMessage(msg)
		onEvent(RunnerEvent{
			Event:     eventType,
			Timestamp: now,
			Message:   summary,
			Usage:     usage,
			RateLimits: rateLimits,
			CodexAppServerPID: r.pid,
		})
	}

	return nil
}

// autoApprove sends an approval response for a command/file approval request.
func (r *Runner) autoApprove(msg map[string]interface{}) {
	id := msg["id"]
	if id == nil {
		return
	}
	resp := map[string]interface{}{
		"id":     id,
		"result": map[string]interface{}{"approved": true},
	}
	if err := r.sendJSON(resp); err != nil {
		r.logger.Warn("failed to send auto-approval", "error", err)
	}
}

// rejectToolCall sends a failure result for an unsupported tool call.
func (r *Runner) rejectToolCall(msg map[string]interface{}) {
	id := msg["id"]
	if id == nil {
		return
	}
	resp := map[string]interface{}{
		"id": id,
		"result": map[string]interface{}{
			"success": false,
			"error":   "unsupported_tool_call",
		},
	}
	if err := r.sendJSON(resp); err != nil {
		r.logger.Warn("failed to send tool call rejection", "error", err)
	}
}

// Stop terminates the codex app-server subprocess.
func (r *Runner) Stop() error {
	if r.stdin != nil {
		r.stdin.Close()
	}
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
		_ = r.cmd.Wait()
	}
	return nil
}

// PID returns the process ID string.
func (r *Runner) PID() string {
	return r.pid
}

// --- Internal helpers ---

func (r *Runner) sendJSON(msg map[string]interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = r.stdin.Write(data)
	return err
}

func (r *Runner) readResponseWithTimeout(ctx context.Context, id int, timeout time.Duration) (map[string]interface{}, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use the shared scanner to avoid data loss between handshake and streaming
	for {
		select {
		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("read timeout waiting for response id=%d", id)
		default:
		}

		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return nil, fmt.Errorf("scanner error: %w", err)
			}
			return nil, fmt.Errorf("EOF waiting for response id=%d", id)
		}

		line := r.scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // Skip non-JSON lines
		}

		// Check if this is the response we're waiting for
		if msgID, ok := msg["id"]; ok {
			var msgIDInt int
			switch v := msgID.(type) {
			case float64:
				msgIDInt = int(v)
			case int:
				msgIDInt = v
			}
			if msgIDInt == id {
				if errObj, ok := msg["error"]; ok {
					return nil, fmt.Errorf("response_error: %v", errObj)
				}
				return msg, nil
			}
		}
	}
}

func isApprovalRequest(msg map[string]interface{}) bool {
	method, _ := msg["method"].(string)
	return strings.Contains(method, "approval") ||
		strings.Contains(method, "item/command/approval") ||
		strings.Contains(method, "item/file/approval")
}

func isUserInputRequired(msg map[string]interface{}) bool {
	method, _ := msg["method"].(string)
	if strings.Contains(method, "requestUserInput") || strings.Contains(method, "user_input") {
		return true
	}
	// Check for turn methods with input required flag
	if params, ok := msg["params"].(map[string]interface{}); ok {
		if inputRequired, ok := params["inputRequired"].(bool); ok && inputRequired {
			return true
		}
	}
	return false
}

func isUnsupportedToolCall(msg map[string]interface{}) bool {
	method, _ := msg["method"].(string)
	return method == "item/tool/call"
}

func extractNestedString(msg map[string]interface{}, keys ...string) string {
	current := msg
	for i, key := range keys {
		if i == len(keys)-1 {
			if v, ok := current[key].(string); ok {
				return v
			}
			return ""
		}
		if next, ok := current[key].(map[string]interface{}); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

func extractErrorMessage(msg map[string]interface{}) string {
	if params, ok := msg["params"].(map[string]interface{}); ok {
		if errMsg, ok := params["error"].(string); ok {
			return errMsg
		}
		if errMsg, ok := params["message"].(string); ok {
			return errMsg
		}
	}
	return "turn_failed"
}

func extractUsage(msg map[string]interface{}) *TokenUsage {
	// Try multiple payload shapes for token counts
	for _, key := range []string{"params", "result"} {
		if params, ok := msg[key].(map[string]interface{}); ok {
			usage := findUsageInMap(params)
			if usage != nil {
				return usage
			}
		}
	}
	return nil
}

func findUsageInMap(m map[string]interface{}) *TokenUsage {
	// Look for total_token_usage, tokenUsage, usage
	for _, key := range []string{"total_token_usage", "tokenUsage", "usage"} {
		if u, ok := m[key].(map[string]interface{}); ok {
			return parseUsageMap(u)
		}
	}
	return nil
}

func parseUsageMap(m map[string]interface{}) *TokenUsage {
	usage := &TokenUsage{}
	for _, key := range []string{"input_tokens", "inputTokens", "prompt_tokens"} {
		if v, ok := m[key].(float64); ok {
			usage.InputTokens = int(v)
			break
		}
	}
	for _, key := range []string{"output_tokens", "outputTokens", "completion_tokens"} {
		if v, ok := m[key].(float64); ok {
			usage.OutputTokens = int(v)
			break
		}
	}
	for _, key := range []string{"total_tokens", "totalTokens"} {
		if v, ok := m[key].(float64); ok {
			usage.TotalTokens = int(v)
			break
		}
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return usage
}

func extractRateLimits(msg map[string]interface{}) map[string]interface{} {
	if params, ok := msg["params"].(map[string]interface{}); ok {
		if rl, ok := params["rateLimits"].(map[string]interface{}); ok {
			return rl
		}
		if rl, ok := params["rate_limits"].(map[string]interface{}); ok {
			return rl
		}
	}
	return nil
}

func summarizeMessage(msg map[string]interface{}) string {
	method, _ := msg["method"].(string)
	if params, ok := msg["params"].(map[string]interface{}); ok {
		if message, ok := params["message"].(string); ok {
			return truncate(message, 200)
		}
		if text, ok := params["text"].(string); ok {
			return truncate(text, 200)
		}
	}
	return method
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
