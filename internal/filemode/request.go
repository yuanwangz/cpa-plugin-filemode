package filemode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// File is one message text of the new turn, as the read tool hands it out.
type File struct {
	Name string
	Text string
}

// RewriteResult reports what Rewrite did.
type RewriteResult struct {
	Body      []byte
	Rewritten bool   // tools injected (and possibly texts moved into files)
	Files     []File // the files of the turn, in order
	Reason    string // why nothing was done, when Rewritten is false
	// ToolChoiceNone: the client sent tool_choice none. The turn is rewritten
	// and the tool table is the same as usual (so the upstream prefix cache
	// holds), but the reply instruction is left out: the model cannot call any
	// tool on this request and answers in plain text.
	ToolChoiceNone bool
}

// SetModel sets the top-level "model" field of a request body.
func SetModel(body []byte, model string) []byte {
	if model == "" || !gjson.ValidBytes(body) {
		return body
	}
	if gjson.GetBytes(body, "model").String() == model {
		return body
	}
	out, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return body
	}
	return out
}

// Rewrite replaces the text of the new user turn of a client request by file
// names, adds right after the turn an assistant step that called the read tool
// and its result carrying the texts (as if the model had already read them),
// and injects the reply tool and the read tool. Unsupported formats and
// requests it should not touch come back unchanged with Rewritten=false.
func Rewrite(format string, body []byte, opt Options) RewriteResult {
	opt = opt.normalized()
	if !gjson.ValidBytes(body) {
		return RewriteResult{Body: body, Reason: "invalid json"}
	}
	switch format {
	case FormatResponses:
		return rewriteResponses(body, opt)
	case FormatChat:
		return rewriteChat(body, opt)
	case FormatClaude:
		return rewriteClaude(body, opt)
	default:
		return RewriteResult{Body: body, Reason: "unsupported format " + format}
	}
}

// ReadCallID is the call id of the n-th read call the plugin adds (n from 1).
// It is fixed, so the rewritten history is the same on every step of a tool
// loop and the upstream prefix cache holds.
func ReadCallID(n int) string { return fmt.Sprintf("call_filemode_read_%d", n) }

// ReadResult is what the read tool returns for the given arguments: the named
// file, or every file of the turn when no (known) file is named.
func ReadResult(files []File, args string) string {
	want := strings.TrimSpace(gjson.Get(args, "file").String())
	if want != "" {
		for _, f := range files {
			if f.Name == want {
				return f.Text
			}
		}
	}
	if len(files) == 1 {
		return files[0].Text
	}
	var b strings.Builder
	for i, f := range files {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "<file name=%q>\n%s\n</file>", f.Name, f.Text)
	}
	if len(files) == 0 {
		return "(no message files in this turn)"
	}
	return b.String()
}

func mustJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

func fileName(n int) string { return fmt.Sprintf("msg-%d.md", n) }

// turn finds the new user turn: the first run of user messages after the last
// reply (an item isReply marks), together with the skippable items inside the
// run. User messages that arrive later in the same tool loop (events, notes
// the client adds between steps) stay as they are, so the rewrite of the turn
// and the read call after it are the same on every step and the prefix cache
// holds. With no user message after the last reply it falls back to the last
// user message and the users right before it. The returned indices are ascending.
func turn(items []gjson.Result, isUser, isSkippable func(gjson.Result) bool, isReply func(i int) bool) []int {
	from := 0
	for i := len(items) - 1; i >= 0; i-- {
		if isReply(i) {
			from = i + 1
			break
		}
	}
	k := -1
	for i := from; i < len(items); i++ {
		if isUser(items[i]) {
			k = i
			break
		}
	}
	if k >= 0 {
		var idx []int
		for i := k; i < len(items) && (isUser(items[i]) || isSkippable(items[i])); i++ {
			if isUser(items[i]) {
				idx = append(idx, i)
			}
		}
		return idx
	}
	for i := len(items) - 1; i >= 0; i-- {
		if isUser(items[i]) {
			k = i
			break
		}
	}
	if k < 0 {
		return nil
	}
	start := k
	for start > 0 && (isUser(items[start-1]) || isSkippable(items[start-1])) {
		start--
	}
	var idx []int
	for i := start; i <= k; i++ {
		if isUser(items[i]) {
			idx = append(idx, i)
		}
	}
	return idx
}

// partsOf returns the content parts of a message, converting string content
// into one text part of the given type.
func partsOf(content gjson.Result, textType string) []string {
	if content.Type == gjson.String {
		return []string{mustJSON(map[string]any{"type": textType, "text": content.String()})}
	}
	var out []string
	if content.IsArray() {
		for _, p := range content.Array() {
			out = append(out, p.Raw)
		}
	}
	return out
}

