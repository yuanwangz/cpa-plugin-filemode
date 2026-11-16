package filemode

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var tl = Tools{Reply: "submit_reply", Read: "read_message"}

func TestRewriteResponsesTurn(t *testing.T) {
	body := `{"model":"gpt","input":[
	 {"type":"message","role":"user","content":"old question"},
	 {"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]},
	 {"role":"user","content":[{"type":"input_text","text":"first new"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},
	 {"type":"message","role":"developer","content":"dev note"},
	 {"type":"message","role":"user","content":[{"type":"input_text","text":"second "},{"type":"input_text","text":"new"}]}
	],"tools":[{"type":"function","name":"shell","parameters":{}}]}`
	r := Rewrite(FormatResponses, []byte(body), DefaultOptions())
	if !r.Rewritten || len(r.Files) != 2 || r.Files[0].Text != "first new" || r.Files[1].Text != "second \nnew" {
		t.Fatalf("rewrite = %+v", r)
	}
	in := gjson.GetBytes(r.Body, "input").Array()
	if len(in) != 7 || in[0].Get("content").String() != "old question" || in[1].Get("content.0.text").String() != "old answer" {
		t.Fatalf("history changed: %s", r.Body)
	}
	c2 := in[2].Get("content").Array()
	if len(c2) != 2 || c2[0].Get("text").String() != "[message text in the file msg-1.md]" || c2[1].Get("type").String() != "input_image" {
		t.Fatalf("msg1 = %s", in[2].Raw)
	}
	if in[3].Get("content").String() != "dev note" {
		t.Fatal("developer message touched")
	}
	c4 := in[4].Get("content").Array()
	if len(c4) != 1 || !strings.Contains(c4[0].Get("text").String(), "msg-2.md]") || !strings.Contains(c4[0].Get("text").String(), "submit_reply tool") {
		t.Fatalf("msg2 = %s", in[4].Raw)
	}
	if strings.Contains(in[2].Raw, "submit_reply") || strings.Contains(r.Body2(), "input_file") {
		t.Fatal("instruction must only follow the last new message; no attachments")
	}
	// the read call, already answered, right after the turn
	if in[5].Get("type").String() != "function_call" || in[5].Get("name").String() != "read_message" || in[5].Get("call_id").String() != "call_filemode_read_1" || in[5].Get("arguments").String() != "{}" {
		t.Fatalf("call = %s", in[5].Raw)
	}
	out := in[6].Get("output").String()
	if in[6].Get("type").String() != "function_call_output" || in[6].Get("call_id").String() != "call_filemode_read_1" || !strings.Contains(out, "<file name=\"msg-1.md\">\nfirst new\n</file>") || !strings.Contains(out, "<file name=\"msg-2.md\">\nsecond \nnew\n</file>") {
		t.Fatalf("result = %s", in[6].Raw)
	}
	tools := gjson.GetBytes(r.Body, "tools").Array()
	if len(tools) != 3 || tools[1].Get("name").String() != "submit_reply" || tools[1].Get("parameters.required.0").String() != "content" || tools[2].Get("name").String() != "read_message" {
		t.Fatalf("tools = %s", gjson.GetBytes(r.Body, "tools").Raw)
	}
}

func (r RewriteResult) Body2() string { return string(r.Body) }

