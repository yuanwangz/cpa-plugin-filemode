package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/yuanwangz/cpa-plugin-filemode/internal/filemode"
	"gopkg.in/yaml.v3"
)

// pluginVersion is overridden at release build time: -ldflags "-X main.pluginVersion=<ver>".
var pluginVersion = "0.2.0"

const (
	pluginName     = "filemode"
	executorID     = "filemode"
	modelsProvider = "filemode"
)

// input/output formats the executor works in; CPA translates any other client
// format (e.g. Gemini) into the first one it can.
var executorFormats = []string{filemode.FormatResponses, filemode.FormatChat, filemode.FormatClaude}

type pluginConfig struct {
	Prefix         string   `yaml:"prefix"`
	ToolName       string   `yaml:"tool_name"`
	ReadToolName   string   `yaml:"read_tool_name"`
	Fallback       *bool    `yaml:"fallback"`
	MaxReads       *int     `yaml:"max_reads"`
	ListModels     string   `yaml:"list_models"`
	Models         []string `yaml:"models"`
	DebugLog       bool     `yaml:"debug_log"`
}

type config struct {
	prefix     string
	toolName   string
	readTool   string
	fallback   bool
	maxReads   int // times a request is sent again when the model calls the read tool itself
	listModels string // auto | config | off
	models     []string
	debug      bool
}

func defaultConfig() config {
	return config{prefix: "file-", toolName: filemode.DefaultToolName, readTool: filemode.DefaultReadToolName, fallback: true, maxReads: 2, listModels: "auto"}
}

var current atomic.Pointer[config]

func loadConfig() config {
	if c := current.Load(); c != nil {
		return *c
	}
	return defaultConfig()
}

func parseConfig(raw []byte) (config, error) {
	cfg := defaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	var pc pluginConfig
	if err := yaml.Unmarshal(raw, &pc); err != nil {
		return cfg, fmt.Errorf("filemode: parse config: %w", err)
	}
	if p := strings.TrimSpace(pc.Prefix); p != "" {
		cfg.prefix = p
	}
	if t := strings.TrimSpace(pc.ToolName); t != "" {
		if !filemode.ValidToolName(t) {
			return cfg, fmt.Errorf("filemode: invalid tool_name %q (letters, digits, _ and -, max 64)", t)
		}
		cfg.toolName = t
	}
	if t := strings.TrimSpace(pc.ReadToolName); t != "" {
		if !filemode.ValidToolName(t) {
			return cfg, fmt.Errorf("filemode: invalid read_tool_name %q (letters, digits, _ and -, max 64)", t)
		}
		cfg.readTool = t
	}
	if cfg.readTool == cfg.toolName {
		return cfg, fmt.Errorf("filemode: tool_name and read_tool_name must differ")
	}
	if pc.Fallback != nil {
		cfg.fallback = *pc.Fallback
	}
	if pc.MaxReads != nil {
		if *pc.MaxReads < 0 || *pc.MaxReads > 5 {
			return cfg, fmt.Errorf("filemode: max_reads must be 0..5, got %d", *pc.MaxReads)
		}
		cfg.maxReads = *pc.MaxReads
	}
	switch m := strings.ToLower(strings.TrimSpace(pc.ListModels)); m {
	case "":
	case "auto", "config", "off":
		cfg.listModels = m
	default:
		return cfg, fmt.Errorf("filemode: list_models must be auto, config or off, got %q", pc.ListModels)
	}
	for _, m := range pc.Models {
		if m = strings.TrimSpace(m); m != "" {
			cfg.models = append(cfg.models, m)
		}
	}
	cfg.debug = pc.DebugLog
	return cfg, nil
}

func (c config) options() filemode.Options {
	o := filemode.DefaultOptions()
	o.ToolName = c.toolName
	o.ReadTool = c.readTool
	return o
}