// moveText rewrites the parts of one user message: its text parts become one
// placeholder text part naming the file, at the position of the first text
// part; all other parts keep their order. instr is appended to the placeholder
// (or, when there is no text, to the last text part or as a new one). It
// returns the new parts, the moved text, the cache_control that sat on the
// moved text (if any) and whether text was moved.
func moveText(o Options, parts []string, textTypes map[string]bool, textType string, name string, instr string) ([]string, string, string, bool) {
	var texts []string
	cache := ""
	first := -1
	for i, raw := range parts {
		p := gjson.Parse(raw)
		if textTypes[p.Get("type").String()] {
			if first < 0 {
				first = i
			}
			texts = append(texts, p.Get("text").String())
			if cc := p.Get("cache_control"); cc.Exists() {
				cache = cc.Raw
			}
		}
	}
	body := strings.Join(texts, "\n")
	if strings.TrimSpace(body) == "" {
		if instr == "" {
			return parts, "", "", false
		}
		out := append([]string(nil), parts...)
		for i := len(out) - 1; i >= 0; i-- {
			p := gjson.Parse(out[i])
			if textTypes[p.Get("type").String()] {
				out[i], _ = sjson.Set(out[i], "text", p.Get("text").String()+instr)
				return out, "", "", false
			}
		}
		out = append(out, mustJSON(map[string]any{"type": textType, "text": strings.TrimLeft(instr, "\n")}))
		return out, "", "", false
	}
	out := make([]string, 0, len(parts))
	for i, raw := range parts {
		if i == first {
			out = append(out, mustJSON(map[string]any{"type": textType, "text": o.placeholder(name) + instr}))
			continue
		}
		if textTypes[gjson.Get(raw, "type").String()] {
			continue
		}
		out = append(out, raw)
	}
	return out, body, cache, true
}

func joinArray(items []string) string { return "[" + strings.Join(items, ",") + "]" }

func replySchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"content": map[string]any{"type": "string", "description": contentArgDesc}},
		"required":   []string{"content"},
	}
}

func readSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"file": map[string]any{"type": "string", "description": fileArgDesc}},
	}
}

// insertAfter returns items with extra inserted after index k.
func insertAfter(items []string, k int, extra ...string) []string {
	out := make([]string, 0, len(items)+len(extra))
	out = append(out, items[:k+1]...)
	out = append(out, extra...)
	return append(out, items[k+1:]...)
}

// toolChoiceNone reports whether the client forbade tool calls on this request
// ("none" in OpenAI Chat and Responses, {"type":"none"} in Claude).
func toolChoiceNone(root gjson.Result) bool {
	tc := root.Get("tool_choice")
	if tc.Type == gjson.String {
		return tc.String() == "none"
	}
	return tc.Get("type").String() == "none"
}

// msgOptions are the options the turn is moved with: on a tool_choice none
// request the reply instruction is left out, since no tool can be called.
func msgOptions(o Options, none bool) Options {
	if none {
		o.noInstruction = true
	}
	return o
}

// clientUsesTool reports whether the client declared a tool of that name.
func clientUsesTool(root gjson.Result, names ...string) bool {
	for _, t := range root.Get("tools").Array() {
		for _, n := range names {
			if t.Get("name").String() == n || t.Get("function.name").String() == n {
				return true
			}
		}
	}
	return false
}

// moveTurn runs moveText over the user messages idx of items (whose content is
// at "content") and returns the new raw items, the files and the last cache_control.
func moveTurn(o Options, items []gjson.Result, idx []int, textTypes map[string]bool, textType string) ([]string, []File, string) {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Raw
	}
	var files []File
	cache := ""
	for j, i := range idx {
		instr := ""
		if j == len(idx)-1 {
			instr = o.instruction()
		}
		parts := partsOf(items[i].Get("content"), textType)
		name := fileName(len(files) + 1)
		np, text, cc, moved := moveText(o, parts, textTypes, textType, name, instr)
		if moved {
			files = append(files, File{Name: name, Text: text})
			if cc != "" {
				cache = cc
			}
		}
		out[i], _ = sjson.SetRaw(items[i].Raw, "content", joinArray(np))
	}
	return out, files, cache
}

// ---------- OpenAI Responses ----------

// ResponsesReadItems are the input items of one read call and its result.
func ResponsesReadItems(o Options, callID, args, result string) []string {
	return []string{
		mustJSON(map[string]any{"type": "function_call", "call_id": callID, "name": o.normalized().ReadTool, "arguments": args}),
		mustJSON(map[string]any{"type": "function_call_output", "call_id": callID, "output": result}),
	}
}