func TestRewriteResponsesToolLoopKeepsTurn(t *testing.T) {
	body := `{"input":[{"role":"user","content":"do it"},{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	r := Rewrite(FormatResponses, []byte(body), DefaultOptions())
	if !r.Rewritten || len(r.Files) != 1 {
		t.Fatalf("%+v", r)
	}
	in := gjson.GetBytes(r.Body, "input").Array()
	// the read pair sits between the user message and the client's own tool loop
	if len(in) != 5 || in[1].Get("name").String() != "read_message" || in[2].Get("output").String() != "do it" || in[3].Get("call_id").String() != "c1" || in[4].Get("output").String() != "ok" {
		t.Fatalf("%s", r.Body)
	}
	// deterministic: same input, same output (prompt cache stays warm across steps)
	if r2 := Rewrite(FormatResponses, []byte(body), DefaultOptions()); string(r2.Body) != string(r.Body) {
		t.Fatal("not deterministic")
	}
}

func TestRewriteResponsesStringInputAndSkips(t *testing.T) {
	r := Rewrite(FormatResponses, []byte(`{"input":"hello <b>&"}`), DefaultOptions())
	if !r.Rewritten || len(r.Files) != 1 || gjson.GetBytes(r.Body, "input.2.output").String() != "hello <b>&" {
		t.Fatalf("%s", r.Body)
	}
	for _, b := range []string{
		`{"input":"x","tools":[{"type":"function","name":"submit_reply"}]}`,
		`{"input":"x","tools":[{"type":"function","name":"read_message"}]}`,
		`{"input":[{"type":"function_call_output","call_id":"a","output":"x"}]}`,
	} {
		if r := Rewrite(FormatResponses, []byte(b), DefaultOptions()); r.Rewritten || string(r.Body) != b {
			t.Fatalf("should skip %s: %+v", b, r)
		}
	}
}

func TestRewriteImageOnlyMessageGetsInstruction(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`
	r := Rewrite(FormatChat, []byte(body), DefaultOptions())
	if !r.Rewritten || len(r.Files) != 0 || gjson.GetBytes(r.Body, "messages.#").Int() != 1 {
		t.Fatalf("%+v %s", r, r.Body)
	}
	c := gjson.GetBytes(r.Body, "messages.0.content").Array()
	if len(c) != 2 || c[0].Get("type").String() != "image_url" || !strings.Contains(c[1].Get("text").String(), "submit_reply") {
		t.Fatalf("%s", r.Body)
	}
}

