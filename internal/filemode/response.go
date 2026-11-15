package filemode

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ReplyText extracts the reply from the reply tool's arguments. Arguments that
// are not a JSON object with a string "content" are returned as they are, so
// the client never sees the tool call even when the model botched the JSON.
func ReplyText(args string) string {
	a := strings.TrimSpace(args)
	if a == "" {
		return ""
	}
	if gjson.Valid(a) {
		r := gjson.Parse(a)
		if r.IsObject() {
			c := r.Get("content")
			if c.Type == gjson.String {
				return c.String()
			}
			if c.Exists() {
				return c.Raw
			}
		}
		if r.Type == gjson.String {
			return r.String()
		}
	}
	return args
}

// Tools names the two plugin tools the client never sees.
type Tools struct {
	Reply string // its call becomes assistant text
	Read  string // its call is dropped (the plugin answers it when it can)
}

func (t Tools) has(name string) bool { return name != "" && (name == t.Reply || name == t.Read) }

// text is the assistant text a plugin tool call stands for.
func (t Tools) text(name, args string) string {
	if name == t.Read {
		return ""
	}
	return ReplyText(args)
}

// Restore rewrites a non-streaming response in the given client format: every
// call of the reply tool becomes assistant text in its place, calls of the read
// tool go, other tool calls stay. It reports whether anything changed.
func Restore(format string, body []byte, tool Tools) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return body, false
	}
	switch format {
	case FormatResponses:
		return restoreResponses(body, tool)
	case FormatChat:
		return restoreChat(body, tool)
	case FormatClaude:
		return restoreClaude(body, tool)
	}
	return body, false
}

func messageItem(id, text string) string {
	return mustJSON(map[string]any{
		"id": id, "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}},
	})
}

func msgID(callID, itemID string) string {
	base := callID
	if base == "" {
		base = itemID
	}
	base = strings.TrimPrefix(strings.TrimPrefix(base, "call_"), "fc_")
	if base == "" {
		base = "filemode"
	}
	return "msg_" + base
}

// restoreResponseObject rewrites the output array of a Responses object.
func restoreResponseObject(obj string, tool Tools) (string, bool) {
	out := gjson.Get(obj, "output")
	if !out.IsArray() {
		return obj, false
	}
	changed := false
	items := []string{}
	for _, it := range out.Array() {
		if it.Get("type").String() == "function_call" && tool.has(it.Get("name").String()) {
			if it.Get("name").String() == tool.Reply {
				items = append(items, messageItem(msgID(it.Get("call_id").String(), it.Get("id").String()), ReplyText(it.Get("arguments").String())))
			}
			changed = true
			continue
		}
		items = append(items, it.Raw)
	}
	if !changed {
		return obj, false
	}
	res, err := sjson.SetRaw(obj, "output", joinArray(items))
	if err != nil {
		return obj, false
	}
	if ot := gjson.Get(res, "output_text"); ot.Exists() {
		var b strings.Builder
		for _, it := range gjson.Get(res, "output").Array() {
			if it.Get("type").String() == "message" {
				for _, c := range it.Get("content").Array() {
					if c.Get("type").String() == "output_text" {
						b.WriteString(c.Get("text").String())
					}
				}
			}
		}
		res, _ = sjson.Set(res, "output_text", b.String())
	}
	return res, true
}

// stripTool removes the reply tool from a Responses object's echoed "tools"
// list, so the client does not see a tool it never declared.
func stripTool(obj string, tool Tools) (string, bool) {
	tools := gjson.Get(obj, "tools")
	if !tools.IsArray() {
		return obj, false
	}
	var keep []string
	removed := false
	for _, t := range tools.Array() {
		if tool.has(t.Get("name").String()) || tool.has(t.Get("function.name").String()) {
			removed = true
			continue
		}
		keep = append(keep, t.Raw)
	}
	if !removed {
		return obj, false
	}
	out, err := sjson.SetRaw(obj, "tools", joinArray(keep))
	if err != nil {
		return obj, false
	}
	return out, true
}

func restoreResponses(body []byte, tool Tools) ([]byte, bool) {
	res, changed := restoreResponseObject(string(body), tool)
	res, stripped := stripTool(res, tool)
	return []byte(res), changed || stripped
}