func rewriteResponses(body []byte, o Options) RewriteResult {
	root := gjson.ParseBytes(body)
	if clientUsesTool(root, o.ownTools()...) {
		return RewriteResult{Body: body, Reason: "tool name already used by client"}
	}
	none := toolChoiceNone(root)
	input := root.Get("input")
	var items []gjson.Result
	switch {
	case input.Type == gjson.String:
		items = []gjson.Result{gjson.Parse(mustJSON(map[string]any{"type": "message", "role": "user", "content": input.String()}))}
	case input.IsArray():
		items = input.Array()
	default:
		return RewriteResult{Body: body, Reason: "no input"}
	}
	isUser := func(it gjson.Result) bool {
		t := it.Get("type").String()
		return (t == "" || t == "message") && it.Get("role").String() == "user"
	}
	isSys := func(it gjson.Result) bool {
		t := it.Get("type").String()
		r := it.Get("role").String()
		return (t == "" || t == "message") && (r == "system" || r == "developer")
	}
	// a reply is an assistant message not followed by a tool call of the same
	// response (text the model wrote alongside its calls is a step of the loop)
	isReply := func(i int) bool {
		it := items[i]
		t := it.Get("type").String()
		if !((t == "" || t == "message") && it.Get("role").String() == "assistant") {
			return false
		}
		return i+1 >= len(items) || items[i+1].Get("type").String() != "function_call"
	}
	idx := turn(items, isUser, isSys, isReply)
	if len(idx) == 0 {
		return RewriteResult{Body: body, Reason: "no user message"}
	}
	out, files, _ := moveTurn(msgOptions(o, none), items, idx, map[string]bool{"input_text": true, "text": true}, "input_text")
	if len(files) > 0 {
		out = insertAfter(out, idx[len(idx)-1], ResponsesReadItems(o, ReadCallID(1), "{}", ReadResult(files, "{}"))...)
	}
	nb, err := sjson.SetRawBytes(body, "input", []byte(joinArray(out)))
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	for _, t := range responsesTools(o) {
		if nb, err = appendTool(nb, t); err != nil {
			return RewriteResult{Body: body, Reason: err.Error()}
		}
	}
	return RewriteResult{Body: nb, Rewritten: true, Files: files, ToolChoiceNone: none}
}

func appendTool(body []byte, tool string) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return sjson.SetRawBytes(body, "tools", []byte("["+tool+"]"))
	}
	return sjson.SetRawBytes(body, "tools.-1", []byte(tool))
}

// ---------- OpenAI Chat Completions ----------

// ChatReadMessages are the messages of one read call and its result.
func ChatReadMessages(o Options, callID, args, result string) []string {
	return []string{
		mustJSON(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": o.normalized().ReadTool, "arguments": args}}}}),
		mustJSON(map[string]any{"role": "tool", "tool_call_id": callID, "content": result}),
	}
}

func rewriteChat(body []byte, o Options) RewriteResult {
	root := gjson.ParseBytes(body)
	if clientUsesTool(root, o.ownTools()...) {
		return RewriteResult{Body: body, Reason: "tool name already used by client"}
	}
	none := toolChoiceNone(root)
	items := root.Get("messages").Array()
	isUser := func(m gjson.Result) bool { return m.Get("role").String() == "user" }
	isSys := func(m gjson.Result) bool {
		r := m.Get("role").String()
		return r == "system" || r == "developer"
	}
	// a reply is an assistant message without tool calls
	isReply := func(i int) bool {
		m := items[i]
		return m.Get("role").String() == "assistant" && len(m.Get("tool_calls").Array()) == 0
	}
	idx := turn(items, isUser, isSys, isReply)
	if len(idx) == 0 {
		return RewriteResult{Body: body, Reason: "no user message"}
	}
	out, files, _ := moveTurn(msgOptions(o, none), items, idx, map[string]bool{"text": true}, "text")
	if len(files) > 0 {
		out = insertAfter(out, idx[len(idx)-1], ChatReadMessages(o, ReadCallID(1), "{}", ReadResult(files, "{}"))...)
	}
	nb, err := sjson.SetRawBytes(body, "messages", []byte(joinArray(out)))
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	for _, t := range chatTools(o) {
		if nb, err = appendTool(nb, t); err != nil {
			return RewriteResult{Body: body, Reason: err.Error()}
		}
	}
	return RewriteResult{Body: nb, Rewritten: true, Files: files, ToolChoiceNone: none}
}

// ---------- Claude Messages ----------

// ClaudeReadMessages are the messages of one read call and its result. A
// cache_control (raw JSON, may be empty) goes on the result.
func ClaudeReadMessages(o Options, callID, args, result, cache string) []string {
	input := gjson.Parse(args)
	if !input.IsObject() {
		input = gjson.Parse("{}")
	}
	call := `{"role":"assistant","content":[{"type":"tool_use","id":"","name":"","input":{}}]}`
	call, _ = sjson.Set(call, "content.0.id", callID)
	call, _ = sjson.Set(call, "content.0.name", o.normalized().ReadTool)
	call, _ = sjson.SetRaw(call, "content.0.input", input.Raw)
	res := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":""}]}`
	res, _ = sjson.Set(res, "content.0.tool_use_id", callID)
	res, _ = sjson.Set(res, "content.0.content", result)
	if cache != "" {
		res, _ = sjson.SetRaw(res, "content.0.cache_control", cache)
	}
	return []string{call, res}
}

func rewriteClaude(body []byte, o Options) RewriteResult {
	root := gjson.ParseBytes(body)
	if clientUsesTool(root, o.ownTools()...) {
		return RewriteResult{Body: body, Reason: "tool name already used by client"}
	}
	none := toolChoiceNone(root)
	items := root.Get("messages").Array()
	// a user message holding only tool_result blocks is a tool-loop step, not a new message
	isUser := func(m gjson.Result) bool {
		if m.Get("role").String() != "user" {
			return false
		}
		c := m.Get("content")
		if c.Type == gjson.String {
			return true
		}
		human := false
		c.ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() != "tool_result" {
				human = true
				return false
			}
			return true
		})
		return human
	}
	isSys := func(gjson.Result) bool { return false }
	// a reply is an assistant message without tool_use blocks
	isReply := func(i int) bool {
		m := items[i]
		if m.Get("role").String() != "assistant" {
			return false
		}
		use := false
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() == "tool_use" {
				use = true
				return false
			}
			return true
		})
		return !use
	}
	idx := turn(items, isUser, isSys, isReply)
	if len(idx) == 0 {
		return RewriteResult{Body: body, Reason: "no user message"}
	}
	out, files, cache := moveTurn(msgOptions(o, none), items, idx, map[string]bool{"text": true}, "text")
	if len(files) > 0 {
		out = insertAfter(out, idx[len(idx)-1], ClaudeReadMessages(o, ReadCallID(1), "{}", ReadResult(files, "{}"), cache)...)
	}
	nb, err := sjson.SetRawBytes(body, "messages", []byte(joinArray(out)))
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	for _, t := range claudeTools(o) {
		if nb, err = appendTool(nb, t); err != nil {
			return RewriteResult{Body: body, Reason: err.Error()}
		}
	}
	return RewriteResult{Body: nb, Rewritten: true, Files: files, ToolChoiceNone: none}
}

