package filemode

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func decodeDataURL(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, ",")
	if !strings.HasPrefix(s, "data:text/markdown;base64,") || i < 0 {
		t.Fatalf("bad data url %q", s)
	}
	raw, err := base64.StdEncoding.DecodeString(s[i+1:])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRewriteResponsesTurn(t *testing.T) {
	body := `{"model":"gpt","input":[
	 {"type":"message","role":"user","content":"old question"},
	 {"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]},
	 {"role":"user","content":[{"type":"input_text","text":"first new"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},
	 {"type":"message","role":"developer","content":"dev note"},
	 {"type":"message","role":"user","content":[{"type":"input_text","text":"second "},{"type":"input_text","text":"new"}]}
	],"tools":[{"type":"function","name":"shell","parameters":{}}]}`
	r := Rewrite(FormatResponses, []byte(body), DefaultOptions())
	if !r.Rewritten || r.Files != 2 {
		t.Fatalf("rewrite = %+v", r)
	}
	in := gjson.GetBytes(r.Body, "input").Array()
	if in[0].Get("content").String() != "old question" || in[1].Get("content.0.text").String() != "old answer" {
		t.Fatalf("history changed: %s", r.Body)
	}
	c2 := in[2].Get("content").Array()
	if len(c2) != 3 || c2[0].Get("text").String() != "[message text in the attached file msg-1.md]" || c2[1].Get("type").String() != "input_file" || c2[1].Get("filename").String() != "msg-1.md" || c2[2].Get("type").String() != "input_image" {
		t.Fatalf("msg1 = %s", in[2].Raw)
	}
	if decodeDataURL(t, c2[1].Get("file_data").String()) != "first new" {
		t.Fatal("msg1 file content")
	}
	if in[3].Get("content").String() != "dev note" {
		t.Fatal("developer message touched")
	}
	c4 := in[4].Get("content").Array()
	if len(c4) != 2 || !strings.Contains(c4[0].Get("text").String(), "msg-2.md]") || !strings.Contains(c4[0].Get("text").String(), "submit_reply tool") {
		t.Fatalf("msg2 = %s", in[4].Raw)
	}
	if decodeDataURL(t, c4[1].Get("file_data").String()) != "second \nnew" {
		t.Fatalf("msg2 file content %q", decodeDataURL(t, c4[1].Get("file_data").String()))
	}
	if strings.Contains(in[2].Raw, "submit_reply") {
		t.Fatal("instruction must only follow the last new message")
	}
	tools := gjson.GetBytes(r.Body, "tools").Array()
	if len(tools) != 2 || tools[1].Get("name").String() != "submit_reply" || tools[1].Get("parameters.required.0").String() != "content" {
		t.Fatalf("tools = %s", gjson.GetBytes(r.Body, "tools").Raw)
	}
}

