"""
OpenAI Responses API → Anthropic proxy
Translates POST /v1/responses to Anthropic Messages API with streaming.
"""
import os, json, time, uuid, httpx
from fastapi import FastAPI, Request, HTTPException
from fastapi.responses import StreamingResponse, JSONResponse
import uvicorn

app = FastAPI()
ANTHROPIC_API_KEY = os.environ["ANTHROPIC_API_KEY"]
ANTHROPIC_BASE = "https://api.anthropic.com/v1/messages"
MODEL_MAP = {
    "claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
    "claude-opus-4": "claude-opus-4-20250514",
    "claude-haiku-4": "claude-haiku-4-20250514",
    "claude-sonnet-4": "claude-sonnet-4-20250514",
}

def map_model(model: str) -> str:
    return MODEL_MAP.get(model, model)

def responses_input_to_messages(input_data):
    """Convert Responses API input to Anthropic messages format."""
    if isinstance(input_data, str):
        return [{"role": "user", "content": input_data}]
    if isinstance(input_data, list):
        messages = []
        for item in input_data:
            if isinstance(item, dict):
                role = item.get("role", "user")
                content = item.get("content", "")
                if isinstance(content, list):
                    parts = []
                    for c in content:
                        if isinstance(c, dict) and c.get("type") == "input_text":
                            parts.append({"type": "text", "text": c.get("text", "")})
                        elif isinstance(c, dict) and c.get("type") == "text":
                            parts.append({"type": "text", "text": c.get("text", "")})
                    content = parts if parts else ""
                if role in ("user", "assistant"):
                    messages.append({"role": role, "content": content})
                elif role == "developer":
                    # developer messages become system (prepend to first user or use system param)
                    pass
        return messages
    return []

def extract_system(input_data):
    """Extract developer/system messages from input."""
    if not isinstance(input_data, list):
        return None
    parts = []
    for item in input_data:
        if isinstance(item, dict) and item.get("role") == "developer":
            content = item.get("content", "")
            if isinstance(content, list):
                for c in content:
                    if isinstance(c, dict):
                        parts.append(c.get("text", "") or c.get("input_text", ""))
            elif isinstance(content, str):
                parts.append(content)
    return "\n\n".join(parts) if parts else None

def build_response_object(response_id: str, model: str, content_text: str, input_tokens: int, output_tokens: int, finish_reason: str = "stop"):
    return {
        "id": response_id,
        "object": "response",
        "created_at": int(time.time()),
        "model": model,
        "output": [
            {
                "type": "message",
                "id": f"msg_{uuid.uuid4().hex[:24]}",
                "role": "assistant",
                "status": "completed",
                "content": [
                    {"type": "output_text", "text": content_text, "annotations": []}
                ],
            }
        ],
        "usage": {
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
            "total_tokens": input_tokens + output_tokens,
        },
        "status": "completed",
        "error": None,
        "incomplete_details": None,
        "instructions": None,
        "metadata": {},
        "parallel_tool_calls": True,
        "temperature": 1.0,
        "tool_choice": "auto",
        "tools": [],
        "top_p": 1.0,
    }

