#!/usr/bin/env python3
"""Mock upstream for the file-mode e2e test.

Speaks four upstream protocols on one port:
  POST .../chat/completions                 OpenAI-compatible (Chat Completions)
  POST .../responses                        Codex (Responses SSE)
  POST .../v1/messages                      Claude Messages
  POST .../models/<m>:generateContent       Gemini (and :streamGenerateContent)

Behaviour is chosen by the model name suffix:
  *-reply   call submit_reply with "ECHO:" + the text the read_message results carry
  *-mixed   call another tool (list_dir) and submit_reply in the same step
  *-reread  call read_message itself while the request holds only one read result, then reply
  *-text    answer with plain text
  *-reject  HTTP 400 when the request carries a read_message result, else plain text

Every request is appended to $MOCK_LOG as one JSON line {path, body}.
"""
import base64
import json
import os
import re
import sys
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = os.environ.get("MOCK_LOG", "/tmp/mock-upstream.jsonl")
TOOL = "submit_reply"


def b64(s):
    try:
        return base64.b64decode(s).decode("utf-8", "replace")
    except Exception:
        return ""


def walk(x):
    if isinstance(x, dict):
        yield x
        for v in x.values():
            yield from walk(v)
    elif isinstance(x, list):
        for v in x:
            yield from walk(v)


READ = "read_message"


def _text(x):
    """The text of a tool result in any upstream shape (string, parts, {result: ...})."""
    if isinstance(x, str):
        try:
            j = json.loads(x)
            if isinstance(j, (dict, list, str)):   # (Chat -> Gemini: CPA puts the tool content in as its raw JSON, a quoted string)
                return _text(j) if not isinstance(j, str) else j
        except Exception:
            pass
        return x
    if isinstance(x, list):
        return "".join(_text(v) for v in x)
    if isinstance(x, dict):
        for k in ("text", "output", "result", "content"):
            if k in x:
                return _text(x[k])
    return ""


def read_results(body):
    """The text of every result of a read_message call, in any upstream format."""
    ids = set()
    for d in walk(body):
        if d.get("name") == READ or (isinstance(d.get("function"), dict) and d["function"].get("name") == READ):
            for k in ("id", "call_id"):
                if d.get(k):
                    ids.add(d[k])
    out = []
    for d in walk(body):
        if d.get("role") == "tool" and d.get("tool_call_id") in ids:
            out.append(_text(d.get("content")))
        elif d.get("type") == "function_call_output" and d.get("call_id") in ids:
            out.append(_text(d.get("output")))
        elif d.get("type") == "tool_result" and d.get("tool_use_id") in ids:
            out.append(_text(d.get("content")))
        for k in ("functionResponse", "function_response"):
            if isinstance(d.get(k), dict) and d[k].get("name") == READ:
                out.append(_text(d[k].get("response")))
    return out


attachments = read_results  # (the name the checks used when the text went as files)


def has_tool(body):
    return TOOL in json.dumps(body)


def reply_text(body):
    files = read_results(body)
    if files:
        return "ECHO:" + files[-1]
    return "INLINE-ECHO"