func TestRewriteChat(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"1","content":"r"},{"role":"assistant","content":"done"},{"role":"user","content":"next one"}]}`
	r := Rewrite(FormatChat, []byte(body), DefaultOptions())
	if !r.Rewritten || len(r.Files) != 1 {
		t.Fatalf("%+v", r)
	}
	ms := gjson.GetBytes(r.Body, "messages").Array()
	if ms[1].Get("content").String() != "hi" {
		t.Fatal("history touched")
	}
	c := ms[5].Get("content").Array()
	if len(c) != 1 || !strings.HasPrefix(c[0].Get("text").String(), "[message text in the file msg-1.md]") {
		t.Fatalf("%s", ms[5].Raw)
	}
	if len(ms) != 8 || ms[6].Get("role").String() != "assistant" || ms[6].Get("tool_calls.0.function.name").String() != "read_message" || ms[6].Get("tool_calls.0.id").String() != "call_filemode_read_1" ||
		ms[7].Get("role").String() != "tool" || ms[7].Get("tool_call_id").String() != "call_filemode_read_1" || ms[7].Get("content").String() != "next one" {
		t.Fatalf("%s", r.Body)
	}
	if gjson.GetBytes(r.Body, "tools.0.function.name").String() != "submit_reply" || gjson.GetBytes(r.Body, "tools.1.function.name").String() != "read_message" {
		t.Fatal("tools not injected")
	}
}

func TestRewriteClaude(t *testing.T) {
	body := `{"model":"c","max_tokens":10,"messages":[
	 {"role":"user","content":[{"type":"text","text":"task"},{"type":"text","text":"more","cache_control":{"type":"ephemeral"}}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file"}]}
	],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"}}`
	r := Rewrite(FormatClaude, []byte(body), DefaultOptions())
	if !r.Rewritten || len(r.Files) != 1 || r.Files[0].Text != "task\nmore" {
		t.Fatalf("%+v", r)
	}
	ms := gjson.GetBytes(r.Body, "messages").Array()
	if len(ms) != 5 || ms[0].Get("content.#").Int() != 1 || !strings.HasPrefix(ms[0].Get("content.0.text").String(), "[message text in the file msg-1.md]") {
		t.Fatalf("%s", r.Body)
	}
	if ms[1].Get("role").String() != "assistant" || ms[1].Get("content.0.type").String() != "tool_use" || ms[1].Get("content.0.name").String() != "read_message" || ms[1].Get("content.0.id").String() != "call_filemode_read_1" || !ms[1].Get("content.0.input").IsObject() {
		t.Fatalf("call %s", ms[1].Raw)
	}
	res := ms[2].Get("content.0")
	if ms[2].Get("role").String() != "user" || res.Get("type").String() != "tool_result" || res.Get("tool_use_id").String() != "call_filemode_read_1" || res.Get("content").String() != "task\nmore" || res.Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("result %s", ms[2].Raw)
	}
	if ms[3].Get("content.0.name").String() != "Read" || ms[4].Get("content.0.type").String() != "tool_result" {
		t.Fatal("client tool loop touched")
	}
	if gjson.GetBytes(r.Body, "tools.1.name").String() != "submit_reply" || gjson.GetBytes(r.Body, "tools.2.name").String() != "read_message" || !gjson.GetBytes(r.Body, "tools.2.input_schema").Exists() {
		t.Fatal("tools")
	}
}

func TestRewriteMixedUserMessageTextPosition(t *testing.T) {
	// tool_result first, then text: the placeholder takes the text's place
	body := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"r"},{"type":"text","text":"also this"}]}]}`
	r := Rewrite(FormatClaude, []byte(body), DefaultOptions())
	c := gjson.GetBytes(r.Body, "messages.1.content").Array()
	if len(c) != 2 || c[0].Get("type").String() != "tool_result" || c[1].Get("type").String() != "text" || gjson.GetBytes(r.Body, "messages.3.content.0.content").String() != "also this" {
		t.Fatalf("%s", r.Body)
	}
}

func TestReadResultAndAppend(t *testing.T) {
	files := []File{{"msg-1.md", "a"}, {"msg-2.md", "b"}}
	if ReadResult(files, `{"file":"msg-2.md"}`) != "b" || !strings.Contains(ReadResult(files, `{}`), "<file name=\"msg-1.md\">\na\n</file>") || ReadResult(files[:1], `{"file":"nope"}`) != "a" {
		t.Fatal("ReadResult")
	}
	o := DefaultOptions()
	b := AppendReadCall(FormatClaude, []byte(`{"messages":[{"role":"user","content":"x"}]}`), o, "toolu_9", `{"file":"msg-1.md"}`, files)
	if gjson.GetBytes(b, "messages.1.content.0.id").String() != "toolu_9" || gjson.GetBytes(b, "messages.1.content.0.input.file").String() != "msg-1.md" || gjson.GetBytes(b, "messages.2.content.0.content").String() != "a" {
		t.Fatalf("%s", b)
	}
	b = AppendReadCall(FormatChat, []byte(`{"messages":[{"role":"user","content":"x"}]}`), o, "c9", "", files)
	if gjson.GetBytes(b, "messages.1.tool_calls.0.function.arguments").String() != "{}" || gjson.GetBytes(b, "messages.2.tool_call_id").String() != "c9" {
		t.Fatalf("%s", b)
	}
	b = AppendReadCall(FormatResponses, []byte(`{"input":[{"role":"user","content":"x"}]}`), o, "c9", "{}", files)
	if gjson.GetBytes(b, "input.1.type").String() != "function_call" || gjson.GetBytes(b, "input.2.call_id").String() != "c9" {
		t.Fatalf("%s", b)
	}
}

func TestReadCalls(t *testing.T) {
	if c := ReadCalls(FormatResponses, []byte(`{"output":[{"type":"reasoning"},{"type":"function_call","call_id":"x","name":"read_message","arguments":"{}"}]}`), "read_message"); len(c) != 1 || c[0].ID != "x" {
		t.Fatalf("%v", c)
	}
	if c := ReadCalls(FormatResponses, []byte(`{"output":[{"type":"function_call","call_id":"x","name":"read_message"},{"type":"function_call","call_id":"y","name":"shell"}]}`), "read_message"); c != nil {
		t.Fatal("mixed must not loop")
	}
	if c := ReadCalls(FormatChat, []byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"a","function":{"name":"read_message","arguments":"{}"}}]}}]}`), "read_message"); len(c) != 1 {
		t.Fatal("chat")
	}
	if c := ReadCalls(FormatChat, []byte(`{"choices":[{"message":{"content":"hi"}}]}`), "read_message"); c != nil {
		t.Fatal("chat text")
	}
	if c := ReadCalls(FormatClaude, []byte(`{"content":[{"type":"thinking","thinking":"."},{"type":"tool_use","id":"t","name":"read_message","input":{}}]}`), "read_message"); len(c) != 1 || c[0].Args != "{}" {
		t.Fatal("claude")
	}
}

