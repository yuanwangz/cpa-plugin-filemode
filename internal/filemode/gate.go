package filemode

import (
	"strings"

	"github.com/tidwall/gjson"
)

// GateHold is how many chunks a Gate holds back at most before it lets the
// stream through undecided (a long reasoning preamble should not leave the
// client without a byte).
const GateHold = 64

// Gate watches the start of a streaming response to tell whether the model's
// first output is a call of the read tool. The executor holds the chunks back
// until the gate decides: a stream that starts with a read call is dropped and
// the request is sent again with the call answered; any other stream is let
// through as it came.
type Gate struct {
	format string
	read   string
	n      int
}

// NewGate returns a gate for the client format and the read tool's name.
func NewGate(format, read string) *Gate { return &Gate{format: format, read: read} }

// Feed looks at one chunk. decided reports whether the gate has made up its
// mind; reread whether the stream opened with a read call.
func (g *Gate) Feed(chunk []byte) (decided, reread bool) {
	g.n++
	frames, _ := parseChunk(chunk)
	for _, f := range frames {
		d := strings.TrimSpace(f.data)
		if d == "[DONE]" {
			return true, false
		}
		if !gjson.Valid(d) {
			continue
		}
		if dec, rr := g.frame(gjson.Parse(d)); dec {
			return true, rr
		}
	}
	return g.n >= GateHold, false
}

func (g *Gate) frame(ev gjson.Result) (bool, bool) {
	switch g.format {
	case FormatChat:
		for _, ch := range ev.Get("choices").Array() {
			if c := ch.Get("delta.content"); c.Type == gjson.String && c.String() != "" {
				return true, false
			}
			for _, tc := range ch.Get("delta.tool_calls").Array() {
				if n := tc.Get("function.name").String(); n != "" {
					return true, n == g.read
				}
			}
			if ch.Get("finish_reason").String() != "" {
				return true, false
			}
		}
	case FormatResponses:
		switch ev.Get("type").String() {
		case "response.output_item.added":
			it := ev.Get("item")
			switch it.Get("type").String() {
			case "function_call":
				return true, it.Get("name").String() == g.read
			case "reasoning":
				return false, false
			}
			return true, false
		case "response.completed", "response.incomplete", "response.done", "response.failed", "error":
			return true, false
		}
	case FormatClaude:
		switch ev.Get("type").String() {
		case "content_block_start":
			cb := ev.Get("content_block")
			switch cb.Get("type").String() {
			case "tool_use":
				return true, cb.Get("name").String() == g.read
			case "thinking", "redacted_thinking":
				return false, false
			}
			return true, false
		case "message_delta", "message_stop", "error":
			return true, false
		}
	default:
		return true, false
	}
	return false, false
}
