package recent

import (
	"strings"
	"testing"
)

func TestRingKeepsNewestFirst(t *testing.T) {
	r := New(2)
	a := r.Start(Record{Model: "file-a", Inbound: InRewritten, Reply: ReplyPending})
	r.Start(Record{Model: "file-b"})
	r.Start(Record{Model: "file-c"})
	got := r.List()
	if len(got) != 2 || got[0].Model != "file-c" || got[1].Model != "file-b" {
		t.Fatalf("list = %+v", got)
	}
	r.Finish(a, ReplyTool, 0, "") // dropped record: harmless
	r.Resize(1)
	if l := r.List(); len(l) != 1 || l[0].Model != "file-c" {
		t.Fatalf("after resize = %+v", l)
	}
	r.Resize(0)
	if r.Start(Record{}) != nil || len(r.List()) != 0 {
		t.Fatal("size 0 keeps nothing")
	}
	r.Finish(nil, ReplyError, 500, "x") // nil record is fine
}

func TestFinish(t *testing.T) {
	r := New(5)
	x := r.Start(Record{Model: "file-a"})
	r.Finish(x, ReplyError, 502, strings.Repeat("错", 200))
	l := r.List()[0]
	if l.Reply != ReplyError || l.Status != 502 || len(l.Error) > 304 || !strings.HasSuffix(l.Error, "…") || l.ID == "" {
		t.Fatalf("record = %+v", l)
	}
}

func TestPage(t *testing.T) {
	p := string(Page("/v0/management/plugins/filemode/recent"))
	if !strings.Contains(p, `const API="/v0/management/plugins/filemode/recent"`) || strings.Contains(p, "__API__") {
		t.Fatal("api path not embedded")
	}
}
