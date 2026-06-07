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

## Codebase facts (frontend)

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
  const result = ...;
  return NextResponse.json({ result });
}
```

**API keys available as env vars at runtime (server-side only):**
- `FAL_KEY` — fal.ai
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
