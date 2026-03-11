# Anthropic Responses API Proxy

A lightweight FastAPI proxy that translates OpenAI's Responses API (`/v1/responses`) to Anthropic's Messages API. This lets Symphony + codex app-server work natively with Anthropic models — no OpenAI API key needed.

## Usage

```bash
pip install fastapi uvicorn httpx
ANTHROPIC_API_KEY=your-key python anthropic-proxy.py
```

Proxy runs on `http://localhost:4001`.

## Symphony Integration

In `WORKFLOW.md`, set:

```yaml
codex:
  command: env OPENAI_API_KEY=proxy OPENAI_BASE_URL=http://localhost:4001 codex app-server
```

## Supported Models

| Codex model name | Anthropic model |
|---|---|
| `claude-sonnet-4-5` | `claude-sonnet-4-5-20250929` |
| `claude-sonnet-4` | `claude-sonnet-4-20250514` |
| `claude-opus-4` | `claude-opus-4-20250514` |

Supports both streaming and non-streaming responses.