func TestGate(t *testing.T) {
	g := NewGate(FormatClaude, "read_message")
	if d, _ := g.Feed([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")); d {
		t.Fatal("decided too early")
	}
	if d, _ := g.Feed([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n")); d {
		t.Fatal("thinking decides nothing")
	}
	if d, rr := g.Feed([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"read_message\"}}\n\n")); !d || !rr {
		t.Fatal("read call not seen")
	}
	g = NewGate(FormatChat, "read_message")
	if d, rr := g.Feed([]byte(`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`)); !d || rr {
		t.Fatal("chat text should pass")
	}
	g = NewGate(FormatChat, "read_message")
	if d, rr := g.Feed([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read_message","arguments":""}}]}}]}`)); !d || !rr {
		t.Fatal("chat read")
	}
	g = NewGate(FormatResponses, "read_message")
	if d, rr := g.Feed([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"submit_reply\"}}\n\n")); !d || rr {
		t.Fatal("reply call should pass")
	}
	g = NewGate(FormatResponses, "read_message")
	for i := 0; i < GateHold-1; i++ {
		if d, _ := g.Feed([]byte("event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\"}\n\n")); d {
			t.Fatal("held too little")
		}
	}
	if d, rr := g.Feed([]byte("data: {}\n\n")); !d || rr {
		t.Fatal("hold limit")
	}
}

func TestRestoreDropsReadCalls(t *testing.T) {
	out, ok := Restore(FormatChat, []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"x","tool_calls":[{"id":"a","type":"function","function":{"name":"read_message","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"submit_reply","arguments":"{\"content\":\"y\"}"}}]},"finish_reason":"tool_calls"}]}`), tl)
	if !ok || gjson.GetBytes(out, "choices.0.message.content").String() != "x\n\ny" || gjson.GetBytes(out, "choices.0.message.tool_calls").Exists() || gjson.GetBytes(out, "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("%s", out)
	}
	out, _ = Restore(FormatClaude, []byte(`{"content":[{"type":"tool_use","id":"r","name":"read_message","input":{}},{"type":"tool_use","id":"u","name":"Bash","input":{}}],"stop_reason":"tool_use"}`), tl)
	if gjson.GetBytes(out, "content.#").Int() != 1 || gjson.GetBytes(out, "content.0.name").String() != "Bash" {
		t.Fatalf("%s", out)
	}
	out, _ = Restore(FormatResponses, []byte(`{"output":[{"type":"function_call","call_id":"r","name":"read_message","arguments":"{}"}],"tools":[{"type":"function","name":"read_message"},{"type":"function","name":"x"}]}`), tl)
	if gjson.GetBytes(out, "output.#").Int() != 0 || gjson.GetBytes(out, "tools.#").Int() != 1 {
		t.Fatalf("%s", out)
	}
	s := runStream(t, NewStreamRestorer(FormatClaude, tl), []string{
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"r\",\"name\":\"read_message\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
	})
	if j := strings.Join(s, ""); strings.Contains(j, "read_message") || strings.Contains(j, "input_json_delta") {
		t.Fatalf("%s", j)
	}
}

func TestReplyText(t *testing.T) {
	for in, want := range map[string]string{`{"content":"hi"}`: "hi", `{"content":{"a":1}}`: `{"a":1}`, `not json`: "not json", `"s"`: "s", ``: ""} {
		if got := ReplyText(in); got != want {
			t.Fatalf("%q -> %q want %q", in, got, want)
		}
	}
}

func TestRestoreResponses(t *testing.T) {
	body := `{"id":"r","object":"response","status":"completed","output":[{"type":"reasoning","id":"rs"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"submit_reply","arguments":"{\"content\":\"**hi**\"}"}]}`
	out, ok := Restore(FormatResponses, []byte(body), tl)
	if !ok {
		t.Fatal("not restored")
	}
	o := gjson.GetBytes(out, "output").Array()
	if len(o) != 2 || o[1].Get("type").String() != "message" || o[1].Get("content.0.text").String() != "**hi**" || o[1].Get("role").String() != "assistant" {
		t.Fatalf("%s", out)
	}
	mixed := `{"output":[{"type":"function_call","call_id":"a","name":"shell","arguments":"{}"},{"type":"function_call","call_id":"b","name":"submit_reply","arguments":"{\"content\":\"x\"}"}]}`
	out, _ = Restore(FormatResponses, []byte(mixed), tl)
	o = gjson.GetBytes(out, "output").Array()
	if o[0].Get("name").String() != "shell" || o[1].Get("type").String() != "message" || strings.Contains(string(out), "submit_reply") {
		t.Fatalf("%s", out)
	}
	plain := `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"t"}]}]}`
	if out, ok := Restore(FormatResponses, []byte(plain), tl); ok || string(out) != plain {
		t.Fatal("plain changed")
	}
}

func TestRestoreChat(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"submit_reply","arguments":"{\"content\":\"yo\"}"}}]},"finish_reason":"tool_calls"}]}`
	out, ok := Restore(FormatChat, []byte(body), tl)
	if !ok || gjson.GetBytes(out, "choices.0.message.content").String() != "yo" || gjson.GetBytes(out, "choices.0.message.tool_calls").Exists() || gjson.GetBytes(out, "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("%s", out)
	}
	mixed := `{"choices":[{"index":0,"message":{"role":"assistant","content":"pre","tool_calls":[{"id":"a","type":"function","function":{"name":"ls","arguments":"{}"}},{"id":"c","type":"function","function":{"name":"submit_reply","arguments":"{\"content\":\"yo\"}"}}]},"finish_reason":"tool_calls"}]}`
	out, _ = Restore(FormatChat, []byte(mixed), tl)
	if gjson.GetBytes(out, "choices.0.message.content").String() != "pre\n\nyo" || gjson.GetBytes(out, "choices.0.message.tool_calls.#").Int() != 1 || gjson.GetBytes(out, "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("%s", out)
	}
}