func (c config) tools() filemode.Tools { return filemode.Tools{Reply: c.toolName, Read: c.readTool} }

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  capabilities       `json:"capabilities"`
}

type capabilities struct {
	ModelRegistrar        bool     `json:"model_registrar"`
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

func handleMethod(method string, req []byte) []byte {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lr lifecycleRequest
		if len(req) > 0 {
			if err := json.Unmarshal(req, &lr); err != nil {
				return errorEnvelope("invalid_request", err.Error(), 0)
			}
		}
		cfg, err := parseConfig(lr.ConfigYAML)
		if err != nil {
			return errorEnvelope("invalid_config", err.Error(), 0)
		}
		current.Store(&cfg)
		return okEnvelope(buildRegistration(cfg))
	case pluginabi.MethodPluginShutdown, pluginabi.MethodPluginQuiesce:
		return okEnvelope(struct{}{})
	case pluginabi.MethodModelRoute:
		return routeModel(req)
	case pluginabi.MethodModelRegister:
		return registerModels()
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": executorID})
	case pluginabi.MethodExecutorExecute:
		return execute(req)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(req)
	case pluginabi.MethodExecutorCountTokens:
		return countTokens(req)
	}
	return errorEnvelope("unknown_method", "unknown method: "+method, 0)
}

func buildRegistration(cfg config) registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "yuanwangz",
			GitHubRepository: "https://github.com/yuanwangz/cpa-plugin-filemode",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "prefix", Type: pluginapi.ConfigFieldTypeString, Default: "file-", Description: "Model-name prefix that turns file mode on; file-gpt-5.5 runs as gpt-5.5. Requests without it are untouched."},
				{Name: "tool_name", Type: pluginapi.ConfigFieldTypeString, Default: filemode.DefaultToolName, Description: "Name of the injected reply tool (argument: content)."},
				{Name: "read_tool_name", Type: pluginapi.ConfigFieldTypeString, Default: filemode.DefaultReadToolName, Description: "Name of the read tool. The request carries one call of it, already answered with the message text; the plugin answers any further call itself."},
				{Name: "max_reads", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: "How many times a request is sent again when the model calls the read tool itself (0 = never; the call is then dropped)."},
				{Name: "fallback", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "When the upstream rejects the rewritten request (HTTP 400/413/415/422), resend the original request once."},
				{Name: "list_models", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"auto", "config", "off"}, Default: "auto", Description: "Prefixed aliases in /v1/models: auto = every model CPA knows plus models, config = only models, off = none. Refreshed when the config reloads."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Default: []string{}, Description: "Base model names to list with the prefix in /v1/models."},
				{Name: "debug_log", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Log one line per file-mode request (format, model, files, fallback). Never logs request or response bodies."},
			},
		},
		Capabilities: capabilities{
			ModelRegistrar:        cfg.listModels != "off",
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeStatic),
			ExecutorInputFormats:  executorFormats,
			ExecutorOutputFormats: executorFormats,
		},
	}
}

func logf(cfg config, level, msg string, fields map[string]any) {
	if !cfg.debug && level != "warn" {
		return
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("filemode: ")
	b.WriteString(msg)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, fields[k])
	}
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{"level": level, "message": b.String()})
}

// ---------- routing ----------

// routeRequest decodes only what routing needs; the base64 body is not decoded.
type routeRequest struct {
	RequestedModel string
	SourceFormat   string
}

func routeModel(raw []byte) []byte {
	var req routeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	cfg := loadConfig()
	if _, ok := stripPrefix(cfg, req.RequestedModel); !ok {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "filemode_prefix"})
}

func stripPrefix(cfg config, model string) (string, bool) {
	m := strings.TrimSpace(model)
	if cfg.prefix == "" || !strings.HasPrefix(m, cfg.prefix) || len(m) == len(cfg.prefix) {
		return m, false
	}
	return m[len(cfg.prefix):], true
}

// ---------- /v1/models ----------

