#!/usr/bin/env python3
"""End-to-end checks against a running CPA (with the plugin) and mock_upstream.py.

Usage: e2e_test.py <workdir>   (the workdir written by start.sh; mock log at <workdir>/mock.jsonl)
Prints one line per check and a summary; exit code 1 when anything fails.
"""
import json
import os
import sys
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mock_upstream import attachments  # noqa: E402

DIR = sys.argv[1]
BASE = "http://127.0.0.1:%s" % os.environ.get("CPA_PORT", "18317")
LOG = os.path.join(DIR, "mock.jsonl")
TXT = "你好，请看 **markdown** <tag> & \"quotes\"\n第二行"
TOOL = "submit_reply"
results = []
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))


def check(name, cond, detail=""):
    results.append((name, bool(cond)))
    print(("PASS " if cond else "FAIL ") + name + ("" if cond else "  -- " + str(detail)[:600]))


def mark():
    with open(LOG) as f:
        return sum(1 for _ in f)


def upstream_since(n):
    with open(LOG) as f:
        lines = f.readlines()[n:]
    return [json.loads(x) for x in lines]


def post(path, body, stream=False):
    req = urllib.request.Request(BASE + path, data=json.dumps(body).encode(), method="POST",
                                 headers={"Content-Type": "application/json", "Authorization": "Bearer test-key",
                                          "x-api-key": "test-key", "anthropic-version": "2023-06-01"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def sse_events(raw):
    out = []
    for block in raw.replace("\r\n", "\n").split("\n\n"):
        data = [l[5:].strip() for l in block.split("\n") if l.startswith("data:")]
        if not data:
            continue
        d = "\n".join(data)
        if d == "[DONE]":
            out.append("[DONE]")
            continue
        try:
            out.append(json.loads(d))
        except Exception:
            pass
    return out


# ---------- client-format helpers: (text, tool names) ----------

def body_for(fmt, model, text, stream):
    if fmt == "responses":
        return "/v1/responses", {"model": model, "stream": stream, "input": [{"role": "user", "content": [{"type": "input_text", "text": text}]}]}
    if fmt == "chat":
        return "/v1/chat/completions", {"model": model, "stream": stream, "messages": [{"role": "system", "content": "be brief"}, {"role": "user", "content": text}]}
    return "/v1/messages", {"model": model, "max_tokens": 256, "stream": stream, "messages": [{"role": "user", "content": text}]}


def parse(fmt, raw, stream):
    texts, tools = [], []
    if fmt == "responses":
        if stream:
            final = None
            for e in sse_events(raw):
                if e == "[DONE]":
                    continue
                if e.get("type") == "response.output_text.delta":
                    texts.append(e.get("delta", ""))
                if e.get("type") == "response.output_item.added" and e["item"].get("type") == "function_call":
                    tools.append(e["item"]["name"])
                if e.get("type") == "response.completed":
                    final = e["response"]
            ftexts = [c.get("text", "") for it in (final or {}).get("output", []) if it.get("type") == "message" for c in it.get("content", [])]
            return "".join(texts), tools, "".join(ftexts)
        r = json.loads(raw)
        for it in r.get("output", []):
            if it.get("type") == "message":
                texts += [c.get("text", "") for c in it.get("content", [])]
            if it.get("type") == "function_call":
                tools.append(it["name"])
        return "".join(texts), tools, None
    if fmt == "chat":
        if stream:
            seen = {}
            for e in sse_events(raw):
                if e == "[DONE]" or not e.get("choices"):
                    continue
                d = e["choices"][0].get("delta", {})
                if d.get("content"):
                    texts.append(d["content"])
                for tc in d.get("tool_calls") or []:
                    if tc.get("function", {}).get("name"):
                        seen[tc.get("index")] = tc["function"]["name"]
            return "".join(texts), list(seen.values()), None
        m = json.loads(raw)["choices"][0]["message"]
        return m.get("content") or "", [c["function"]["name"] for c in m.get("tool_calls") or []], None
    # claude
    if stream:
        for e in sse_events(raw):
            if e == "[DONE]":
                continue
            if e.get("type") == "content_block_start" and e["content_block"].get("type") == "tool_use":
                tools.append(e["content_block"]["name"])
            if e.get("type") == "content_block_delta" and e["delta"].get("type") == "text_delta":
                texts.append(e["delta"]["text"])
        return "".join(texts), tools, None
    r = json.loads(raw)
    for b in r.get("content", []):
        if b.get("type") == "text":
            texts.append(b["text"])
        if b.get("type") == "tool_use":
            tools.append(b["name"])
    return "".join(texts), tools, None


def has_text(body, needle):
    return any(isinstance(v, str) and needle in v for v in strings(body))


def strings(x):
    if isinstance(x, dict):
        for v in x.values():
            yield from strings(v)
    elif isinstance(x, list):
        for v in x:
            yield from strings(v)
    elif isinstance(x, str):
        yield x


def upstream_text(body):
    """All plain strings in an upstream request (to find inline text)."""
    return json.dumps(body, ensure_ascii=False)


def upstream_model(rec):
    b = rec["body"]
    if "model" in b:
        return b["model"]
    p = rec["path"]
    if "/models/" in p:
        return p.split("/models/")[1].split(":")[0]
    return None


# ---------- checks ----------

def test_models():
    with urllib.request.urlopen(urllib.request.Request(BASE + "/v1/models", headers={"Authorization": "Bearer test-key"})) as r:
        ids = [m["id"] for m in json.loads(r.read())["data"]]
    check("/v1/models lists prefixed aliases (file-oa-reply, file-cl-reply, file-cx-reply, file-gm-reply)",
          all(("file-" + m) in ids for m in ("oa-reply", "cl-reply", "cx-reply", "gm-reply")), ids)
    check("/v1/models keeps the plain models", "oa-reply" in ids, ids)


def test_passthrough():
    for fmt, model in (("chat", "oa-text"), ("responses", "oa-text"), ("claude", "cl-text"), ("responses", "cx-text")):
        n = mark()
        path, body = body_for(fmt, model, TXT, False)
        st, raw = post(path, body)
        ups = upstream_since(n)
        ok = st == 200 and len(ups) == 1 and TOOL not in json.dumps(ups[0]["body"]) and not attachments(ups[0]["body"]) and has_text(ups[0]["body"], TXT)
        check("no prefix: %s -> %s request untouched (no tool, no file, text inline)" % (fmt, model), ok, (st, raw[:300], ups))
        text, tools, _ = parse(fmt, raw, False) if st == 200 else ("", [], None)
        check("no prefix: %s -> %s plain answer" % (fmt, model), text == "plain text answer", raw[:300])


EXPECT_SHAPE = {  # upstream format -> description of the attachment shape we expect
    "oa": "file part (chat)", "cl": "document text source", "cx": "input_file", "gm": "inline_data text/plain",
}


def attachment_shape_ok(up, rec):
    b = rec["body"]
    s = json.dumps(b)
    if up == "oa":
        return '"type": "file"' in s and "msg-1.md" in s
    if up == "cl":
        docs = [d for d in walk(b) if isinstance(d, dict) and d.get("type") == "document"]
        return bool(docs) and all(d["source"].get("type") == "text" and d["source"].get("media_type") == "text/plain" for d in docs)
    if up == "cx":
        return '"input_file"' in s and "msg-1.md" in s
    if up == "gm":
        parts = [d for d in walk(b) if isinstance(d, dict) and ("inline_data" in d or "inlineData" in d)]
        mts = [(p.get("inline_data") or p.get("inlineData")) for p in parts]
        mts = [m.get("mime_type") or m.get("mimeType") for m in mts]
        return "text/plain" in mts and "text/markdown" not in mts
    return False


def walk(x):
    if isinstance(x, dict):
        yield x
        for v in x.values():
            yield from walk(v)
    elif isinstance(x, list):
        for v in x:
            yield from walk(v)


def test_reply(fmt, up, stream, inline=False):
    model = "file-%s-reply" % up
    n = mark()
    path, body = body_for(fmt, model, TXT, stream)
    st, raw = post(path, body, stream)
    ups = upstream_since(n)
    tag = "%s->%s %s" % (fmt, up, "stream" if stream else "non-stream")
    check("[%s] HTTP 200" % tag, st == 200, raw[:400])
    if st != 200 or not ups:
        check("[%s] upstream reached" % tag, False, ups)
        return
    rec = ups[0]
    check("[%s] prefix stripped (upstream model %s)" % (tag, upstream_model(rec)), upstream_model(rec) == "%s-reply" % up, upstream_model(rec))
    check("[%s] submit_reply tool injected" % tag, TOOL in json.dumps(rec["body"]), "")
    check("[%s] instruction appended" % tag, "by calling the submit_reply tool" in json.dumps(rec["body"]), "")
    if inline:
        check("[%s] text kept inline (translator would drop the file)" % tag, not attachments(rec["body"]) and "你好" in upstream_text(rec["body"]), attachments(rec["body"]))
        want = "INLINE-ECHO"
    else:
        att = attachments(rec["body"])
        check("[%s] new message body travels as attachment (%s)" % (tag, EXPECT_SHAPE[up]), att == [TXT] and attachment_shape_ok(up, rec), (att, json.dumps(rec["body"], ensure_ascii=False)[:1500]))
        check("[%s] message text not left inline" % tag, "**markdown**" not in upstream_text(rec["body"]).replace(json.dumps(TXT, ensure_ascii=False), ""), "")
        want = "ECHO:" + TXT
    text, tools, final = parse(fmt, raw, stream)
    check("[%s] reply restored as plain text" % tag, text == want, (text, raw[:800]))
    check("[%s] client never sees submit_reply" % tag, TOOL not in raw and TOOL not in tools, raw[:3000])
    if final is not None:
        check("[%s] response.completed output carries the text" % tag, final == want, (final, raw[-1500:]))


def test_mixed(fmt, up, stream):
    model = "file-%s-mixed" % up
    path, body = body_for(fmt, model, TXT, stream)
    st, raw = post(path, body, stream)
    tag = "%s->%s %s" % (fmt, up, "stream" if stream else "non-stream")
    text, tools, _ = parse(fmt, raw, stream) if st == 200 else ("", [], None)
    want = "INLINE-ECHO" if (fmt == "claude" and up == "cx") else "ECHO:" + TXT
    check("[mixed %s] other tool call kept, submit_reply turned into text" % tag,
          st == 200 and tools == ["list_dir"] and text.endswith(want) and TOOL not in raw, (st, text, tools, raw[:3000]))


def test_text(fmt, up, stream):
    path, body = body_for(fmt, "file-%s-text" % up, TXT, stream)
    st, raw = post(path, body, stream)
    text, tools, _ = parse(fmt, raw, stream) if st == 200 else ("", [], None)
    check("[direct text %s->%s %s] passes through" % (fmt, up, "stream" if stream else "non-stream"),
          st == 200 and text == "plain text answer" and not tools, (st, raw[:400]))


def test_reject(fmt, up, stream):
    n = mark()
    path, body = body_for(fmt, "file-%s-reject" % up, TXT, stream)
    st, raw = post(path, body, stream)
    ups = upstream_since(n)
    text, tools, _ = parse(fmt, raw, stream) if st == 200 else ("", [], None)
    tag = "%s->%s %s" % (fmt, up, "stream" if stream else "non-stream")
    with_files = [u for u in ups if attachments(u["body"])]
    plain = [u for u in ups if not attachments(u["body"])]
    check("[reject %s] first attempt had files, resent once as the original" % tag,
          len(with_files) >= 1 and len(plain) == 1 and TOOL not in json.dumps(plain[0]["body"]) and has_text(plain[0]["body"], TXT),
          [(u["path"], len(attachments(u["body"]))) for u in ups])
    check("[reject %s] client gets the plain answer" % tag, st == 200 and text == "plain text answer", (st, raw[:300]))


def test_history_untouched():
    n = mark()
    body = {"model": "file-oa-reply", "input": [
        {"role": "user", "content": [{"type": "input_text", "text": "old question"}]},
        {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "old answer"}]},
        {"role": "user", "content": [{"type": "input_text", "text": "first new"}, {"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="}]},
        {"role": "user", "content": [{"type": "input_text", "text": "second new"}]},
    ]}
    st, raw = post("/v1/responses", body)
    ups = upstream_since(n)
    s = json.dumps(ups[0]["body"], ensure_ascii=False) if ups else ""
    check("history untouched, two new messages -> msg-1.md / msg-2.md, image kept",
          st == 200 and "old question" in s and "old answer" in s and attachments(ups[0]["body"]) == ["first new", "second new"]
          and "msg-2.md" in s and "image_url" in s, s[:1500])


