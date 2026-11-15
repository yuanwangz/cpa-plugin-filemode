// Package filemode holds the protocol logic of the file-mode plugin: rewriting a
// client request so the new user turn travels as attached .md files plus an
// injected reply tool, restoring that tool call into plain assistant text, and
// fixing translated upstream payloads. It has no cgo and no host dependency.
package filemode

import (
	"regexp"
	"strings"
)

// Client-side protocol formats handled by the rewrite and restore logic.
const (
	FormatResponses = "openai-response"
	FormatChat      = "openai"
	FormatClaude    = "claude"
)

// Defaults for the model-facing text. They are English on purpose: the
// instruction is part of the prompt the model reads.
const (
	DefaultToolName    = "submit_reply"
	DefaultPlaceholder = "[message text in the attached file {name}]"
	DefaultInstruction = "\n\nThis turn is in file mode: you must reply to the message(s) above by calling the {tool} tool (argument: content), never as plain text. Other tools stay available for the work before that; when you reply, that call must be the only tool call in that step, and its content is exactly what the user receives."
	DefaultToolDesc    = "Submit your complete reply to the user. In file mode every reply must go through this tool, not plain text. The content is delivered to them verbatim as your answer (Markdown allowed). Call it alone, as the only tool call in that step."
	contentArgDesc     = "The complete reply, exactly as the user should receive it."
)

// Options controls one rewrite.
type Options struct {
	ToolName    string
	Placeholder string // "{name}" is replaced by the file name
	Instruction string // "{tool}" is replaced by the tool name
	ToolDesc    string
	// InlineText keeps the message text inline (no file parts) but still
	// injects the tool and the instruction. Used for upstream paths that are
	// known to drop the file part.
	InlineText bool
}

// DefaultOptions returns the options used by the plugin unless configured.
func DefaultOptions() Options {
	return Options{ToolName: DefaultToolName, Placeholder: DefaultPlaceholder, Instruction: DefaultInstruction, ToolDesc: DefaultToolDesc}
}

func (o Options) normalized() Options {
	d := DefaultOptions()
	if strings.TrimSpace(o.ToolName) == "" {
		o.ToolName = d.ToolName
	}
	if o.Placeholder == "" {
		o.Placeholder = d.Placeholder
	}
	if o.Instruction == "" {
		o.Instruction = d.Instruction
	}
	if o.ToolDesc == "" {
		o.ToolDesc = d.ToolDesc
	}
	return o
}

func (o Options) placeholder(name string) string {
	return strings.ReplaceAll(o.Placeholder, "{name}", name)
}

func (o Options) instruction() string {
	return strings.ReplaceAll(o.Instruction, "{tool}", o.ToolName)
}

// placeholderRegexp matches a rendered placeholder and captures the file name.
func placeholderRegexp(tpl string) *regexp.Regexp {
	if tpl == "" {
		tpl = DefaultPlaceholder
	}
	i := strings.Index(tpl, "{name}")
	if i < 0 {
		return nil
	}
	return regexp.MustCompile(regexp.QuoteMeta(tpl[:i]) + `([^\s\]]+\.md)` + regexp.QuoteMeta(tpl[i+len("{name}"):]))
}

// ValidToolName reports whether name is usable as a function name on every
// supported upstream (OpenAI, Anthropic and Gemini share this subset).
func ValidToolName(name string) bool {
	return toolNameRe.MatchString(name)
}

var toolNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