func registerModels() []byte {
	cfg := loadConfig()
	seen := map[string]bool{}
	var ids, auto []string
	add := func(list *[]string, m string) {
		m = strings.TrimSpace(m)
		if m == "" || strings.HasPrefix(m, cfg.prefix) || seen[m] {
			return
		}
		seen[m] = true
		*list = append(*list, m)
	}
	for _, m := range cfg.models {
		add(&ids, m)
	}
	if cfg.listModels == "auto" {
		if creds, err := authModels(); err == nil {
			for _, c := range creds {
				if strings.EqualFold(c.Provider, modelsProvider) {
					continue
				}
				for _, m := range c.Models {
					add(&auto, m)
				}
			}
		}
		sort.Strings(auto)
		ids = append(ids, auto...)
	}
	models := make([]pluginapi.ModelInfo, 0, len(ids))
	now := time.Now().Unix()
	for _, id := range ids {
		models = append(models, pluginapi.ModelInfo{
			ID: cfg.prefix + id, Object: "model", Created: now, OwnedBy: modelsProvider,
			DisplayName: cfg.prefix + id, Description: "File mode: " + id + " reading the new user turn through a tool",
			UserDefined: true,
		})
	}
	return okEnvelope(pluginapi.ModelRegistrationResponse{Provider: modelsProvider, Models: models})
}

func authModels() ([]pluginapi.HostAuthModels, error) {
	raw, err := callHost(pluginabi.MethodHostAuthModels, map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp pluginapi.HostAuthModelsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return resp.Credentials, nil
}

// ---------- executor ----------

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostModelRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type prepared struct {
	cfg      config
	format   string
	model    string
	original []byte // client request with the prefix stripped
	send     []byte
	rewrite  filemode.RewriteResult
	req      rpcExecutorRequest
}

func prepare(raw []byte) (*prepared, []byte) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, errorEnvelope("invalid_request", err.Error(), http.StatusBadRequest)
	}
	cfg := loadConfig()
	format := strings.TrimSpace(req.Format)
	if format == "" {
		format = strings.TrimSpace(req.SourceFormat)
	}
	model, _ := stripPrefix(cfg, req.Model)
	if model == "" {
		return nil, errorEnvelope("invalid_request", "model is required", http.StatusBadRequest)
	}
	p := &prepared{cfg: cfg, format: format, model: model, req: req}
	p.original = filemode.SetModel(req.Payload, model)
	p.rewrite = filemode.Rewrite(format, p.original, cfg.options())
	p.send = p.original
	if p.rewrite.Rewritten {
		p.send = p.rewrite.Body
	}
	logf(cfg, "info", "request", map[string]any{"format": format, "model": model, "stream": req.Stream, "rewritten": p.rewrite.Rewritten, "files": len(p.rewrite.Files), "skip_reason": p.rewrite.Reason})
	return p, nil
}

func (p *prepared) hostRequest(body []byte, stream bool) hostModelRequest {
	callerScope, _ := p.req.Metadata["caller_scope"].(string)
	return hostModelRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: p.format,
			ExitProtocol:  p.format,
			Model:         p.model,
			Stream:        stream,
			Body:          body,
			Headers:       p.req.Headers,
			Query:         p.req.Query,
			Alt:           p.req.Alt,
			CallerScope:   callerScope,
		},
		HostCallbackID: p.req.HostCallbackID,
	}
}

// rejected reports whether an upstream error looks like a refusal of the
// request shape (the added tool call, the tools), which is worth one retry
// without them.
func rejected(err error) bool {
	he, ok := err.(*hostError)
	if !ok {
		return false
	}
	switch he.Status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func errorFrom(err error) []byte {
	if he, ok := err.(*hostError); ok {
		status := he.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		return errorEnvelope("upstream_error", he.Message, status)
	}
	return errorEnvelope("upstream_error", err.Error(), http.StatusBadGateway)
}

func cleanHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, v := range h {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		out[k] = v
	}
	return out
}

