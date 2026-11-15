package filemode

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StreamRestorer rewrites a streaming response chunk by chunk. Chunks are in
// the host's framing for the client format. Process returns the chunks to emit
// in place of the input chunk (possibly none); Finish returns what is still
// pending when the upstream stream ends.
type StreamRestorer interface {
	Process(chunk []byte) [][]byte
	Finish() [][]byte
}

// NewStreamRestorer returns a restorer for the format, or a pass-through one.
func NewStreamRestorer(format string, tool Tools) StreamRestorer {
	switch format {
	case FormatResponses:
		return &responsesStream{tool: tool, calls: map[int64]*respCall{}, byItem: map[string]*respCall{}}
	case FormatChat:
		return &chatStream{tool: tool, choices: map[int64]*chatChoice{}}
	case FormatClaude:
		return &claudeStream{tool: tool, blocks: map[int64]*claudeBlock{}}
	}
	return passStream{}
}

type passStream struct{}

func (passStream) Process(c []byte) [][]byte { return [][]byte{c} }
func (passStream) Finish() [][]byte          { return nil }

// framer carries the per-stream framing state shared by the restorers.
type framer struct {
	style chunkStyle
	held  []byte // an "event:" line whose "data:" line arrives in the next chunk
}

// process runs fn over every frame of chunk. fn returns the frames to emit for
// one input frame and whether it changed anything. Unchanged chunks are passed
// through byte for byte. Some upstream paths (Codex) deliver the "event:" line
// and the "data:" line of one SSE event as separate chunks; such an event line
// is held back and joined with the chunk that follows.
func (fr *framer) process(chunk []byte, fn func(f frame) ([]frame, bool)) [][]byte {
	if len(fr.held) > 0 {
		joined := append([]byte(nil), fr.held...)
		if joined[len(joined)-1] != '\n' {
			joined = append(joined, '\n')
		}
		chunk = append(joined, chunk...)
		fr.held = nil
	}
	frames, st := parseChunk(chunk)
	if len(frames) == 0 {
		return [][]byte{chunk}
	}
	eventOnly := true
	for _, f := range frames {
		if f.hasData {
			eventOnly = false
		}
	}
	if eventOnly {
		fr.held = append([]byte(nil), chunk...)
		return nil
	}
	if st.known {
		fr.style = st
	}
	var out []frame
	changed := false
	for _, f := range frames {
		res, ch := fn(f)
		if ch {
			changed = true
		}
		out = append(out, res...)
	}
	if !changed {
		return [][]byte{chunk}
	}
	chunks := make([][]byte, 0, len(out))
	for _, f := range out {
		chunks = append(chunks, f.encode(fr.style))
	}
	return chunks
}

// flushHeld returns a held event line that never got its data line.
func (fr *framer) flushHeld() [][]byte {
	if len(fr.held) == 0 {
		return nil
	}
	h := fr.held
	fr.held = nil
	return [][]byte{h}
}

// ---------- Chat Completions ----------

type chatCall struct {
	suppressed bool
	silent     bool // a read-tool call: dropped, no text
	client     int64
	args       strings.Builder
}

type chatChoice struct {
	calls   map[int64]*chatCall
	next    int64
	order   []int64
	emitted bool
	other   bool
}

type chatStream struct {
	framer
	tool    Tools
	choices map[int64]*chatChoice
	last    string // last chunk JSON, template for synthesized chunks
}

func (s *chatStream) choice(i int64) *chatChoice {
	c := s.choices[i]
	if c == nil {
		c = &chatChoice{calls: map[int64]*chatCall{}}
		s.choices[i] = c
	}
	return c
}