func TestRewriteResponsesToolLoopKeepsTurnFiles(t *testing.T) {
	body := `{"input":[{"role":"user","content":"do it"},{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	r := Rewrite(FormatResponses, []byte(body), DefaultOptions())
	if !r.Rewritten || r.Files != 1 {
		t.Fatalf("%+v", r)
	}
	in := gjson.GetBytes(r.Body, "input").Array()
	if in[0].Get("content.1.type").String() != "input_file" || in[2].Get("output").String() != "ok" {
		t.Fatalf("%s", r.Body)
	}
	// deterministic: same input, same output (prompt cache stays warm across steps)
	if r2 := Rewrite(FormatResponses, []byte(body), DefaultOptions()); string(r2.Body) != string(r.Body) {
		t.Fatal("not deterministic")
	}
}

func TestRewriteResponsesStringInputAndSkips(t *testing.T) {
	r := Rewrite(FormatResponses, []byte(`{"input":"hello <b>&"}`), DefaultOptions())
	if !r.Rewritten || r.Files != 1 || decodeDataURL(t, gjson.GetBytes(r.Body, "input.0.content.1.file_data").String()) != "hello <b>&" {
		t.Fatalf("%s", r.Body)
	}
	for _, b := range []string{
		`{"input":"x","tool_choice":"none"}`,
		`{"input":"x","tools":[{"type":"function","name":"submit_reply"}]}`,
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
	if !r.Rewritten || r.Files != 0 {
		t.Fatalf("%+v", r)
	}
	c := gjson.GetBytes(r.Body, "messages.0.content").Array()
	if len(c) != 2 || c[0].Get("type").String() != "image_url" || !strings.Contains(c[1].Get("text").String(), "submit_reply") {
		t.Fatalf("%s", r.Body)
	}
}

func TestRewriteChat(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"1","content":"r"},{"role":"assistant","content":"done"},{"role":"user","content":"next one"}]}`
	r := Rewrite(FormatChat, []byte(body), DefaultOptions())
	if !r.Rewritten || r.Files != 1 {
		t.Fatalf("%+v", r)
	}
	ms := gjson.GetBytes(r.Body, "messages").Array()
	if ms[1].Get("content").String() != "hi" {
		t.Fatal("history touched")
	}
	c := ms[5].Get("content").Array()
	if c[1].Get("type").String() != "file" || c[1].Get("file.filename").String() != "msg-1.md" || decodeDataURL(t, c[1].Get("file.file_data").String()) != "next one" {
		t.Fatalf("%s", ms[5].Raw)
	}
	if gjson.GetBytes(r.Body, "tools.0.function.name").String() != "submit_reply" {
		t.Fatal("tool not injected")
	}
	inl := DefaultOptions()
	inl.InlineText = true
	r = Rewrite(FormatChat, []byte(body), inl)
	if !r.Rewritten || r.Files != 0 || !strings.HasPrefix(gjson.GetBytes(r.Body, "messages.5.content.0.text").String(), "next one\n\n") {
		t.Fatalf("inline: %s", r.Body)
	}
}

func TestRewriteClaude(t *testing.T) {
	body := `{"model":"c","max_tokens":10,"messages":[
	 {"role":"user","content":[{"type":"text","text":"task"},{"type":"text","text":"more","cache_control":{"type":"ephemeral"}}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file"}]}
	],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"}}`
	r := Rewrite(FormatClaude, []byte(body), DefaultOptions())
	if !r.Rewritten || r.Files != 1 {
		t.Fatalf("%+v", r)
	}
	c := gjson.GetBytes(r.Body, "messages.0.content").Array()
	if len(c) != 2 || c[1].Get("type").String() != "document" || c[1].Get("source.media_type").String() != "text/plain" || c[1].Get("title").String() != "msg-1.md" || c[1].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("%s", gjson.GetBytes(r.Body, "messages.0").Raw)
	}
	raw, _ := base64.StdEncoding.DecodeString(c[1].Get("source.data").String())
	if string(raw) != "task\nmore" {
		t.Fatalf("doc %q", raw)
	}
	if gjson.GetBytes(r.Body, "messages.2.content.0.type").String() != "tool_result" {
		t.Fatal("tool_result message touched")
	}
	if gjson.GetBytes(r.Body, "tools.1.name").String() != "submit_reply" || !gjson.GetBytes(r.Body, "tools.1.input_schema").Exists() {
		t.Fatal("tool")
	}
	if r := Rewrite(FormatClaude, []byte(`{"messages":[{"role":"user","content":"x"}],"tool_choice":{"type":"none"}}`), DefaultOptions()); r.Rewritten {
		t.Fatal("tool_choice none should skip")
	}
}