def test_gemini_client(stream):
    """A Gemini-format client: CPA translates it into the executor's format and back."""
    n = mark()
    action = "streamGenerateContent?alt=sse" if stream else "generateContent"
    body = {"contents": [{"role": "user", "parts": [{"text": TXT}]}]}
    st, raw = post("/v1beta/models/file-oa-reply:" + action, body, stream)
    ups = upstream_since(n)
    tag = "gemini client->oa %s" % ("stream" if stream else "non-stream")
    check("[%s] file attached upstream, prefix stripped" % tag, st == 200 and ups and attachments(ups[0]["body"]) == [TXT] and upstream_model(ups[0]) == "oa-reply", (st, raw[:300]))
    texts, calls = [], []
    for e in (sse_events(raw) if stream else [json.loads(raw)] if st == 200 else []):
        if e == "[DONE]":
            continue
        for c in e.get("candidates", []):
            for p in c.get("content", {}).get("parts", []):
                if "text" in p:
                    texts.append(p["text"])
                if "functionCall" in p:
                    calls.append(p["functionCall"]["name"])
    check("[%s] reply restored as text" % tag, "".join(texts) == "ECHO:" + TXT and not calls and TOOL not in raw, raw[:800])


def main():
    test_models()
    test_passthrough()
    test_history_untouched()
    for stream in (False, True):
        for fmt in ("responses", "chat", "claude"):
            for up in ("oa", "cl", "cx", "gm"):
                test_reply(fmt, up, stream, inline=(fmt == "claude" and up == "cx"))
    for stream in (False, True):
        test_gemini_client(stream)
        for fmt in ("responses", "chat", "claude"):
            for up in ("oa", "cl", "cx", "gm"):
                test_mixed(fmt, up, stream)
        for fmt, up in (("responses", "oa"), ("chat", "cl"), ("claude", "gm"), ("responses", "cx")):
            test_text(fmt, up, stream)
        for fmt, up in (("responses", "oa"), ("chat", "oa"), ("claude", "cl"), ("responses", "gm"), ("chat", "cx")):
            test_reject(fmt, up, stream)
    passed = sum(1 for _, ok in results if ok)
    print("\nSUMMARY: %d/%d passed" % (passed, len(results)))
    sys.exit(0 if passed == len(results) else 1)


if __name__ == "__main__":
    main()
