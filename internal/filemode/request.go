package filemode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RewriteResult reports what Rewrite did.
type RewriteResult struct {
	Body      []byte
	Rewritten bool   // tool injected (and possibly files attached)
	Files     int    // number of .md files attached
	Reason    string // why nothing was done, when Rewritten is false
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

// Rewrite turns the new user turn of a client request into attached .md files
// and injects the reply tool. Unsupported formats and requests it should not
// touch come back unchanged with Rewritten=false.
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

func dataURL(text string) string {
	return "data:text/markdown;base64," + base64.StdEncoding.EncodeToString([]byte(text))
}

func mustJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

func fileName(n int) string { return fmt.Sprintf("msg-%d.md", n) }

// turn finds the new user turn: the last user message k, and the user messages
// before it back to the previous non-user, non-system item. isUser and
// isSkippable classify items; the returned indices are ascending.
func turn(items []gjson.Result, isUser, isSkippable func(gjson.Result) bool) []int {
	k := -1
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

// moveText rewrites the parts of one user message. textTypes are the part types
// treated as message text. mkFile builds the file part(s) for the moved text.
// When the text is moved, the placeholder text part and the file part take the
// position of the first text part; all other parts keep their order.
func moveText(o Options, parts []string, textTypes map[string]bool, textType string, name string, instr string, mkFile func(name, text string, textParts []gjson.Result) []string) ([]string, bool) {
	var texts []string
	var textParts []gjson.Result
	first := -1
	for i, raw := range parts {
		p := gjson.Parse(raw)
		if textTypes[p.Get("type").String()] {
			if first < 0 {
				first = i
			}
			texts = append(texts, p.Get("text").String())
			textParts = append(textParts, p)
		}
	}
	body := strings.Join(texts, "\n")
	if o.InlineText || strings.TrimSpace(body) == "" {
		if instr == "" {
			return parts, false
		}
		out := append([]string(nil), parts...)
		// append the instruction to the last text part, or add one
		for i := len(out) - 1; i >= 0; i-- {
			p := gjson.Parse(out[i])
			if textTypes[p.Get("type").String()] {
				out[i], _ = sjson.Set(out[i], "text", p.Get("text").String()+instr)
				return out, false
			}
		}
		out = append(out, mustJSON(map[string]any{"type": textType, "text": strings.TrimLeft(instr, "\n")}))
		return out, false
	}
	out := make([]string, 0, len(parts)+1)
	for i, raw := range parts {
		if i == first {
			out = append(out, mustJSON(map[string]any{"type": textType, "text": o.placeholder(name) + instr}))
			out = append(out, mkFile(name, body, textParts)...)
			continue
		}
		if textTypes[gjson.Get(raw, "type").String()] {
			continue
		}
		out = append(out, raw)
	}
	return out, true
}

func joinArray(items []string) string { return "[" + strings.Join(items, ",") + "]" }

func toolSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"content": map[string]any{"type": "string", "description": contentArgDesc}},
		"required":   []string{"content"},
	}
}

// ---------- OpenAI Responses ----------

func rewriteResponses(body []byte, o Options) RewriteResult {
	root := gjson.ParseBytes(body)
	if tc := root.Get("tool_choice"); tc.Type == gjson.String && tc.String() == "none" {
		return RewriteResult{Body: body, Reason: "tool_choice none"}
	}
	for _, t := range root.Get("tools").Array() {
		if t.Get("name").String() == o.ToolName || t.Get("function.name").String() == o.ToolName {
			return RewriteResult{Body: body, Reason: "tool name already used by client"}
		}
	}
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
	idx := turn(items, isUser, isSys)
	if len(idx) == 0 {
		return RewriteResult{Body: body, Reason: "no user message"}
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Raw
	}
	files := 0
	textTypes := map[string]bool{"input_text": true, "text": true}
	for j, i := range idx {
		instr := ""
		if j == len(idx)-1 {
			instr = o.instruction()
		}
		parts := partsOf(items[i].Get("content"), "input_text")
		name := fileName(files + 1)
		np, moved := moveText(o, parts, textTypes, "input_text", name, instr, func(name, text string, _ []gjson.Result) []string {
			return []string{mustJSON(map[string]any{"type": "input_file", "filename": name, "file_data": dataURL(text)})}
		})
		if moved {
			files++
		}
		out[i], _ = sjson.SetRaw(items[i].Raw, "content", joinArray(np))
	}
	nb, err := sjson.SetRawBytes(body, "input", []byte(joinArray(out)))
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	tool := mustJSON(map[string]any{"type": "function", "name": o.ToolName, "description": o.ToolDesc, "parameters": toolSchema()})
	nb, err = appendTool(nb, tool)
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	return RewriteResult{Body: nb, Rewritten: true, Files: files}
}