func TestRewriteMixedUserMessageTextPosition(t *testing.T) {
	// tool_result first, then text: placeholder and document take the text's place
	body := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"r"},{"type":"text","text":"also this"}]}]}`
	r := Rewrite(FormatClaude, []byte(body), DefaultOptions())
	c := gjson.GetBytes(r.Body, "messages.1.content").Array()
	if len(c) != 3 || c[0].Get("type").String() != "tool_result" || c[1].Get("type").String() != "text" || c[2].Get("type").String() != "document" {
		t.Fatalf("%s", r.Body)
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
	out, ok := Restore(FormatResponses, []byte(body), "submit_reply")
	if !ok {
		t.Fatal("not restored")
	}
	o := gjson.GetBytes(out, "output").Array()
	if len(o) != 2 || o[1].Get("type").String() != "message" || o[1].Get("content.0.text").String() != "**hi**" || o[1].Get("role").String() != "assistant" {
		t.Fatalf("%s", out)
	}
	mixed := `{"output":[{"type":"function_call","call_id":"a","name":"shell","arguments":"{}"},{"type":"function_call","call_id":"b","name":"submit_reply","arguments":"{\"content\":\"x\"}"}]}`
	out, _ = Restore(FormatResponses, []byte(mixed), "submit_reply")
	o = gjson.GetBytes(out, "output").Array()
	if o[0].Get("name").String() != "shell" || o[1].Get("type").String() != "message" || strings.Contains(string(out), "submit_reply") {
		t.Fatalf("%s", out)
	}
	plain := `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"t"}]}]}`
	if out, ok := Restore(FormatResponses, []byte(plain), "submit_reply"); ok || string(out) != plain {
		t.Fatal("plain changed")
	}
}

func TestRestoreChat(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"submit_reply","arguments":"{\"content\":\"yo\"}"}}]},"finish_reason":"tool_calls"}]}`
	out, ok := Restore(FormatChat, []byte(body), "submit_reply")
	if !ok || gjson.GetBytes(out, "choices.0.message.content").String() != "yo" || gjson.GetBytes(out, "choices.0.message.tool_calls").Exists() || gjson.GetBytes(out, "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("%s", out)
	}
	mixed := `{"choices":[{"index":0,"message":{"role":"assistant","content":"pre","tool_calls":[{"id":"a","type":"function","function":{"name":"ls","arguments":"{}"}},{"id":"c","type":"function","function":{"name":"submit_reply","arguments":"{\"content\":\"yo\"}"}}]},"finish_reason":"tool_calls"}]}`
	out, _ = Restore(FormatChat, []byte(mixed), "submit_reply")
	if gjson.GetBytes(out, "choices.0.message.content").String() != "pre\n\nyo" || gjson.GetBytes(out, "choices.0.message.tool_calls.#").Int() != 1 || gjson.GetBytes(out, "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("%s", out)
	}
}

func TestRestoreClaude(t *testing.T) {
	body := `{"type":"message","content":[{"type":"tool_use","id":"t","name":"submit_reply","input":{"content":"hey"}}],"stop_reason":"tool_use"}`
	out, ok := Restore(FormatClaude, []byte(body), "submit_reply")
	if !ok || gjson.GetBytes(out, "content.0.type").String() != "text" || gjson.GetBytes(out, "content.0.text").String() != "hey" || gjson.GetBytes(out, "stop_reason").String() != "end_turn" {
		t.Fatalf("%s", out)
	}
	mixed := `{"content":[{"type":"tool_use","id":"t","name":"submit_reply","input":{"content":"hey"}},{"type":"tool_use","id":"u","name":"Bash","input":{}}],"stop_reason":"tool_use"}`
	out, _ = Restore(FormatClaude, []byte(mixed), "submit_reply")
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
	out := runStream(t, NewStreamRestorer(FormatChat, "submit_reply"), chunks)
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
	out := runStream(t, NewStreamRestorer(FormatChat, "submit_reply"), chunks)
	if len(out) != 2 || gjson.Get(out[0], "choices.0.delta.tool_calls.0.index").Int() != 0 || gjson.Get(out[0], "choices.0.delta.tool_calls.0.function.name").String() != "ls" {
		t.Fatalf("%v", out)
	}
	if gjson.Get(out[1], "choices.0.delta.content").String() != "t" || gjson.Get(out[1], "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("%v", out)
	}
}

