---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: 1bd6029af9a0
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
    cd "$SYMPHONY_WORKSPACE_PATH"
    git clone https://github.com/courtyard-nft/courtyard-frontend.git 2>&1 || echo "already cloned"
    
    # Install deps so quality tools (prettier, eslint, tsc) actually work
    cd courtyard-frontend
    which yarn 2>/dev/null || npm install -g yarn 2>&1 | tail -2
    yarn install --frozen-lockfile 2>&1 | tail -5 || echo "yarn install failed"
    
    # Set up feature branch
    # If issue is blocked by another issue, stack on that blocker's branch.
    # Otherwise, branch from main.
    ISSUE_ID=$(basename "$(dirname "$PWD")")
    FEATURE_BRANCH="devin/${ISSUE_ID}-impl"
    
    git fetch origin
    
    # Check if a blocker branch env var was injected by Symphony
    if [ -n "$SYMPHONY_BLOCKER_BRANCH" ]; then
      BASE_BRANCH="$SYMPHONY_BLOCKER_BRANCH"
      git checkout "$BASE_BRANCH" 2>/dev/null || git checkout -b "$BASE_BRANCH" "origin/$BASE_BRANCH"
    else
      BASE_BRANCH="main"
      git checkout main
      git pull origin main
    fi
    
    if git show-ref --quiet "refs/heads/$FEATURE_BRANCH"; then
      git checkout "$FEATURE_BRANCH"
      echo "Resumed existing branch $FEATURE_BRANCH"
    else
      git checkout -b "$FEATURE_BRANCH"
      echo "Created branch $FEATURE_BRANCH from $BASE_BRANCH"
    fi
    
    cd ..
    echo "Workspace ready for $ISSUE_ID on branch $FEATURE_BRANCH (base: $BASE_BRANCH)"
  before_run: |
    echo "Starting agent run for $SYMPHONY_ISSUE_IDENTIFIER"
  after_run: |
    echo "Agent run completed for $SYMPHONY_ISSUE_IDENTIFIER"
  timeout_ms: 300000

agent:
  max_concurrent_agents: 3
  max_turns: 50
  max_retry_backoff_ms: 300000
  max_concurrent_agents_by_state:
    todo: 2
    in progress: 3

codex:
  command: env OPENAI_API_KEY=proxy OPENAI_BASE_URL=http://localhost:4001 codex app-server
  approval_policy: never
  thread_sandbox: workspace-write
  turn_sandbox_policy:
    type: workspaceWrite
    network_access: true
  turn_timeout_ms: 3600000
  read_timeout_ms: 10000
  stall_timeout_ms: 300000

server:
  port: 8080
---

You are an expert TypeScript/React engineer working on the Courtyard NFT frontend.

The courtyard-frontend repo is cloned at `./courtyard-frontend` with all deps installed.

## Task: {{ issue.identifier }} — {{ issue.title }}

{{ issue.description }}

## MANDATORY: Git branching — READ THIS CAREFULLY

{% if issue.blocked_by.size > 0 %}
**This issue is blocked by {{ issue.blocked_by[0].identifier }}. Stack your branch on its branch:**

```bash
cd ./courtyard-frontend
git fetch origin
git checkout devin/{{ issue.blocked_by[0].identifier }}-impl
git checkout -b devin/{{ issue.identifier }}-impl
```

**Open PR targeting `devin/{{ issue.blocked_by[0].identifier }}-impl`:**
```bash
gh pr create \
  --title "[{{ issue.identifier }}] {{ issue.title }}" \
  --base devin/{{ issue.blocked_by[0].identifier }}-impl \
  --body "Stacks on devin/{{ issue.blocked_by[0].identifier }}-impl\n\nLinear: {{ issue.url }}"
```

{% else %}
**Branch from `main`. Target `main` for your PR.**

```bash
cd ./courtyard-frontend
git fetch origin
git checkout main
git pull origin main
git checkout -b devin/{{ issue.identifier }}-impl
```

**Open PR targeting `main`:**
```bash
gh pr create \
  --title "[{{ issue.identifier }}] {{ issue.title }}" \
  --base main \
  --body "Linear: {{ issue.url }}"
```
{% endif %}

## Codebase facts

**Next.js App Router project.** Routes: `src/app/`, NOT `src/pages/`.

**UI library:** `import { Box, Typography, CircularProgress } from '@repo/misprint';`
**Always sort named imports alphabetically.**

**Asset page link:** `href={'/asset/' + asset.proof_of_integrity}` — NEVER use `.id`

**Image component:** `import { Image } from '@/Components/Common/Nextjs/Image';`
**Link component:** `import { Link } from '@/Components/Common/Nextjs/Link';`

**API routes pattern** (for server-side API calls):
```typescript
// packages/courtyard-website-v2/src/app/api/<feature>/route.ts
import { NextRequest, NextResponse } from 'next/server';

export async function POST(req: NextRequest) {
  // Access secrets server-side only — never expose to client
  const falKey = process.env.FAL_KEY;
  const geminiKey = process.env.GEMINI_API_KEY;
  return NextResponse.json({ result });
}
```

**API keys available as env vars at runtime (server-side only):**
- `FAL_KEY` — fal.ai (ElevenLabs TTS, music generation)
- `GEMINI_API_KEY` — Google Gemini

## Quality checks — run BEFORE committing

```bash
cd ./courtyard-frontend
yarn workspace courtyard-website-v2 lint:fix
yarn workspace courtyard-website-v2 format
git add -A
```

## Step-by-step instructions

1. **Set up the branch** (see MANDATORY Git section above)
2. **Verify you're on the right branch:** `git branch` must show `devin/{{ issue.identifier }}-impl`
3. **Implement the feature** per the task description
4. **Run quality checks** (lint:fix + format), then `git add -A`
5. **Commit:** `git commit -m "[{{ issue.identifier }}] {{ issue.title }}"`
6. **Push:** `git push origin devin/{{ issue.identifier }}-impl`
7. **Open PR** (see MANDATORY Git section above for the correct base branch)

Start immediately. Do not call update_plan.