def scenario(model):
    for s in ("reply", "mixed", "reread", "text", "reject"):
        if model.endswith("-" + s):
            return s
    return "text"


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def send_json(self, code, obj):
        raw = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def start_sse(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

    def sse(self, data, event=None):
        s = ""
        if event:
            s += "event: %s\n" % event
        s += "data: %s\n\n" % (data if isinstance(data, str) else json.dumps(data))
        self.wfile.write(s.encode())
        self.wfile.flush()

    def do_GET(self):
        self.send_json(200, {"object": "list", "data": []})

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n)
        try:
            body = json.loads(raw or b"{}")
        except Exception:
            body = {}
        with open(LOG, "a") as f:
            f.write(json.dumps({"path": self.path, "body": body}) + "\n")
        path = self.path.split("?")[0]
        if path.endswith("/chat/completions"):
            return self.openai(body)
        if path.endswith("/responses"):
            return self.codex(body)
        if path.endswith("/messages"):
            return self.claude(body)
        m = re.search(r"/models/([^/:]+):(generateContent|streamGenerateContent)", path)
        if m:
            return self.gemini(body, m.group(1), m.group(2) == "streamGenerateContent")
        self.send_json(404, {"error": {"message": "unknown path " + path}})

    def reject_if_files(self, body, model):
        if scenario(model) == "reject" and attachments(body):
            self.send_json(400, {"error": {"message": "tool results are not supported by this model", "type": "invalid_request_error", "code": "unsupported_file"}})
            return True
        return False

    def plan(self, body, model):
        """[(kind, name, payload)]: kind is 'text' or 'call'."""
        sc = scenario(model)
        if sc == "reply" and has_tool(body):
            return [("call", TOOL, {"content": reply_text(body)})]
        if sc == "reread" and has_tool(body):
            if len(read_results(body)) < 2:
                return [("call", READ, {})]
            return [("call", TOOL, {"content": reply_text(body)})]
        if sc == "mixed" and has_tool(body):
            return [("call", "list_dir", {"path": "."}), ("call", TOOL, {"content": reply_text(body)})]
        return [("text", None, "plain text answer")]

    # ---------- OpenAI chat ----------
    def openai(self, body):
        model = body.get("model", "")
        if self.reject_if_files(body, model):
            return
        plan = self.plan(body, model)
        cid = "chatcmpl-" + uuid.uuid4().hex[:8]
        if not body.get("stream"):
            msg = {"role": "assistant", "content": None}
            calls = []
            for i, (k, name, p) in enumerate(plan):
                if k == "text":
                    msg["content"] = p
                else:
                    calls.append({"id": "call_%d" % i, "type": "function", "function": {"name": name, "arguments": json.dumps(p)}})
            if calls:
                msg["tool_calls"] = calls
            return self.send_json(200, {"id": cid, "object": "chat.completion", "created": 1, "model": model,
                                        "choices": [{"index": 0, "message": msg, "finish_reason": "tool_calls" if calls else "stop"}],
                                        "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
        self.start_sse()
        base = {"id": cid, "object": "chat.completion.chunk", "created": 1, "model": model}
        self.sse(dict(base, choices=[{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}]))
        calls = 0
        for i, (k, name, p) in enumerate(plan):
            if k == "text":
                for piece in (p[:5], p[5:]):
                    self.sse(dict(base, choices=[{"index": 0, "delta": {"content": piece}, "finish_reason": None}]))
            else:
                a = json.dumps(p)
                self.sse(dict(base, choices=[{"index": 0, "delta": {"tool_calls": [{"index": calls, "id": "call_%d" % i, "type": "function", "function": {"name": name, "arguments": ""}}]}, "finish_reason": None}]))
                for piece in (a[:7], a[7:]):
                    self.sse(dict(base, choices=[{"index": 0, "delta": {"tool_calls": [{"index": calls, "function": {"arguments": piece}}]}, "finish_reason": None}]))
                calls += 1
        self.sse(dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": "tool_calls" if calls else "stop"}]))
        self.sse(dict(base, choices=[], usage={"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}))
        self.sse("[DONE]")

    # ---------- Codex (Responses, always SSE) ----------
    def codex(self, body):
        model = body.get("model", "")
        if self.reject_if_files(body, model):
            return
        plan = self.plan(body, model)
        rid = "resp_" + uuid.uuid4().hex[:8]
        self.start_sse()
        seq = [0]

        def ev(obj):
            obj["sequence_number"] = seq[0]
            seq[0] += 1
            self.sse(obj, obj["type"])

        resp = {"id": rid, "object": "response", "created_at": 1, "status": "in_progress", "model": model, "output": []}
        ev({"type": "response.created", "response": resp})
        out = []
        for i, (k, name, p) in enumerate(plan):
            if k == "text":
                item = {"id": "msg_%d" % i, "type": "message", "status": "in_progress", "role": "assistant", "content": []}
                ev({"type": "response.output_item.added", "output_index": i, "item": item})
                ev({"type": "response.content_part.added", "item_id": item["id"], "output_index": i, "content_index": 0, "part": {"type": "output_text", "text": "", "annotations": []}})
                ev({"type": "response.output_text.delta", "item_id": item["id"], "output_index": i, "content_index": 0, "delta": p})
                ev({"type": "response.output_text.done", "item_id": item["id"], "output_index": i, "content_index": 0, "text": p})
                done = dict(item, status="completed", content=[{"type": "output_text", "text": p, "annotations": []}])
                ev({"type": "response.content_part.done", "item_id": item["id"], "output_index": i, "content_index": 0, "part": done["content"][0]})
                ev({"type": "response.output_item.done", "output_index": i, "item": done})
            else:
                a = json.dumps(p)
                item = {"id": "fc_%d" % i, "type": "function_call", "status": "in_progress", "call_id": "call_%d" % i, "name": name, "arguments": ""}
                ev({"type": "response.output_item.added", "output_index": i, "item": item})
                for piece in (a[:7], a[7:]):
                    ev({"type": "response.function_call_arguments.delta", "item_id": item["id"], "output_index": i, "delta": piece})
                ev({"type": "response.function_call_arguments.done", "item_id": item["id"], "output_index": i, "arguments": a})
                done = dict(item, status="completed", arguments=a)
                ev({"type": "response.output_item.done", "output_index": i, "item": done})
            out.append(done)
        ev({"type": "response.completed", "response": dict(resp, status="completed", output=out, usage={"input_tokens": 10, "output_tokens": 5, "total_tokens": 15})})

    # ---------- Claude ----------
    def claude(self, body):
        model = body.get("model", "")
        if self.reject_if_files(body, model):
            return
        plan = self.plan(body, model)
        mid = "msg_" + uuid.uuid4().hex[:8]
        blocks = []
        for i, (k, name, p) in enumerate(plan):
            blocks.append({"type": "text", "text": p} if k == "text" else {"type": "tool_use", "id": "toolu_%d" % i, "name": name, "input": p})
        stop = "tool_use" if any(b["type"] == "tool_use" for b in blocks) else "end_turn"
        if not body.get("stream"):
            return self.send_json(200, {"id": mid, "type": "message", "role": "assistant", "model": model, "content": blocks,
                                        "stop_reason": stop, "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 5}})
        self.start_sse()
        self.sse({"type": "message_start", "message": {"id": mid, "type": "message", "role": "assistant", "model": model, "content": [], "stop_reason": None, "usage": {"input_tokens": 10, "output_tokens": 1}}}, "message_start")
        for i, b in enumerate(blocks):
            if b["type"] == "text":
                self.sse({"type": "content_block_start", "index": i, "content_block": {"type": "text", "text": ""}}, "content_block_start")
                self.sse({"type": "content_block_delta", "index": i, "delta": {"type": "text_delta", "text": b["text"]}}, "content_block_delta")
            else:
                a = json.dumps(b["input"])
                self.sse({"type": "content_block_start", "index": i, "content_block": {"type": "tool_use", "id": b["id"], "name": b["name"], "input": {}}}, "content_block_start")
                for piece in (a[:7], a[7:]):
                    self.sse({"type": "content_block_delta", "index": i, "delta": {"type": "input_json_delta", "partial_json": piece}}, "content_block_delta")
            self.sse({"type": "content_block_stop", "index": i}, "content_block_stop")
        self.sse({"type": "message_delta", "delta": {"stop_reason": stop, "stop_sequence": None}, "usage": {"output_tokens": 5}}, "message_delta")
        self.sse({"type": "message_stop"}, "message_stop")

    # ---------- Gemini ----------
    def gemini(self, body, model, stream):
        if self.reject_if_files(body, model):
            return
        plan = self.plan(body, model)
        parts = []
        for k, name, p in plan:
            parts.append({"text": p} if k == "text" else {"functionCall": {"name": name, "args": p}})
        resp = {"candidates": [{"content": {"role": "model", "parts": parts}, "finishReason": "STOP", "index": 0}],
                "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}, "modelVersion": model}
        if not stream:
            return self.send_json(200, resp)
        self.start_sse()
        self.sse(resp)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18080
    ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