func TestChatStreamPlainTextUntouched(t *testing.T) {
	chunks := []string{`{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`}
	out := runStream(t, NewStreamRestorer(FormatChat, "submit_reply"), chunks)
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
	out := runStream(t, NewStreamRestorer(FormatResponses, "submit_reply"), chunks)
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
	out := runStream(t, NewStreamRestorer(FormatClaude, "submit_reply"), chunks)
	all := strings.Join(out, "")
	if strings.Contains(all, "submit_reply") || strings.Contains(all, "tool_use") || !strings.Contains(all, `"text":"ok"`) || !strings.Contains(all, `"stop_reason":"end_turn"`) {
		t.Fatalf("%s", all)
	}
	if !strings.Contains(all, "event: content_block_start\ndata: {\"content_block\":{\"text\":\"\",\"type\":\"text\"},\"index\":0,\"type\":\"content_block_start\"}\n\n") {
		t.Fatalf("framing: %q", all)
	}
}

func TestFixUpstream(t *testing.T) {
	claude := `{"messages":[{"role":"user","content":[{"type":"text","text":"[message text in the attached file msg-1.md]"},{"type":"document","source":{"type":"base64","media_type":"text/markdown","data":"` + base64.StdEncoding.EncodeToString([]byte("你好")) + `"}},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBE"}}]}],"tools":[{"name":"submit_reply"}]}`
	out, ok := FixUpstream("claude", []byte(claude), "")
	if !ok || gjson.GetBytes(out, "messages.0.content.1.source.type").String() != "text" || gjson.GetBytes(out, "messages.0.content.1.source.data").String() != "你好" || gjson.GetBytes(out, "messages.0.content.1.title").String() != "msg-1.md" || gjson.GetBytes(out, "messages.0.content.2.source.type").String() != "base64" {
		t.Fatalf("%s", out)
	}
	gem := `{"request":{"contents":[{"parts":[{"inlineData":{"mimeType":"text/markdown","data":"AA"}},{"inline_data":{"mime_type":"image/png","data":"AA"}}]}]}}`
	out, ok = FixUpstream("antigravity", []byte(gem), "")
	if !ok || gjson.GetBytes(out, "request.contents.0.parts.0.inlineData.mimeType").String() != "text/plain" || gjson.GetBytes(out, "request.contents.0.parts.1.inline_data.mime_type").String() != "image/png" {
		t.Fatalf("%s", out)
	}
	oa := `{"messages":[{"role":"user","content":[{"type":"text","text":"[message text in the attached file msg-2.md]"},{"type":"file","file":{"filename":"","file_data":"data:text/plain;base64,AA"}}]}]}`
	out, ok = FixUpstream("openai", []byte(oa), "")
	if !ok || gjson.GetBytes(out, "messages.0.content.1.file.filename").String() != "msg-2.md" {
		t.Fatalf("%s", out)
	}
	if !IsOurRequest([]byte(claude), "submit_reply", "") || IsOurRequest([]byte(`{"x":"submit_reply"}`), "submit_reply", "") {
		t.Fatal("IsOurRequest")
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
	out := runStream(t, NewStreamRestorer(FormatResponses, "submit_reply"), chunks)
	all := strings.Join(out, "")
	if strings.Contains(all, "submit_reply") || strings.Contains(all, "event: response.output_item.added\nevent:") {
		t.Fatalf("%q", all)
	}
	last, _ := parseChunk([]byte(out[len(out)-1]))
	if last[0].event != "response.completed" || gjson.Get(last[0].data, "response.output.0.content.0.text").String() != "x" {
		t.Fatalf("%q", out[len(out)-1])
	}
}