@app.post("/v1/responses")
@app.post("/responses")
async def create_response(request: Request):
    try:
        body = await request.json()
    except Exception:
        raise HTTPException(400, "invalid json")

    model_requested = body.get("model", "claude-sonnet-4-5")
    model = map_model(model_requested)
    input_data = body.get("input", [])
    stream = body.get("stream", False)
    max_tokens = body.get("max_output_tokens") or body.get("max_tokens") or 8192
    instructions = body.get("instructions")

    messages = responses_input_to_messages(input_data)
    system = extract_system(input_data) or instructions

    if not messages:
        messages = [{"role": "user", "content": "Hello"}]

    # Ensure messages alternate properly and start with user
    cleaned = []
    for m in messages:
        if m.get("role") in ("user", "assistant"):
            cleaned.append(m)
    if not cleaned or cleaned[0]["role"] != "user":
        cleaned.insert(0, {"role": "user", "content": "(continue)"})
    messages = cleaned

    anthropic_body = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": messages,
        "stream": stream,
    }
    if system:
        anthropic_body["system"] = system

    headers = {
        "x-api-key": ANTHROPIC_API_KEY,
        "anthropic-version": "2023-06-01",
        "content-type": "application/json",
    }

    response_id = f"resp_{uuid.uuid4().hex}"

    if not stream:
        async with httpx.AsyncClient(timeout=120) as client:
            r = await client.post(ANTHROPIC_BASE, json=anthropic_body, headers=headers)
            if r.status_code != 200:
                raise HTTPException(r.status_code, r.text)
            data = r.json()
            text = "".join(
                b.get("text", "") for b in data.get("content", []) if b.get("type") == "text"
            )
            usage = data.get("usage", {})
            return JSONResponse(build_response_object(
                response_id, model_requested, text,
                usage.get("input_tokens", 0), usage.get("output_tokens", 0)
            ))

    # Streaming: translate Anthropic SSE → OpenAI Responses SSE
    async def event_stream():
        async with httpx.AsyncClient(timeout=300) as client:
            async with client.stream("POST", ANTHROPIC_BASE, json=anthropic_body, headers=headers) as r:
                if r.status_code != 200:
                    error_body = await r.aread()
                    yield f"data: {json.dumps({'type':'error','error':{'message': error_body.decode()}})}\n\n"
                    return

                # Send response.created
                yield f"event: response.created\ndata: {json.dumps({'type':'response.created','response':{'id':response_id,'object':'response','status':'in_progress','model':model_requested,'output':[],'usage':None}})}\n\n"

                output_item_id = f"msg_{uuid.uuid4().hex[:24]}"
                # Send output_item.added
                yield f"event: response.output_item.added\ndata: {json.dumps({'type':'response.output_item.added','output_index':0,'item':{'type':'message','id':output_item_id,'role':'assistant','status':'in_progress','content':[]}})}\n\n"
                # Send content_part.added
                yield f"event: response.content_part.added\ndata: {json.dumps({'type':'response.content_part.added','item_id':output_item_id,'output_index':0,'content_index':0,'part':{'type':'output_text','text':'','annotations':[]}})}\n\n"

                full_text = ""
                input_tokens = 0
                output_tokens = 0

                async for line in r.aiter_lines():
                    if not line.startswith("data:"):
                        continue
                    raw = line[5:].strip()
                    if not raw or raw == "[DONE]":
                        continue
                    try:
                        evt = json.loads(raw)
                    except Exception:
                        continue

                    etype = evt.get("type", "")

                    if etype == "content_block_delta":
                        delta = evt.get("delta", {})
                        if delta.get("type") == "text_delta":
                            chunk = delta.get("text", "")
                            full_text += chunk
                            yield f"event: response.output_text.delta\ndata: {json.dumps({'type':'response.output_text.delta','item_id':output_item_id,'output_index':0,'content_index':0,'delta':chunk})}\n\n"

                    elif etype == "message_delta":
                        usage = evt.get("usage", {})
                        output_tokens = usage.get("output_tokens", output_tokens)

                    elif etype == "message_start":
                        usage = evt.get("message", {}).get("usage", {})
                        input_tokens = usage.get("input_tokens", 0)

                    elif etype == "message_stop":
                        pass

                # Send completion events
                yield f"event: response.output_text.done\ndata: {json.dumps({'type':'response.output_text.done','item_id':output_item_id,'output_index':0,'content_index':0,'text':full_text})}\n\n"
                yield f"event: response.output_item.done\ndata: {json.dumps({'type':'response.output_item.done','output_index':0,'item':{'type':'message','id':output_item_id,'role':'assistant','status':'completed','content':[{'type':'output_text','text':full_text,'annotations':[]}]}})}\n\n"

                final = build_response_object(response_id, model_requested, full_text, input_tokens, output_tokens)
                final["status"] = "completed"
                yield f"event: response.completed\ndata: {json.dumps({'type':'response.completed','response':final})}\n\n"

    return StreamingResponse(event_stream(), media_type="text/event-stream")

@app.get("/v1/models")
async def list_models():
    return {"object": "list", "data": [
        {"id": "claude-sonnet-4-5", "object": "model"},
        {"id": "claude-opus-4", "object": "model"},
    ]}

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=4001, log_level="warning")