func (c *chatChoice) pending() (string, bool) {
	if c.emitted {
		return "", false
	}
	var texts []string
	for _, k := range c.order {
		if cl := c.calls[k]; cl.suppressed && !cl.silent {
			texts = append(texts, ReplyText(cl.args.String()))
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n\n"), true
}

func (s *chatStream) Process(chunk []byte) [][]byte {
	return s.framer.process(chunk, s.frame)
}

func (s *chatStream) frame(f frame) ([]frame, bool) {
	d := strings.TrimSpace(f.data)
	if d == "[DONE]" {
		extra := s.flush()
		if len(extra) == 0 {
			return []frame{f}, false
		}
		return append(extra, f), true
	}
	if !gjson.Valid(d) {
		return []frame{f}, false
	}
	s.last = d
	out := d
	changed := false
	drop := false
	for ci, ch := range gjson.Get(d, "choices").Array() {
		idx := ch.Get("index").Int()
		c := s.choice(idx)
		p := "choices." + itoa(ci)
		if tcs := ch.Get("delta.tool_calls"); tcs.IsArray() {
			var keep []string
			for pos, tc := range tcs.Array() {
				ti := int64(pos)
				if v := tc.Get("index"); v.Exists() {
					ti = v.Int()
				}
				cl := c.calls[ti]
				if cl == nil {
					cl = &chatCall{}
					c.calls[ti] = cl
					c.order = append(c.order, ti)
					if n := tc.Get("function.name").String(); s.tool.has(n) {
						cl.suppressed = true
						cl.silent = n == s.tool.Read
					} else {
						cl.client = c.next
						c.next++
						c.other = true
					}
				}
				if cl.suppressed {
					cl.args.WriteString(tc.Get("function.arguments").String())
					changed = true
					continue
				}
				raw := tc.Raw
				if cl.client != ti {
					raw, _ = sjson.Set(raw, "index", cl.client)
					changed = true
				}
				keep = append(keep, raw)
			}
			if len(keep) == 0 {
				out, _ = sjson.Delete(out, p+".delta.tool_calls")
			} else {
				out, _ = sjson.SetRaw(out, p+".delta.tool_calls", joinArray(keep))
			}
		}
		if fr := ch.Get("finish_reason"); fr.Exists() && fr.Type != gjson.Null {
			if text, ok := c.pending(); ok {
				prev := gjson.Get(out, p+".delta.content").String()
				out, _ = sjson.Set(out, p+".delta.content", prev+text)
				c.emitted = true
				changed = true
				if fr.String() == "tool_calls" && !c.other {
					out, _ = sjson.Set(out, p+".finish_reason", "stop")
				}
			}
		}
	}
	if !changed {
		return []frame{f}, false
	}
	// drop chunks that only carried the suppressed call
	chs := gjson.Get(out, "choices").Array()
	if len(chs) > 0 && !gjson.Get(out, "usage").Exists() {
		empty := true
		for _, ch := range chs {
			delta := ch.Get("delta")
			if (delta.Exists() && len(delta.Map()) > 0) || (ch.Get("finish_reason").Exists() && ch.Get("finish_reason").Type != gjson.Null) {
				empty = false
			}
		}
		drop = empty
	}
	if drop {
		return nil, true
	}
	return []frame{{event: f.event, data: out, raw: f.raw}}, true
}

// flush emits pending reply text for choices that never got a finish_reason.
func (s *chatStream) flush() []frame {
	var out []frame
	for idx, c := range s.choices {
		text, ok := c.pending()
		if !ok {
			continue
		}
		c.emitted = true
		ch := `{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`
		for _, k := range []string{"id", "created", "model", "system_fingerprint"} {
			if v := gjson.Get(s.last, k); v.Exists() {
				ch, _ = sjson.SetRaw(ch, k, v.Raw)
			}
		}
		ch, _ = sjson.Set(ch, "choices.0.index", idx)
		ch, _ = sjson.Set(ch, "choices.0.delta.content", text)
		out = append(out, frame{data: ch, raw: s.style.raw})
	}
	return out
}

func (s *chatStream) Finish() [][]byte {
	out := s.flushHeld()
	for _, f := range s.flush() {
		out = append(out, f.encode(s.style))
	}
	return out
}

// ---------- OpenAI Responses ----------

type respCall struct {
	outIndex int64
	itemID   string
	callID   string
	args     strings.Builder
	final    *string
	done     bool
	silent   bool // a read-tool call: dropped, no text
}

type responsesStream struct {
	framer
	tool     Tools
	calls    map[int64]*respCall
	byItem   map[string]*respCall
	seq      int64
	renumber bool
}

func (s *responsesStream) Process(chunk []byte) [][]byte {
	return s.framer.process(chunk, s.frame)
}

func (s *responsesStream) lookup(ev gjson.Result) *respCall {
	if id := ev.Get("item_id").String(); id != "" {
		if c := s.byItem[id]; c != nil {
			return c
		}
	}
	if oi := ev.Get("output_index"); oi.Exists() {
		return s.calls[oi.Int()]
	}
	return nil
}

// stamp sets the sequence number of an outgoing event.
func (s *responsesStream) stamp(data string) (string, bool) {
	sn := gjson.Get(data, "sequence_number")
	if !s.renumber {
		if sn.Exists() {
			s.seq = sn.Int()
		}
		return data, false
	}
	if !sn.Exists() {
		return data, false
	}
	s.seq++
	out, _ := sjson.Set(data, "sequence_number", s.seq)
	return out, true
}

func (s *responsesStream) emit(f frame, data string) frame {
	typ := gjson.Get(data, "type").String()
	ev := f.event
	if ev != "" {
		ev = typ
	}
	return frame{event: ev, data: data, raw: f.raw}
}

func (s *responsesStream) frame(f frame) ([]frame, bool) {
	d := strings.TrimSpace(f.data)
	if !gjson.Valid(d) {
		return []frame{f}, false
	}
	ev := gjson.Parse(d)
	typ := ev.Get("type").String()
	switch typ {
	case "response.output_item.added":
		it := ev.Get("item")
		if it.Get("type").String() == "function_call" && s.tool.has(it.Get("name").String()) {
			c := &respCall{outIndex: ev.Get("output_index").Int(), itemID: it.Get("id").String(), callID: it.Get("call_id").String(), silent: it.Get("name").String() == s.tool.Read}
			s.calls[c.outIndex] = c
			if c.itemID != "" {
				s.byItem[c.itemID] = c
			}
			s.renumber = true
			return nil, true
		}
	case "response.function_call_arguments.delta":
		if c := s.lookup(ev); c != nil {
			c.args.WriteString(ev.Get("delta").String())
			return nil, true
		}
	case "response.function_call_arguments.done":
		if c := s.lookup(ev); c != nil {
			a := ev.Get("arguments").String()
			c.final = &a
			return nil, true
		}
	case "response.output_item.done":
		it := ev.Get("item")
		if c := s.calls[ev.Get("output_index").Int()]; c != nil && it.Get("type").String() == "function_call" && s.tool.has(it.Get("name").String()) {
			a := it.Get("arguments").String()
			c.final = &a
			return s.synth(f, c), true
		}
	case "response.completed", "response.incomplete", "response.done", "response.failed":
		var extra []frame
		for _, c := range s.calls {
			if !c.done {
				extra = append(extra, s.synth(f, c)...)
			}
		}
		out := d
		changed := len(extra) > 0
		if r := ev.Get("response"); r.Exists() {
			nr, ok := restoreResponseObject(r.Raw, s.tool)
			nr, ok2 := stripTool(nr, s.tool)
			if ok || ok2 {
				out, _ = sjson.SetRaw(out, "response", nr)
				changed = true
			}
		}
		out, st := s.stamp(out)
		if !changed && !st {
			return []frame{f}, false
		}
		return append(extra, s.emit(f, out)), true
	}
	out := d
	changed := false
	if r := ev.Get("response"); r.Exists() {
		if nr, ok := stripTool(r.Raw, s.tool); ok {
			out, _ = sjson.SetRaw(out, "response", nr)
			changed = true
		}
	}
	out, st := s.stamp(out)
	if !st && !changed {
		return []frame{f}, false
	}
	return []frame{s.emit(f, out)}, true
}

// synth produces the events of a completed assistant message carrying the
// reply text, at the output index of the replaced call.
func (s *responsesStream) synth(f frame, c *respCall) []frame {
	c.done = true
	args := c.args.String()
	if c.final != nil && *c.final != "" {
		args = *c.final
	}
	text := ReplyText(args)
	if c.silent {
		text = ""
	}
	id := msgID(c.callID, c.itemID)
	oi := c.outIndex
	part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}
	events := []map[string]any{
		{"type": "response.output_item.added", "output_index": oi, "item": map[string]any{"id": id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}},
		{"type": "response.content_part.added", "item_id": id, "output_index": oi, "content_index": 0, "part": part},
		{"type": "response.output_text.delta", "item_id": id, "output_index": oi, "content_index": 0, "delta": text, "logprobs": []any{}},
		{"type": "response.output_text.done", "item_id": id, "output_index": oi, "content_index": 0, "text": text, "logprobs": []any{}},
		{"type": "response.content_part.done", "item_id": id, "output_index": oi, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}},
	}
	var out []frame
	for _, e := range events {
		s.seq++
		e["sequence_number"] = s.seq
		data := mustJSON(e)
		out = append(out, s.emit(f, data))
	}
	s.seq++
	done := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":null}`
	done, _ = sjson.Set(done, "sequence_number", s.seq)
	done, _ = sjson.Set(done, "output_index", oi)
	done, _ = sjson.SetRaw(done, "item", messageItem(id, text))
	out = append(out, s.emit(f, done))
	return out
}

