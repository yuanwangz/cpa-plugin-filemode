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

// Restore rewrites a non-streaming response in the given client format: every
// call of the reply tool becomes assistant text in its place, other tool calls
// stay. It reports whether anything changed.
func Restore(format string, body []byte, tool string) ([]byte, bool) {
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
func restoreResponseObject(obj string, tool string) (string, bool) {
	out := gjson.Get(obj, "output")
	if !out.IsArray() {
		return obj, false
	}
	changed := false
	items := []string{}
	for _, it := range out.Array() {
		if it.Get("type").String() == "function_call" && it.Get("name").String() == tool {
			items = append(items, messageItem(msgID(it.Get("call_id").String(), it.Get("id").String()), ReplyText(it.Get("arguments").String())))
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
func stripTool(obj string, tool string) (string, bool) {
	tools := gjson.Get(obj, "tools")
	if !tools.IsArray() {
		return obj, false
	}
	var keep []string
	removed := false
	for _, t := range tools.Array() {
		if t.Get("name").String() == tool || t.Get("function.name").String() == tool {
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

func restoreResponses(body []byte, tool string) ([]byte, bool) {
	res, changed := restoreResponseObject(string(body), tool)
	res, stripped := stripTool(res, tool)
	return []byte(res), changed || stripped
}

func restoreChat(body []byte, tool string) ([]byte, bool) {
	res := string(body)
	changed := false
	for ci, ch := range gjson.Get(res, "choices").Array() {
		calls := ch.Get("message.tool_calls")
		if !calls.IsArray() {
			continue
		}
		var keep []string
		var texts []string
		for _, c := range calls.Array() {
			if c.Get("function.name").String() == tool {
				texts = append(texts, ReplyText(c.Get("function.arguments").String()))
				continue
			}
			keep = append(keep, c.Raw)
		}
		if len(texts) == 0 {
			continue
		}
		changed = true
		prefix := "choices." + itoa(ci) + ".message."
		content := ch.Get("message.content").String()
		all := texts
		if strings.TrimSpace(content) != "" {
			all = append([]string{content}, texts...)
		}
		res, _ = sjson.Set(res, prefix+"content", strings.Join(all, "\n\n"))
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

func restoreClaude(body []byte, tool string) ([]byte, bool) {
	content := gjson.GetBytes(body, "content")
	if !content.IsArray() {
		return body, false
	}
	changed := false
	other := false
	var blocks []string
	for _, b := range content.Array() {
		if b.Get("type").String() == "tool_use" {
			if b.Get("name").String() == tool {
				blocks = append(blocks, mustJSON(map[string]any{"type": "text", "text": ReplyText(b.Get("input").Raw)}))
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
