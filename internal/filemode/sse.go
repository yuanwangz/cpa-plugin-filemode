package filemode

import (
	"bytes"
	"strings"
)

// frame is one streaming event as the host hands it over: either a raw JSON
// payload (Chat Completions chunks, which the host frames itself) or an SSE
// frame with optional event name.
type frame struct {
	event   string
	data    string
	raw     bool // payload without SSE framing
	hasData bool
	orig    []byte // original bytes when the frame stands alone in its chunk
}

// chunkStyle remembers how the upstream chunks were framed so synthesized
// frames look the same.
type chunkStyle struct {
	raw   bool
	term  string // terminator after an SSE frame: "\n\n", "\n" or ""
	known bool
}

// parseChunk splits one host chunk into frames.
func parseChunk(chunk []byte) ([]frame, chunkStyle) {
	t := bytes.TrimSpace(chunk)
	if len(t) == 0 {
		return nil, chunkStyle{}
	}
	if t[0] == '{' || t[0] == '[' {
		return []frame{{data: string(t), raw: true, hasData: true, orig: chunk}}, chunkStyle{raw: true, known: true}
	}
	st := chunkStyle{known: true}
	switch {
	case bytes.HasSuffix(chunk, []byte("\n\n")):
		st.term = "\n\n"
	case bytes.HasSuffix(chunk, []byte("\n")):
		st.term = "\n"
	}
	var frames []frame
	var cur frame
	var data []string
	has := false
	flush := func() {
		if has {
			cur.data = strings.Join(data, "\n")
			frames = append(frames, cur)
		}
		cur, data, has = frame{}, nil, false
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(chunk), "\r\n", "\n"), "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			cur.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			has = true
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			data = append(data, d)
			has = true
			cur.hasData = true
		}
	}
	flush()
	if len(frames) == 1 {
		frames[0].orig = chunk
	}
	return frames, st
}

// encode renders a frame as one host chunk in the given style.
func (f frame) encode(st chunkStyle) []byte {
	if f.raw || (st.known && st.raw) {
		return []byte(f.data)
	}
	var b strings.Builder
	if f.event != "" {
		b.WriteString("event: ")
		b.WriteString(f.event)
		b.WriteString("\n")
	}
	b.WriteString("data: ")
	b.WriteString(f.data)
	term := st.term
	if !st.known {
		term = "\n\n"
	}
	b.WriteString(term)
	return []byte(b.String())
}
