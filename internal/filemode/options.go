// Package filemode holds the protocol logic of the file-mode plugin: rewriting a
// client request so the text of the new user turn is replaced by a file name and
// travels as the result of a read tool the model has already called, injecting
// the reply tool, and restoring that tool call into plain assistant text. It has
// no cgo and no host dependency.
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
	DefaultToolName     = "submit_reply"
	DefaultReadToolName = "read_message"
	DefaultPlaceholder  = "[message text in the file {name}]"
	DefaultInstruction  = "\n\nThis turn is in file mode: you must reply to the message(s) above by calling the {tool} tool (argument: content), never as plain text. Other tools stay available for the work before that; when you reply, that call must be the only tool call in that step, and its content is exactly what the user receives."
	DefaultToolDesc     = "Submit your complete reply to the user. In file mode every reply must go through this tool, not plain text. The content is delivered to them verbatim as your answer (Markdown allowed). Call it alone, as the only tool call in that step."
	contentArgDesc     = "The complete reply, exactly as the user should receive it."
	DefaultReadDesc    = "Read the text of the new message(s) of this turn. With no argument it returns every file of the turn."
	fileArgDesc        = "Optional file name, e.g. msg-1.md; omit to read them all."
)

// Options controls one rewrite.
type Options struct {
	ToolName    string
	Placeholder string // "{name}" is replaced by the file name
	Instruction string // "{tool}" is replaced by the tool name
	ToolDesc    string
	ReadTool    string // the read tool; its call and result are added after the turn
	ReadDesc    string
}

// DefaultOptions returns the options used by the plugin unless configured.
func DefaultOptions() Options {
	return Options{ToolName: DefaultToolName, Placeholder: DefaultPlaceholder, Instruction: DefaultInstruction, ToolDesc: DefaultToolDesc, ReadTool: DefaultReadToolName, ReadDesc: DefaultReadDesc}
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
	if strings.TrimSpace(o.ReadTool) == "" {
		o.ReadTool = d.ReadTool
	}
	if o.ReadDesc == "" {
		o.ReadDesc = d.ReadDesc
	}
	return o
}

func (o Options) placeholder(name string) string {
	return strings.ReplaceAll(o.Placeholder, "{name}", name)
}

func (o Options) instruction() string {
	return strings.ReplaceAll(o.Instruction, "{tool}", o.ToolName)
}

// ValidToolName reports whether name is usable as a function name on every
// supported upstream (OpenAI, Anthropic and Gemini share this subset).
func ValidToolName(name string) bool {
	return toolNameRe.MatchString(name)
}

var toolNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
