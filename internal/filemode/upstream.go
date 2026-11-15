package filemode

import (
	"bytes"
	"encoding/base64"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsOurRequest reports cheaply whether a translated upstream body carries the
// reply tool and a file placeholder, i.e. was produced by Rewrite.
func IsOurRequest(body []byte, tool, placeholder string) bool {
	if placeholder == "" {
		placeholder = DefaultPlaceholder
	}
	marker := placeholder
	if i := strings.Index(marker, "{name}"); i >= 0 {
		marker = marker[:i]
	}
	return bytes.Contains(body, []byte(tool)) && (marker == "" || bytes.Contains(body, []byte(marker)))
}

// FixUpstream repairs a payload after CPA translated it to the upstream format
// toFormat, for the shapes the built-in translators produce from Rewrite's
// output. It only touches attachments that Rewrite created: text documents and
// inline data with a text/* media type.
//
//   - claude: a base64 document with a text/* media type (Anthropic accepts
//     base64 only for PDF) becomes a text-source document, titled after its
//     placeholder.
//   - gemini, gemini-cli, antigravity: inline data text/markdown becomes
//     text/plain (the type Gemini documents for plain text).
//   - openai: a file part without a filename gets the one from its placeholder.
func FixUpstream(toFormat string, body []byte, placeholder string) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return body, false
	}
	re := placeholderRegexp(placeholder)
	switch toFormat {
	case "claude":
		return fixClaude(body, re)
	case "gemini", "gemini-cli", "antigravity", "vertex", "aistudio":
		return fixGemini(body)
	case "openai":
		return fixOpenAI(body, re)
	}
	return body, false
}

func nameFrom(re interface{ FindStringSubmatch(string) []string }, text string) string {
	if re == nil {
		return ""
	}
	if m := re.FindStringSubmatch(text); len(m) > 1 {
		return m[1]
	}
	return ""
}

func fixClaude(body []byte, re interface{ FindStringSubmatch(string) []string }) ([]byte, bool) {
	out := body
	changed := false
	for mi, m := range gjson.GetBytes(body, "messages").Array() {
		lastName := ""
		for bi, b := range m.Get("content").Array() {
			switch b.Get("type").String() {
			case "text":
				if n := nameFrom(re, b.Get("text").String()); n != "" {
					lastName = n
				}
			case "document":
				src := b.Get("source")
				mt := strings.ToLower(src.Get("media_type").String())
				if src.Get("type").String() != "base64" || !strings.HasPrefix(mt, "text/") {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(src.Get("data").String())
				if err != nil {
					continue
				}
				p := "messages." + itoa(mi) + ".content." + itoa(bi)
				doc := `{"type":"text","media_type":"text/plain","data":""}`
				doc, _ = sjson.Set(doc, "data", string(raw))
				out, _ = sjson.SetRawBytes(out, p+".source", []byte(doc))
				if !b.Get("title").Exists() && lastName != "" {
					out, _ = sjson.SetBytes(out, p+".title", lastName)
				}
				changed = true
			}
		}
	}
	return out, changed
}

func fixGemini(body []byte) ([]byte, bool) {
	out := body
	changed := false
	for _, root := range []string{"contents", "request.contents"} {
		for ci, c := range gjson.GetBytes(body, root).Array() {
			for pi, p := range c.Get("parts").Array() {
				for _, key := range []string{"inline_data", "inlineData"} {
					d := p.Get(key)
					if !d.Exists() {
						continue
					}
					for _, mk := range []string{"mime_type", "mimeType"} {
						if strings.EqualFold(d.Get(mk).String(), "text/markdown") {
							path := root + "." + itoa(ci) + ".parts." + itoa(pi) + "." + key + "." + mk
							out, _ = sjson.SetBytes(out, path, "text/plain")
							changed = true
						}
					}
				}
			}
		}
	}
	return out, changed
}

func fixOpenAI(body []byte, re interface{ FindStringSubmatch(string) []string }) ([]byte, bool) {
	out := body
	changed := false
	for mi, m := range gjson.GetBytes(body, "messages").Array() {
		lastName := ""
		for pi, p := range m.Get("content").Array() {
			switch p.Get("type").String() {
			case "text":
				if n := nameFrom(re, p.Get("text").String()); n != "" {
					lastName = n
				}
			case "file":
				if p.Get("file.filename").String() == "" && lastName != "" && strings.HasPrefix(p.Get("file.file_data").String(), "data:text/") {
					out, _ = sjson.SetBytes(out, "messages."+itoa(mi)+".content."+itoa(pi)+".file.filename", lastName)
					changed = true
				}
			}
		}
	}
	return out, changed
}