func TestRestoreClaude(t *testing.T) {
	body := `{"type":"message","content":[{"type":"tool_use","id":"t","name":"submit_reply","input":{"content":"hey"}}],"stop_reason":"tool_use"}`
	out, ok := Restore(FormatClaude, []byte(body), tl)
	if !ok || gjson.GetBytes(out, "content.0.type").String() != "text" || gjson.GetBytes(out, "content.0.text").String() != "hey" || gjson.GetBytes(out, "stop_reason").String() != "end_turn" {
		t.Fatalf("%s", out)
	}
	mixed := `{"content":[{"type":"tool_use","id":"t","name":"submit_reply","input":{"content":"hey"}},{"type":"tool_use","id":"u","name":"Bash","input":{}}],"stop_reason":"tool_use"}`
	out, _ = Restore(FormatClaude, []byte(mixed), tl)
	if gjson.GetBytes(out, "stop_reason").String() != "tool_use" || gjson.GetBytes(out, "content.1.name").String() != "Bash" {
		t.Fatalf("%s", out)
	}
}

func runStream(t *testing.T, s StreamRestorer, chunks []string) []string {
	t.Helper()
	var out []string
	for _, c := range chunks {
		for _, o := range s.Process([]byte(c)) {
			out = append(out, string(o))
		}
	}
	for _, o := range s.Finish() {
		out = append(out, string(o))
	}
	return out
}