func execute(raw []byte) []byte {
	p, errEnv := prepare(raw)
	if errEnv != nil {
		return errEnv
	}
	rewritten := p.rewrite.Rewritten
	send := p.send
	resp, err := hostExecute(p.hostRequest(send, false))
	if err != nil && rewritten && p.cfg.fallback && rejected(err) {
		logf(p.cfg, "warn", "upstream rejected the file-mode request, resending the original once", map[string]any{"format": p.format, "model": p.model, "error": truncate(err.Error(), 300)})
		rewritten = false
		resp, err = hostExecute(p.hostRequest(p.original, false))
	}
	// the model called the read tool itself: answer it and ask again
	for n := 0; err == nil && rewritten && n < p.cfg.maxReads; n++ {
		calls := filemode.ReadCalls(p.format, resp.Body, p.cfg.readTool)
		if len(calls) == 0 {
			break
		}
		for k, c := range calls {
			id := c.ID
			if id == "" {
				id = filemode.ReadCallID(2 + n*10 + k)
			}
			send = filemode.AppendReadCall(p.format, send, p.cfg.options(), id, c.Args, p.rewrite.Files)
		}
		logf(p.cfg, "info", "model called the read tool, answered and sent again", map[string]any{"format": p.format, "model": p.model, "calls": len(calls)})
		resp, err = hostExecute(p.hostRequest(send, false))
	}
	if err != nil {
		return errorFrom(err)
	}
	body := resp.Body
	if rewritten {
		var restored bool
		body, restored = filemode.Restore(p.format, body, p.cfg.tools())
		logf(p.cfg, "info", "response", map[string]any{"format": p.format, "model": p.model, "restored": restored})
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: body, Headers: cleanHeaders(resp.Headers)})
}

func hostExecute(req hostModelRequest) (pluginapi.HostModelExecutionResponse, error) {
	raw, err := callHost(pluginabi.MethodHostModelExecute, req)
	if err != nil {
		return pluginapi.HostModelExecutionResponse{}, err
	}
	var resp pluginapi.HostModelExecutionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return resp, err
	}
	if resp.StatusCode >= 400 {
		return resp, &hostError{Code: "upstream_error", Message: string(resp.Body), Status: resp.StatusCode}
	}
	return resp, nil
}

func hostStream(req hostModelRequest) (pluginapi.HostModelStreamResponse, error) {
	raw, err := callHost(pluginabi.MethodHostModelExecuteStream, req)
	if err != nil {
		return pluginapi.HostModelStreamResponse{}, err
	}
	var resp pluginapi.HostModelStreamResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return resp, err
	}
	if resp.StatusCode >= 400 {
		closeHostStream(resp.StreamID)
		return resp, &hostError{Code: "upstream_error", Message: fmt.Sprintf("upstream status %d", resp.StatusCode), Status: resp.StatusCode}
	}
	if resp.StreamID == "" {
		return resp, &hostError{Code: "upstream_error", Message: "empty stream id"}
	}
	return resp, nil
}

func closeHostStream(id string) {
	if id != "" {
		_, _ = callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: id})
	}
}

type streamEmit struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type streamClose struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func executeStream(raw []byte) []byte {
	p, errEnv := prepare(raw)
	if errEnv != nil {
		return errEnv
	}
	if p.req.StreamID == "" {
		return errorEnvelope("executor_error", "stream_id is required", 0)
	}
	rewritten := p.rewrite.Rewritten
	resp, err := hostStream(p.hostRequest(p.send, true))
	if err != nil && rewritten && p.cfg.fallback && rejected(err) {
		logf(p.cfg, "warn", "upstream rejected the file-mode request, resending the original once", map[string]any{"format": p.format, "model": p.model, "error": truncate(err.Error(), 300)})
		rewritten = false
		resp, err = hostStream(p.hostRequest(p.original, true))
	}
	if err != nil {
		return errorFrom(err)
	}
	go pump(p, rewritten, resp.StreamID)
	headers := cleanHeaders(resp.Headers)
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "text/event-stream")
	}
	return okEnvelope(map[string]any{"headers": headers})
}