func restoreChat(body []byte, tool Tools) ([]byte, bool) {
	res := string(body)
	changed := false
	for ci, ch := range gjson.Get(res, "choices").Array() {
		calls := ch.Get("message.tool_calls")
		if !calls.IsArray() {
			continue
		}
		var keep []string
		var texts []string
		dropped := false
		for _, c := range calls.Array() {
			if n := c.Get("function.name").String(); tool.has(n) {
				dropped = true
				if t := tool.text(n, c.Get("function.arguments").String()); n == tool.Reply {
					texts = append(texts, t)
				}
				continue
			}
			keep = append(keep, c.Raw)
		}
		if !dropped {
			continue
		}
		changed = true
		prefix := "choices." + itoa(ci) + ".message."
		content := ch.Get("message.content").String()
		all := texts
		if strings.TrimSpace(content) != "" {
			all = append([]string{content}, texts...)
		}
		if len(all) > 0 || ch.Get("message.content").Exists() {
			res, _ = sjson.Set(res, prefix+"content", strings.Join(all, "\n\n"))
		}
		if len(keep) == 0 {
			res, _ = sjson.Delete(res, prefix+"tool_calls")
			if ch.Get("finish_reason").String() == "tool_calls" {
				res, _ = sjson.Set(res, "choices."+itoa(ci)+".finish_reason", "stop")
			}
		} else {
			res, _ = sjson.SetRaw(res, prefix+"tool_calls", joinArray(keep))
		}
	}
	return []byte(res), changed
}

func restoreClaude(body []byte, tool Tools) ([]byte, bool) {
	content := gjson.GetBytes(body, "content")
	if !content.IsArray() {
		return body, false
	}
	changed := false
	other := false
	var blocks []string
	for _, b := range content.Array() {
		if b.Get("type").String() == "tool_use" {
			if n := b.Get("name").String(); tool.has(n) {
				if n == tool.Reply {
					blocks = append(blocks, mustJSON(map[string]any{"type": "text", "text": ReplyText(b.Get("input").Raw)}))
				}
				changed = true
				continue
			}
			other = true
		}
		blocks = append(blocks, b.Raw)
	}
	if !changed {
		return body, false
	}
	out, err := sjson.SetRawBytes(body, "content", []byte(joinArray(blocks)))
	if err != nil {
		return body, false
	}
	if !other && gjson.GetBytes(out, "stop_reason").String() == "tool_use" {
		out, _ = sjson.SetBytes(out, "stop_reason", "end_turn")
	}
	return out, true
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

// Call is one tool call of a response.
type Call struct {
	ID   string
	Args string
}

// ReadCalls returns the read-tool calls of a non-streaming response when they
// are all the model did (no text, no other call): the plugin then answers them
// and asks again. Otherwise it returns nil.
func ReadCalls(format string, body []byte, read string) []Call {
	if read == "" || !gjson.ValidBytes(body) {
		return nil
	}
	var calls []Call
	switch format {
	case FormatResponses:
		for _, it := range gjson.GetBytes(body, "output").Array() {
			switch it.Get("type").String() {
			case "reasoning":
			case "function_call":
				if it.Get("name").String() != read {
					return nil
				}
				calls = append(calls, Call{ID: it.Get("call_id").String(), Args: it.Get("arguments").String()})
			case "message":
				for _, c := range it.Get("content").Array() {
					if strings.TrimSpace(c.Get("text").String()) != "" {
						return nil
					}
				}
			default:
				return nil
			}
		}
	case FormatChat:
		chs := gjson.GetBytes(body, "choices").Array()
		if len(chs) != 1 || strings.TrimSpace(chs[0].Get("message.content").String()) != "" {
			return nil
		}
		for _, c := range chs[0].Get("message.tool_calls").Array() {
			if c.Get("function.name").String() != read {
				return nil
			}
			calls = append(calls, Call{ID: c.Get("id").String(), Args: c.Get("function.arguments").String()})
		}
	case FormatClaude:
		for _, b := range gjson.GetBytes(body, "content").Array() {
			switch b.Get("type").String() {
			case "thinking", "redacted_thinking":
			case "tool_use":
				if b.Get("name").String() != read {
					return nil
				}
				calls = append(calls, Call{ID: b.Get("id").String(), Args: b.Get("input").Raw})
			case "text":
				if strings.TrimSpace(b.Get("text").String()) != "" {
					return nil
				}
			default:
				return nil
			}
		}
	}
	return calls
}
