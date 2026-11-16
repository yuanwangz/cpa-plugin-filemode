// Package recent keeps the last few file-mode requests in memory for the
// management page: which request went through the plugin, whether the inbound
// rewrite worked and whether the reply came back through the reply tool.
// Nothing is written to disk and no request or response body is kept; the
// records are gone when CPA restarts.
package recent

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Inbound outcomes.
const (
	InRewritten = "rewritten"  // the new user turn went out as files + read call
	InSkipped   = "skipped"    // nothing to rewrite (Note says why); sent as is
	InFallback  = "fallback"   // upstream rejected the rewritten request; original sent instead
	InToolsOnly = "tools_only" // tool_choice none: messages untouched, tools appended to keep the prefix cache
)

// Reply outcomes.
const (
	ReplyPending = "pending" // stream still running
	ReplyTool    = "tool"    // the model answered through the reply tool, turned back into text
	ReplyPlain   = "plain"   // the model answered in plain text without the reply tool
	ReplyText    = "text"    // plain text answer with the reply tool turned off (reply_tool: false)
	ReplyPass    = "pass"    // request was not rewritten, response passed through
	ReplyError   = "error"   // upstream or stream error (Error says what)
)

// Record is one file-mode request.
type Record struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Model      string    `json:"model"`  // as requested, with the prefix
	Format     string    `json:"format"` // executor format
	Stream     bool      `json:"stream"`
	Client     string    `json:"client,omitempty"` // User-Agent, clipped
	Inbound    string    `json:"inbound"`
	Note       string    `json:"note,omitempty"` // skip reason or why the upstream rejected
	Files      int       `json:"files"`
	Reads      int       `json:"reads"`      // read-tool calls the plugin answered itself
	ReplyTool  bool      `json:"reply_tool"` // reply_tool switch when this request came in
	Reply      string    `json:"reply"`
	Status     int       `json:"status,omitempty"` // upstream HTTP status on error
	Error      string    `json:"error,omitempty"`
	DurationMs int64     `json:"duration_ms"`
}

// Ring is a fixed-size, concurrency-safe list of the newest records.
type Ring struct {
	mu   sync.Mutex
	size int
	list []*Record // oldest first
}

// New returns a ring holding at most size records (0 keeps none).
func New(size int) *Ring { return &Ring{size: size} }

// Resize changes the capacity, dropping the oldest records when it shrinks.
func (r *Ring) Resize(size int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.size = size
	r.trim()
}

func (r *Ring) trim() {
	if r.size <= 0 {
		r.list = nil
		return
	}
	if over := len(r.list) - r.size; over > 0 {
		r.list = append([]*Record(nil), r.list[over:]...)
	}
}

// Start adds a record and returns it; finish it with Update. Returns nil when
// the ring keeps nothing, and every method accepts a nil record.
func (r *Ring) Start(rec Record) *Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size <= 0 {
		return nil
	}
	if rec.ID == "" {
		rec.ID = newID()
	}
	if rec.At.IsZero() {
		rec.At = time.Now()
	}
	p := &rec
	r.list = append(r.list, p)
	r.trim()
	return p
}

// Update changes a record under the lock.
func (r *Ring) Update(rec *Record, fn func(*Record)) {
	if rec == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(rec)
}

// Finish sets the reply outcome and the duration.
func (r *Ring) Finish(rec *Record, reply string, status int, err string) {
	r.Update(rec, func(x *Record) {
		x.Reply, x.Status, x.Error = reply, status, clip(err, 300)
		x.DurationMs = time.Since(x.At).Milliseconds()
	})
}

// List returns copies of the records, newest first.
func (r *Ring) List() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(r.list))
	for i := len(r.list) - 1; i >= 0; i-- {
		out = append(out, *r.list[i])
	}
	return out
}

// Size reports the capacity.
func (r *Ring) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Clip shortens s to n bytes on a rune boundary.
func Clip(s string, n int) string { return clip(s, n) }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}