// AppendReadCall adds one more read call and its result at the end of a
// rewritten request (in the client format), for when the model called the
// read tool itself. Unknown formats come back unchanged.
func AppendReadCall(format string, body []byte, o Options, callID, args string, files []File) []byte {
	if strings.TrimSpace(args) == "" || !gjson.Valid(args) {
		args = "{}"
	}
	result := ReadResult(files, args)
	var key string
	var extra []string
	switch format {
	case FormatResponses:
		key, extra = "input", ResponsesReadItems(o, callID, args, result)
	case FormatChat:
		key, extra = "messages", ChatReadMessages(o, callID, args, result)
	case FormatClaude:
		key, extra = "messages", ClaudeReadMessages(o, callID, args, result, "")
	default:
		return body
	}
	arr := gjson.GetBytes(body, key)
	if !arr.IsArray() {
		return body
	}
	var items []string
	for _, it := range arr.Array() {
		items = append(items, it.Raw)
	}
	items = append(items, extra...)
	out, err := sjson.SetRawBytes(body, key, []byte(joinArray(items)))
	if err != nil {
		return body
	}
	return out
}

// The injected tools (reply, then read) in each format. Built the same way on
// every request so the tool table is byte-identical and the prefix cache holds.
func responsesTools(o Options) []string {
	read := mustJSON(map[string]any{"type": "function", "name": o.ReadTool, "description": o.ReadDesc, "parameters": readSchema()})
	if o.NoReply {
		return []string{read}
	}
	return []string{mustJSON(map[string]any{"type": "function", "name": o.ToolName, "description": o.ToolDesc, "parameters": replySchema()}), read}
}

func chatTools(o Options) []string {
	read := mustJSON(map[string]any{"type": "function", "function": map[string]any{"name": o.ReadTool, "description": o.ReadDesc, "parameters": readSchema()}})
	if o.NoReply {
		return []string{read}
	}
	return []string{mustJSON(map[string]any{"type": "function", "function": map[string]any{"name": o.ToolName, "description": o.ToolDesc, "parameters": replySchema()}}), read}
}

func claudeTools(o Options) []string {
	read := mustJSON(map[string]any{"name": o.ReadTool, "description": o.ReadDesc, "input_schema": readSchema()})
	if o.NoReply {
		return []string{read}
	}
	return []string{mustJSON(map[string]any{"name": o.ToolName, "description": o.ToolDesc, "input_schema": replySchema()}), read}
}