func TestChatStream(t *testing.T) {
	chunks := []string{
		`{"id":"x","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"submit_reply","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"content\":\"he"}}]},"finish_reason":null}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"llo\"}"}}]},"finish_reason":null}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}
	out := runStream(t, NewStreamRestorer(FormatChat, tl), chunks)
	all := strings.Join(out, "\n")
	if strings.Contains(all, "submit_reply") || len(out) != 3 {
		t.Fatalf("%d %s", len(out), all)
	}
	if gjson.Get(out[1], "choices.0.delta.content").String() != "hello" || gjson.Get(out[1], "choices.0.finish_reason").String() != "stop" || out[2] != "[DONE]" {
		t.Fatalf("%s", all)
	}
}

func TestChatStreamMixedRenumbers(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"submit_reply","arguments":"{\"content\":\"t\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"ls","arguments":"{}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	out := runStream(t, NewStreamRestorer(FormatChat, tl), chunks)
	if len(out) != 2 || gjson.Get(out[0], "choices.0.delta.tool_calls.0.index").Int() != 0 || gjson.Get(out[0], "choices.0.delta.tool_calls.0.function.name").String() != "ls" {
		t.Fatalf("%v", out)
	}
	if gjson.Get(out[1], "choices.0.delta.content").String() != "t" || gjson.Get(out[1], "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("%v", out)
	}
}

func TestChatStreamPlainTextUntouched(t *testing.T) {
	chunks := []string{`{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`}
	out := runStream(t, NewStreamRestorer(FormatChat, tl), chunks)
	if strings.Join(out, "|") != strings.Join(chunks, "|") {
		t.Fatalf("%v", out)
	}
}

func sse(ev, data string) string { return "event: " + ev + "\ndata: " + data + "\n\n" }

func TestResponsesStream(t *testing.T) {
	chunks := []string{
		sse("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"r","status":"in_progress","output":[]}}`),
		sse("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"submit_reply","arguments":""}}`),
		sse("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"content\":\"hi"}`),
		sse("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":3,"output_index":0,"item_id":"fc_1","delta":"!\"}"}`),
		sse("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","sequence_number":4,"output_index":0,"item_id":"fc_1","arguments":"{\"content\":\"hi!\"}"}`),
		sse("response.output_item.done", `{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"submit_reply","arguments":"{\"content\":\"hi!\"}"}}`),
		sse("response.completed", `{"type":"response.completed","sequence_number":6,"response":{"id":"r","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"submit_reply","arguments":"{\"content\":\"hi!\"}"}]}}`),
	}
	out := runStream(t, NewStreamRestorer(FormatResponses, tl), chunks)
	all := strings.Join(out, "")
	if strings.Contains(all, "submit_reply") || strings.Contains(all, "function_call") {
		t.Fatalf("%s", all)
	}
	var types []string
	var seq []int64
	for _, o := range out {
		frames, _ := parseChunk([]byte(o))
		for _, f := range frames {
			types = append(types, gjson.Get(f.data, "type").String())
			seq = append(seq, gjson.Get(f.data, "sequence_number").Int())
			if f.event != gjson.Get(f.data, "type").String() {
				t.Fatalf("event line mismatch %q", o)
			}
		}
	}
	want := "response.created,response.output_item.added,response.content_part.added,response.output_text.delta,response.output_text.done,response.content_part.done,response.output_item.done,response.completed"
	if strings.Join(types, ",") != want {
		t.Fatalf("%v", types)
	}
	for i := range seq {
		if seq[i] != int64(i) {
			t.Fatalf("sequence %v", seq)
		}
	}
	last := out[len(out)-1]
	frames, _ := parseChunk([]byte(last))
	if gjson.Get(frames[0].data, "response.output.0.content.0.text").String() != "hi!" {
		t.Fatalf("%s", last)
	}
}

func TestClaudeStream(t *testing.T) {
	chunks := []string{
		sse("message_start", `{"type":"message_start","message":{"id":"m","content":[]}}`),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"submit_reply","input":{}}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"content\": \"ok"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"}"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	}
	out := runStream(t, NewStreamRestorer(FormatClaude, tl), chunks)
	all := strings.Join(out, "")
	if strings.Contains(all, "submit_reply") || strings.Contains(all, "tool_use") || !strings.Contains(all, `"text":"ok"`) || !strings.Contains(all, `"stop_reason":"end_turn"`) {
		t.Fatalf("%s", all)
	}
	if !strings.Contains(all, "event: content_block_start\ndata: {\"content_block\":{\"text\":\"\",\"type\":\"text\"},\"index\":0,\"type\":\"content_block_start\"}\n\n") {
		t.Fatalf("framing: %q", all)
	}
}

func TestResponsesStreamSplitEventAndDataChunks(t *testing.T) {
	var chunks []string
	for _, e := range []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc","call_id":"c","name":"submit_reply","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"output_index":0,"item_id":"fc","delta":"{\"content\":\"x\"}"}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"fc","call_id":"c","name":"submit_reply","arguments":"{\"content\":\"x\"}"}}`,
		`{"type":"response.completed","sequence_number":3,"response":{"output":[{"type":"function_call","id":"fc","call_id":"c","name":"submit_reply","arguments":"{\"content\":\"x\"}"}]}}`,
	} {
		chunks = append(chunks, "event: "+gjson.Get(e, "type").String()+"\n", "data: "+e+"\n\n")
	}
	out := runStream(t, NewStreamRestorer(FormatResponses, tl), chunks)
	all := strings.Join(out, "")
	if strings.Contains(all, "submit_reply") || strings.Contains(all, "event: response.output_item.added\nevent:") {
		t.Fatalf("%q", all)
	}
	last, _ := parseChunk([]byte(out[len(out)-1]))
	if last[0].event != "response.completed" || gjson.Get(last[0].data, "response.output.0.content.0.text").String() != "x" {
		t.Fatalf("%q", out[len(out)-1])
	}
}