func readChunk(in string) (pluginapi.HostModelStreamReadResponse, error) {
	var chunk pluginapi.HostModelStreamReadResponse
	raw, err := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: in})
	if err != nil {
		return chunk, err
	}
	err = json.Unmarshal(raw, &chunk)
	return chunk, err
}

// pump copies the host model stream into the plugin stream through the
// restorer. While the gate is undecided the chunks are held back; a stream
// that opens with a read call is dropped and the request sent again with the
// call answered (at most maxReads times).
func pump(p *prepared, rewritten bool, in string) {
	out := p.req.StreamID
	errMsg := ""
	defer func() {
		if rec := recover(); rec != nil {
			errMsg = fmt.Sprintf("filemode stream panic: %v", rec)
		}
		closeHostStream(in)
		_, _ = callHost(pluginabi.MethodHostStreamClose, streamClose{StreamID: out, Error: errMsg})
	}()
	restorer := filemode.NewStreamRestorer("", filemode.Tools{})
	if rewritten {
		restorer = filemode.NewStreamRestorer(p.format, p.cfg.tools())
	}
	emit := func(chunks [][]byte) bool {
		for _, c := range chunks {
			if len(c) == 0 {
				continue
			}
			if _, err := callHost(pluginabi.MethodHostStreamEmit, streamEmit{StreamID: out, Payload: c}); err != nil {
				return false
			}
		}
		return true
	}
	send := p.send
	reads := 0
	var gate *filemode.Gate
	if rewritten && p.cfg.maxReads > 0 {
		gate = filemode.NewGate(p.format, p.cfg.readTool)
	}
	var held [][]byte
	release := func() bool {
		gate = nil
		for _, c := range held {
			if !emit(restorer.Process(c)) {
				return false
			}
		}
		held = nil
		return true
	}
	for {
		chunk, err := readChunk(in)
		if err != nil {
			errMsg = err.Error()
			return
		}
		if len(chunk.Payload) > 0 {
			if gate != nil {
				held = append(held, chunk.Payload)
				decided, reread := gate.Feed(chunk.Payload)
				if decided && reread {
					closeHostStream(in)
					reads++
					send = filemode.AppendReadCall(p.format, send, p.cfg.options(), filemode.ReadCallID(1+reads), "{}", p.rewrite.Files)
					logf(p.cfg, "info", "model called the read tool, answered and sent again", map[string]any{"format": p.format, "model": p.model, "stream": true})
					resp, err := hostStream(p.hostRequest(send, true))
					if err != nil {
						in = ""
						errMsg = err.Error()
						return
					}
					in, held = resp.StreamID, nil
					gate = filemode.NewGate(p.format, p.cfg.readTool)
					if reads >= p.cfg.maxReads {
						gate = nil
					}
					continue
				}
				if decided && !release() {
					return
				}
			} else if !emit(restorer.Process(chunk.Payload)) {
				return
			}
		}
		if chunk.Error != "" {
			errMsg = chunk.Error
			return
		}
		if chunk.Done {
			if !release() {
				return
			}
			emit(restorer.Finish())
			return
		}
	}
}

// countTokens answers token counting for prefixed models with an estimate
// (about 4 bytes per token of the original request): the host offers no
// callback that counts on another model.
func countTokens(raw []byte) []byte {
	p, errEnv := prepare(raw)
	if errEnv != nil {
		return errEnv
	}
	n := len(p.original) / 4
	return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(fmt.Sprintf(`{"input_tokens":%d}`, n)), Headers: http.Header{"Content-Type": []string{"application/json"}}})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
