# Symphony Supervision Runbook

_For Courty — how to monitor, intervene, and recover Symphony._

## Quick Status Check

```bash
# Is Symphony running?
pgrep -a symphony

# Dashboard (if --port 8080 set)
curl -s http://localhost:8080/status | python3 -m json.tool

# Full state (active workspaces, turn counts)
curl -s http://localhost:8080/api/v1/state | python3 -m json.tool

# Tail logs
tail -f ~/symphony/logs/symphony.log
tail -f ~/symphony/logs/symphony-error.log
```

## Start / Stop / Restart (launchd)

```bash
# Load and start
launchctl load ~/Library/LaunchAgents/io.courtyard.symphony.plist

# Stop (and disable restart)
launchctl unload ~/Library/LaunchAgents/io.courtyard.symphony.plist

# Restart (stop then start)
launchctl unload ~/Library/LaunchAgents/io.courtyard.symphony.plist
launchctl load ~/Library/LaunchAgents/io.courtyard.symphony.plist

# Force a Linear poll immediately
curl -s -X POST http://localhost:8080/api/v1/refresh
```

> **Note:** `KeepAlive.Crashed=true` means launchd auto-restarts Symphony on crash.
> You don't need to manually restart it unless you want to apply config changes.

## Updating LINEAR_API_KEY

The plist has `REPLACE_WITH_LINEAR_KEY` as a placeholder. To inject the real key:

```bash
LINEAR_KEY=$(cat ~/linear.txt | tr -d '[:space:]')
/usr/libexec/PlistBuddy -c "Set :EnvironmentVariables:LINEAR_API_KEY $LINEAR_KEY" \
  ~/Library/LaunchAgents/io.courtyard.symphony.plist
# Then reload
launchctl unload ~/Library/LaunchAgents/io.courtyard.symphony.plist
launchctl load ~/Library/LaunchAgents/io.courtyard.symphony.plist
```

## When to Intervene

### Agent stuck at high turn count

Check the dashboard. If an issue shows turn count > 15 (out of 20 max) and hasn't produced a PR:

```bash
# Get issue details
curl -s http://localhost:8080/api/v1/<ENG-XXXX> | python3 -m json.tool

# If stuck: kill the workspace to release the claim
# Symphony will retry with exponential backoff
```

Look at the logs to understand why it's stuck. Common causes:
- Test failures blocking the agent
- Missing environment (yarn not installed, wrong branch)
- Agent hitting `request_user_input` (hard-fails by design)

### Agent repeatedly failing (RetryQueued)

Check logs for the error pattern. Common fixes:
1. Workspace env issue → update `hooks.after_create` in WORKFLOW.md
2. GitHub auth → check SSH key is configured (`ssh -T git@github.com`)
3. Codex not found → `which codex` — reinstall if missing

### Symphony itself crashed / not running

launchd should auto-restart it. If it's not coming back:

```bash
# Check launchd status
launchctl list io.courtyard.symphony

# Check exit code
# If non-zero: look at symphony-error.log for cause
tail -50 ~/symphony/logs/symphony-error.log
```

### Config reload needed

Just edit `~/symphony/WORKFLOW.md` — Symphony watches it with fsnotify and reloads automatically.

## Per-Issue Intervention

```bash
# See what's happening with a specific issue
curl -s http://localhost:8080/api/v1/ENG-1234 | python3 -m json.tool

# Force a refresh (picks up newly-eligible issues)
curl -s -X POST http://localhost:8080/api/v1/refresh

# Kill a stuck workspace (releases claim, Symphony will retry)
rm -rf /tmp/symphony_workspaces/ENG-1234-*
```

## Log Format

Symphony uses `log/slog` structured JSON logs. Key fields:
- `level` — INFO, WARN, ERROR
- `msg` — event description
- `issue` — Linear issue identifier (e.g. ENG-5106)
- `session` — workspace session ID
- `turn` — current turn count
- `status` — orchestration state

```bash
# Filter by issue
grep '"issue":"ENG-5106"' ~/symphony/logs/symphony.log | tail -20

# See all errors
grep '"level":"ERROR"' ~/symphony/logs/symphony.log | tail -20
```

## WORKFLOW.md Templates

Symphony auto-selects a WORKFLOW.md template based on issue labels (when configured):
- `frontend.md` — React/TypeScript/Next.js work
- `backend.md` — Go/API work  
- `infra.md` — Infrastructure/CI work

Current default: single `WORKFLOW.md` in `~/symphony/` applies to all issues.

## Escalation Triggers

**Take over manually when:**
- Agent produced a PR but CI is failing for >2 hours
- Agent left a comment saying it needs user input
- Turn limit hit (20 turns) and work is ~50% done — finish it yourself
- The issue is time-sensitive (production incident, etc.)

**Leave it alone when:**
- CI is running normally (green or recently started)
- Agent just pushed and is waiting for CI feedback
- It's been <1 hour since last activity

## Health Summary

To get a quick status summary in one command:

```bash
echo "=== Symphony Process ===" && pgrep -a symphony && \
echo "=== Active Workspaces ===" && ls /tmp/symphony_workspaces/ 2>/dev/null || echo "none" && \
echo "=== Dashboard ===" && curl -s http://localhost:8080/status 2>/dev/null | python3 -m json.tool || echo "dashboard not available"
```
