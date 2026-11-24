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
	DefaultInstruction  = "\n\nFile mode: when you write your final answer for this step, pass it to the {tool} tool (argument: content) instead of plain text, as the only tool call in that step. Use other tools as usual until then."
	DefaultToolDesc     = "Write your final answer for this step (Markdown allowed). Use it instead of plain text, as the only tool call in that step."
	contentArgDesc      = "The final answer, as plain text or Markdown."
	DefaultReadDesc     = "Read the text of the new message(s) of this turn. With no argument it returns every file of the turn."
	fileArgDesc         = "Optional file name, e.g. msg-1.md; omit to read them all."
)

// Options controls one rewrite.
type Options struct {
	ToolName    string
	Placeholder string // "{name}" is replaced by the file name
	Instruction string // "{tool}" is replaced by the tool name
	ToolDesc    string
	ReadTool    string // the read tool; its call and result are added after the turn
	ReadDesc    string
	NoReply     bool // reply tool off: neither the reply tool nor the instruction is injected; the model answers in plain text

	noInstruction bool // this request only: the turn goes without the reply instruction (tool_choice none)
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
	if o.NoReply || o.noInstruction {
		return ""
	}
	return strings.ReplaceAll(o.Instruction, "{tool}", o.ToolName)
}

// ValidToolName reports whether name is usable as a function name on every
// supported upstream (OpenAI, Anthropic and Gemini share this subset).
func ValidToolName(name string) bool {
	return toolNameRe.MatchString(name)
}

// ownTools are the tool names the plugin declares on a request.
func (o Options) ownTools() []string {
	if o.NoReply {
		return []string{o.ReadTool}
	}
	return []string{o.ToolName, o.ReadTool}
}

var toolNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
