---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: courtyard-eng
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
    - Closed
    - Cancelled
    - Canceled
    - Duplicate

polling:
  interval_ms: 30000

workspace:
  root: /tmp/symphony_workspaces

hooks:
  after_create: |
    echo "Workspace created for $SYMPHONY_ISSUE_IDENTIFIER"
  before_run: |
    echo "Starting agent run"
  after_run: |
    echo "Agent run completed"
  timeout_ms: 60000

agent:
  max_concurrent_agents: 5
  max_turns: 20
  max_retry_backoff_ms: 300000
  max_concurrent_agents_by_state:
    todo: 2
    in progress: 5

codex:
  command: codex app-server
  approval_policy: never
  thread_sandbox: workspace-write
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000

server:
  port: 8080
---

You are an expert software engineer working on the Courtyard codebase.

## Task

You are assigned to work on issue **{{ issue.identifier }}**: *{{ issue.title }}*

{% if issue.description != blank %}
### Description
{{ issue.description }}
{% endif %}

### Issue Details
- **State**: {{ issue.state }}
- **Priority**: {{ issue.priority }}
{% if issue.url != blank %}- **URL**: {{ issue.url }}{% endif %}
{% if issue.branch_name != blank %}- **Branch**: {{ issue.branch_name }}{% endif %}

{% if issue.labels.size > 0 %}
### Labels
{% for label in issue.labels %}- {{ label }}
{% endfor %}
{% endif %}

{% if issue.blocked_by.size > 0 %}
### Blockers
This issue is blocked by:
{% for blocker in issue.blocked_by %}- {{ blocker.identifier }} ({{ blocker.state }})
{% endfor %}
{% endif %}

{% if attempt %}
### Retry Context
This is attempt **{{ attempt }}**. Review previous work in this workspace and continue from where the last attempt left off.
{% endif %}

## Instructions

1. Read the issue description carefully
2. Understand the current state of the codebase
3. Implement the required changes following the project's coding standards
4. Write or update tests as needed
5. Ensure all tests pass
6. Create a pull request with a clear description
7. Move the issue to "Human Review" state when the PR is ready

## Constraints

- Do not modify unrelated code
- Follow existing patterns and conventions
- Ensure backward compatibility
- Write clear commit messages
- Keep changes focused and minimal
