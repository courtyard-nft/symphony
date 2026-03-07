# Symphony

A Go implementation of the [OpenAI Symphony specification](https://github.com/openai/symphony) — a long-running daemon that orchestrates coding agents to work on Linear issues.

Symphony continuously polls Linear for issues, creates isolated per-issue workspaces, and runs `codex app-server` sessions against those issues.

## Features

- **Poll-driven orchestration** — Continuously monitors Linear for eligible issues
- **Per-issue workspace isolation** — Each issue gets its own workspace directory
- **Codex app-server integration** — JSON-RPC protocol handshake and streaming turn processing
- **Retry with exponential backoff** — Automatic retry on failures with configurable backoff
- **Dynamic config reload** — Edit `WORKFLOW.md` and changes apply without restart
- **Concurrency control** — Global and per-state agent concurrency limits
- **Lifecycle hooks** — Shell scripts run at workspace create, before/after run, and before remove
- **HTTP dashboard** — Optional web UI and JSON API for runtime observability
- **Structured logging** — `log/slog` with issue/session context fields

## Quick Start

### Prerequisites

- Go 1.22+
- A Linear API key
- Codex CLI installed (`codex app-server`)

### Build

```bash
go build -o symphony ./cmd/symphony
```

### Configure

Create a `WORKFLOW.md` file in your working directory. See the included [WORKFLOW.md](./WORKFLOW.md) for a complete example.

Minimum configuration:

```yaml
---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: your-project-slug
---
Your prompt template here. Use {{ issue.identifier }} and {{ issue.title }}.
```

### Run

```bash
# Set your Linear API key
export LINEAR_API_KEY=lin_api_xxxxx

# Run with default WORKFLOW.md in current directory
./symphony

# Or specify a custom workflow path
./symphony --workflow /path/to/WORKFLOW.md

# Enable the HTTP dashboard
./symphony --port 8080
```

## Configuration

Configuration lives in the YAML front matter of `WORKFLOW.md`. All fields support `$VAR` environment variable indirection.

### Config Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tracker.kind` | string | *required* | Tracker type (`linear`) |
| `tracker.endpoint` | string | `https://api.linear.app/graphql` | API endpoint |
| `tracker.api_key` | string | *required* | API key or `$VAR` |
| `tracker.project_slug` | string | *required* | Linear project slug |
| `tracker.active_states` | list | `[Todo, In Progress]` | States eligible for dispatch |
| `tracker.terminal_states` | list | `[Closed, Cancelled, ...]` | Terminal states |
| `polling.interval_ms` | int | `30000` | Poll interval in ms |
| `workspace.root` | path | `<tmpdir>/symphony_workspaces` | Workspace root directory |
| `hooks.after_create` | string | *null* | Script to run after workspace creation |
| `hooks.before_run` | string | *null* | Script to run before each agent run |
| `hooks.after_run` | string | *null* | Script to run after each agent run |
| `hooks.before_remove` | string | *null* | Script to run before workspace removal |
| `hooks.timeout_ms` | int | `60000` | Hook execution timeout |
| `agent.max_concurrent_agents` | int | `10` | Global concurrency limit |
| `agent.max_turns` | int | `20` | Max turns per worker session |
| `agent.max_retry_backoff_ms` | int | `300000` | Max retry backoff (5 min) |
| `agent.max_concurrent_agents_by_state` | map | `{}` | Per-state concurrency limits |
| `codex.command` | string | `codex app-server` | Codex launch command |
| `codex.turn_timeout_ms` | int | `3600000` | Turn timeout (1 hour) |
| `codex.read_timeout_ms` | int | `5000` | Read timeout (5 sec) |
| `codex.stall_timeout_ms` | int | `300000` | Stall timeout (5 min) |
| `server.port` | int | *disabled* | HTTP server port |

### Dynamic Reload

Symphony watches `WORKFLOW.md` for changes using `fsnotify`. When changes are detected:

- Config is re-parsed and re-applied
- Polling interval and concurrency limits update immediately
- Prompt template changes apply to future agent runs
- Invalid reloads keep the last known good configuration

### Prompt Templates

The prompt body (below the YAML front matter) is a [Liquid](https://shopify.github.io/liquid/) template. Available variables:

- `issue` — The full issue object (id, identifier, title, description, state, priority, labels, blocked_by, etc.)
- `attempt` — Retry attempt number (nil for first run)

## Architecture

```
cmd/symphony/          Main entrypoint, CLI flags
internal/config/       Typed config with defaults and $VAR resolution
internal/workflow/     WORKFLOW.md parser and Liquid template renderer
internal/linear/       Linear GraphQL client with pagination
internal/orchestrator/ Poll loop, state machine, retry/backoff, reconciliation
internal/workspace/    Workspace creation, sanitization, hooks, safety checks
internal/runner/       Codex app-server JSON-RPC client
internal/server/       Optional HTTP dashboard and JSON API
```

### Orchestration State Machine

Issues move through internal orchestration states:

1. **Unclaimed** — Issue is eligible for dispatch
2. **Claimed** — Orchestrator has reserved the issue
3. **Running** — Agent worker is actively processing
4. **RetryQueued** — Waiting for retry timer
5. **Released** — Claim removed (terminal, non-active, or completed)

### Retry Strategy

- **Normal exit**: Short 1-second continuation retry to re-check if issue needs more work
- **Failure exit**: Exponential backoff `min(10000 * 2^(attempt-1), max_retry_backoff_ms)`

## HTTP API

When enabled via `--port` or `server.port`:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | HTML dashboard |
| `/status` | GET | JSON runtime snapshot |
| `/api/v1/state` | GET | Full state summary |
| `/api/v1/<identifier>` | GET | Issue-specific details |
| `/api/v1/refresh` | POST | Trigger immediate poll |

## Trust and Safety Posture

This implementation uses a **high-trust configuration**:

- **Auto-approve**: Command execution and file-change approvals are automatically approved
- **Hard-fail on user input**: If the agent requests user input, the run fails immediately
- **Workspace isolation**: All agent execution is confined to per-issue workspace directories
- **Path prefix safety**: Workspace paths are validated to stay under the configured root
- **Identifier sanitization**: Only `[A-Za-z0-9._-]` characters allowed in workspace directory names
- **Secret handling**: API keys use `$VAR` indirection; secrets are never logged

### Recommendations for Production

- Run Symphony under a dedicated OS user with restricted permissions
- Mount the workspace root on a dedicated volume
- Use network policies to restrict agent outbound access
- Monitor the HTTP dashboard for stalled sessions
- Set appropriate concurrency limits based on available resources

## Development

```bash
# Run tests
go test ./...

# Build
go build ./...

# Run with verbose logging
./symphony --workflow WORKFLOW.md --port 8080
```

## License

See [LICENSE](./LICENSE) for details.