func (s *responsesStream) Finish() [][]byte {
	out := s.flushHeld()
	for _, c := range s.calls {
		if !c.done {
			for _, fr := range s.synth(frame{event: "x"}, c) {
				out = append(out, fr.encode(s.style))
			}
		}
	}
	return out
}

// ---------- Claude Messages ----------

type claudeBlock struct {
	args   strings.Builder
	done   bool
	silent bool // a read-tool call: dropped, no text
}

type claudeStream struct {
	framer
	tool   Tools
	blocks map[int64]*claudeBlock
	other  bool
}

func (s *claudeStream) Process(chunk []byte) [][]byte {
	return s.framer.process(chunk, s.frame)
}

func claudeFrame(f frame, data string) frame {
	ev := ""
	if f.event != "" || !f.raw {
		ev = gjson.Get(data, "type").String()
	}
	return frame{event: ev, data: data, raw: f.raw}
}

func (s *claudeStream) frame(f frame) ([]frame, bool) {
	d := strings.TrimSpace(f.data)
	if !gjson.Valid(d) {
		return []frame{f}, false
	}
	ev := gjson.Parse(d)
	switch ev.Get("type").String() {
	case "content_block_start":
		cb := ev.Get("content_block")
		if cb.Get("type").String() == "tool_use" {
			if n := cb.Get("name").String(); s.tool.has(n) {
				s.blocks[ev.Get("index").Int()] = &claudeBlock{silent: n == s.tool.Read}
				return nil, true
			}
			s.other = true
		}
	case "content_block_delta":
		if b := s.blocks[ev.Get("index").Int()]; b != nil {
			b.args.WriteString(ev.Get("delta.partial_json").String())
			return nil, true
		}
	case "content_block_stop":
		idx := ev.Get("index").Int()
		if b := s.blocks[idx]; b != nil {
			return s.synth(f, idx, b), true
		}
	case "message_delta":
		if len(s.blocks) > 0 && !s.other && ev.Get("delta.stop_reason").String() == "tool_use" {
			var extra []frame
			for idx, b := range s.blocks {
				if !b.done {
					extra = append(extra, s.synth(f, idx, b)...)
				}
			}
			out, _ := sjson.Set(d, "delta.stop_reason", "end_turn")
			return append(extra, claudeFrame(f, out)), true
		}
	}
	return []frame{f}, false
}

func (s *claudeStream) synth(f frame, idx int64, b *claudeBlock) []frame {
	b.done = true
	text := ReplyText(b.args.String())
	if b.silent {
		text = ""
	}
	evs := []map[string]any{
		{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "text", "text": ""}},
		{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "text_delta", "text": text}},
		{"type": "content_block_stop", "index": idx},
	}
	var out []frame
	for _, e := range evs {
		out = append(out, claudeFrame(f, mustJSON(e)))
	}
	return out
}

func (s *claudeStream) Finish() [][]byte {
	out := s.flushHeld()
	for idx, b := range s.blocks {
		if !b.done {
			for _, fr := range s.synth(frame{event: "x"}, idx, b) {
				out = append(out, fr.encode(s.style))
			}
		}
	}
	return out
}