func TestToolChoiceNoneKeepsToolTable(t *testing.T) {
	o := DefaultOptions()
	cases := []struct {
		format, body, msgs, toolName string
	}{
		{FormatClaude, `{"messages":[{"role":"user","content":"x"}],"tools":[{"name":"Read","input_schema":{}}],"tool_choice":{"type":"none"}}`, "messages", "name"},
		{FormatChat, `{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"shell"}}],"tool_choice":"none"}`, "messages", "function.name"},
		{FormatResponses, `{"input":[{"type":"message","role":"user","content":"x"}],"tools":[{"type":"function","name":"shell"}],"tool_choice":"none"}`, "input", "name"},
	}
	for _, c := range cases {
		r := Rewrite(c.format, []byte(c.body), o)
		if r.Rewritten || !r.ToolsOnly {
			t.Fatalf("%s: %+v", c.format, r)
		}
		if gjson.GetBytes(r.Body, c.msgs).Raw != gjson.Get(c.body, c.msgs).Raw {
			t.Fatalf("%s: messages touched: %s", c.format, r.Body)
		}
		tools := gjson.GetBytes(r.Body, "tools").Array()
		if len(tools) != 3 || tools[1].Get(c.toolName).String() != o.ToolName || tools[2].Get(c.toolName).String() != o.ReadTool {
			t.Fatalf("%s: tools %s", c.format, gjson.GetBytes(r.Body, "tools").Raw)
		}
		// same tool table as a rewritten request of the same client
		full := Rewrite(c.format, []byte(strings.Replace(strings.Replace(c.body, `,"tool_choice":"none"`, "", 1), `,"tool_choice":{"type":"none"}`, "", 1)), o)
		if !full.Rewritten || gjson.GetBytes(full.Body, "tools").Raw != gjson.GetBytes(r.Body, "tools").Raw {
			t.Fatalf("%s: tool table differs\n%s\n%s", c.format, gjson.GetBytes(full.Body, "tools").Raw, gjson.GetBytes(r.Body, "tools").Raw)
		}
	}
}

func TestNoReplyInjectsOnlyTheReadTool(t *testing.T) {
	o := DefaultOptions()
	o.NoReply = true
	cases := []struct {
		format, body, msgs, toolName string
	}{
		{FormatClaude, `{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"Read","input_schema":{}}]}`, "messages", "name"},
		{FormatChat, `{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"shell"}}]}`, "messages", "function.name"},
		{FormatResponses, `{"input":[{"type":"message","role":"user","content":"hello"}],"tools":[{"type":"function","name":"shell"}]}`, "input", "name"},
	}
	for _, c := range cases {
		r := Rewrite(c.format, []byte(c.body), o)
		if !r.Rewritten || len(r.Files) != 1 || r.Files[0].Text != "hello" {
			t.Fatalf("%s: %+v", c.format, r)
		}
		tools := gjson.GetBytes(r.Body, "tools").Array()
		if len(tools) != 2 || tools[1].Get(c.toolName).String() != o.ReadTool {
			t.Fatalf("%s: tools %s", c.format, gjson.GetBytes(r.Body, "tools").Raw)
		}
		msgs := gjson.GetBytes(r.Body, c.msgs).Raw
		if strings.Contains(msgs, o.ToolName) || strings.Contains(msgs, "File mode") || !strings.Contains(msgs, o.ReadTool) {
			t.Fatalf("%s: messages %s", c.format, msgs)
		}
		// tool_choice none keeps the same table
		none := strings.Replace(c.body, `}]}`, `}],"tool_choice":"none"}`, 1)
		if c.format == FormatClaude {
			none = strings.Replace(c.body, `}]}`, `}],"tool_choice":{"type":"none"}}`, 1)
		}
		n := Rewrite(c.format, []byte(none), o)
		if !n.ToolsOnly || gjson.GetBytes(n.Body, "tools").Raw != gjson.GetBytes(r.Body, "tools").Raw {
			t.Fatalf("%s: tools only %s", c.format, n.Body)
		}
	}
	// a client tool that shares the reply tool's name no longer blocks the rewrite
	body := `{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"submit_reply"}}]}`
	if r := Rewrite(FormatChat, []byte(body), o); !r.Rewritten {
		t.Fatalf("client submit_reply blocked: %+v", r)
	}
}

func TestRestoreLeavesClientToolWhenReplyOff(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"submit_reply","arguments":"{\"content\":\"x\"}"}}]}}]}`
	out, changed := Restore(FormatChat, []byte(body), Tools{Read: DefaultReadToolName})
	if changed || string(out) != body {
		t.Fatalf("changed: %s", out)
	}
}