func appendTool(body []byte, tool string) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return sjson.SetRawBytes(body, "tools", []byte("["+tool+"]"))
	}
	return sjson.SetRawBytes(body, "tools.-1", []byte(tool))
}

// ---------- OpenAI Chat Completions ----------

func rewriteChat(body []byte, o Options) RewriteResult {
	root := gjson.ParseBytes(body)
	if tc := root.Get("tool_choice"); tc.Type == gjson.String && tc.String() == "none" {
		return RewriteResult{Body: body, Reason: "tool_choice none"}
	}
	for _, t := range root.Get("tools").Array() {
		if t.Get("function.name").String() == o.ToolName {
			return RewriteResult{Body: body, Reason: "tool name already used by client"}
		}
	}
	items := root.Get("messages").Array()
	isUser := func(m gjson.Result) bool { return m.Get("role").String() == "user" }
	isSys := func(m gjson.Result) bool {
		r := m.Get("role").String()
		return r == "system" || r == "developer"
	}
	idx := turn(items, isUser, isSys)
	if len(idx) == 0 {
		return RewriteResult{Body: body, Reason: "no user message"}
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Raw
	}
	files := 0
	textTypes := map[string]bool{"text": true}
	for j, i := range idx {
		instr := ""
		if j == len(idx)-1 {
			instr = o.instruction()
		}
		parts := partsOf(items[i].Get("content"), "text")
		name := fileName(files + 1)
		np, moved := moveText(o, parts, textTypes, "text", name, instr, func(name, text string, _ []gjson.Result) []string {
			return []string{mustJSON(map[string]any{"type": "file", "file": map[string]any{"filename": name, "file_data": dataURL(text)}})}
		})
		if moved {
			files++
		}
		out[i], _ = sjson.SetRaw(items[i].Raw, "content", joinArray(np))
	}
	nb, err := sjson.SetRawBytes(body, "messages", []byte(joinArray(out)))
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	tool := mustJSON(map[string]any{"type": "function", "function": map[string]any{"name": o.ToolName, "description": o.ToolDesc, "parameters": toolSchema()}})
	nb, err = appendTool(nb, tool)
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	return RewriteResult{Body: nb, Rewritten: true, Files: files}
}

// ---------- Claude Messages ----------

func rewriteClaude(body []byte, o Options) RewriteResult {
	root := gjson.ParseBytes(body)
	if root.Get("tool_choice.type").String() == "none" {
		return RewriteResult{Body: body, Reason: "tool_choice none"}
	}
	for _, t := range root.Get("tools").Array() {
		if t.Get("name").String() == o.ToolName {
			return RewriteResult{Body: body, Reason: "tool name already used by client"}
		}
	}
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
	idx := turn(items, isUser, isSys)
	if len(idx) == 0 {
		return RewriteResult{Body: body, Reason: "no user message"}
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Raw
	}
	files := 0
	textTypes := map[string]bool{"text": true}
	for j, i := range idx {
		instr := ""
		if j == len(idx)-1 {
			instr = o.instruction()
		}
		parts := partsOf(items[i].Get("content"), "text")
		name := fileName(files + 1)
		np, moved := moveText(o, parts, textTypes, "text", name, instr, func(name, text string, tps []gjson.Result) []string {
			// base64 text/plain: the shape every built-in Claude-source translator
			// carries (OpenAI file part, Gemini inline data); a Claude upstream gets
			// it turned into a text-source document by the upstream normalizer.
			doc := `{"type":"document","source":{"type":"base64","media_type":"text/plain","data":""},"title":""}`
			doc, _ = sjson.Set(doc, "source.data", base64.StdEncoding.EncodeToString([]byte(text)))
			doc, _ = sjson.Set(doc, "title", name)
			// keep a prompt-cache breakpoint that sat on the moved text
			for k := len(tps) - 1; k >= 0; k-- {
				if cc := tps[k].Get("cache_control"); cc.Exists() {
					doc, _ = sjson.SetRaw(doc, "cache_control", cc.Raw)
					break
				}
			}
			return []string{doc}
		})
		if moved {
			files++
		}
		out[i], _ = sjson.SetRaw(items[i].Raw, "content", joinArray(np))
	}
	nb, err := sjson.SetRawBytes(body, "messages", []byte(joinArray(out)))
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	tool := mustJSON(map[string]any{"name": o.ToolName, "description": o.ToolDesc, "input_schema": toolSchema()})
	nb, err = appendTool(nb, tool)
	if err != nil {
		return RewriteResult{Body: body, Reason: err.Error()}
	}
	return RewriteResult{Body: nb, Rewritten: true, Files: files}
}
