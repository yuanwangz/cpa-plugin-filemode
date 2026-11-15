package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/yuanwangz/cpa-plugin-filemode/internal/filemode"
	"gopkg.in/yaml.v3"
)

const (
	pluginName     = "filemode"
	pluginVersion  = "0.1.0"
	executorID     = "filemode"
	modelsProvider = "filemode"
)

// input/output formats the executor works in; CPA translates any other client
// format (e.g. Gemini) into the first one it can.
var executorFormats = []string{filemode.FormatResponses, filemode.FormatChat, filemode.FormatClaude}

type pluginConfig struct {
	Prefix         string   `yaml:"prefix"`
	ToolName       string   `yaml:"tool_name"`
	Fallback       *bool    `yaml:"fallback"`
	UpstreamFixups *bool    `yaml:"upstream_fixups"`
	ListModels     string   `yaml:"list_models"`
	Models         []string `yaml:"models"`
	DebugLog       bool     `yaml:"debug_log"`
}

type config struct {
	prefix     string
	toolName   string
	fallback   bool
	fixups     bool
	listModels string // auto | config | off
	models     []string
	debug      bool
}

func defaultConfig() config {
	return config{prefix: "file-", toolName: filemode.DefaultToolName, fallback: true, fixups: true, listModels: "auto"}
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
	if pc.Fallback != nil {
		cfg.fallback = *pc.Fallback
	}
	if pc.UpstreamFixups != nil {
		cfg.fixups = *pc.UpstreamFixups
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

func (c config) options(inline bool) filemode.Options {
	o := filemode.DefaultOptions()
	o.ToolName = c.toolName
	o.InlineText = inline
	return o
}

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
	RequestNormalizer     bool     `json:"request_normalizer"`
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
	case pluginabi.MethodRequestNormalize:
		return normalizeRequest(req)
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
				{Name: "fallback", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "When the upstream rejects the rewritten request (HTTP 400/413/415/422), resend the original request once."},
				{Name: "upstream_fixups", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Repair attachments after CPA's translation (Claude text documents, Gemini text/plain). Adds one cheap plugin call per translated request."},
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
			RequestNormalizer:     cfg.fixups,
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
			DisplayName: cfg.prefix + id, Description: "File mode: " + id + " with the new user turn sent as .md attachments",
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

// ---------- upstream knowledge ----------

type providerCache struct {
	mu      sync.Mutex
	at      time.Time
	byModel map[string]map[string]bool
}

var providers providerCache

// providersFor returns the provider keys whose credentials list the model.
func providersFor(model string) map[string]bool {
	base := model
	if i := strings.Index(base, "("); i > 0 && strings.HasSuffix(base, ")") {
		base = base[:i]
	}
	providers.mu.Lock()
	defer providers.mu.Unlock()
	if providers.byModel == nil || time.Since(providers.at) > 30*time.Second {
		creds, err := authModels()
		if err != nil {
			return nil
		}
		m := map[string]map[string]bool{}
		for _, c := range creds {
			for _, id := range c.Models {
				if m[id] == nil {
					m[id] = map[string]bool{}
				}
				m[id][strings.ToLower(c.Provider)] = true
			}
		}
		providers.byModel, providers.at = m, time.Now()
	}
	return providers.byModel[base]
}

// inlineText decides whether the message text must stay inline because a
// built-in translator on the way would drop the file part. Only the Claude
// client format into a Codex upstream does that (Codex keeps PDF documents only).
func inlineText(format, model string) bool {
	if format != filemode.FormatClaude {
		return false
	}
	return providersFor(model)["codex"]
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

// active counts rewritten requests in flight; the upstream normalizer returns
// at once while it is zero.
var active atomic.Int64

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
	p.rewrite = filemode.Rewrite(format, p.original, cfg.options(inlineText(format, model)))
	p.send = p.original
	if p.rewrite.Rewritten {
		p.send = p.rewrite.Body
	}
	logf(cfg, "info", "request", map[string]any{"format": format, "model": model, "stream": req.Stream, "rewritten": p.rewrite.Rewritten, "files": p.rewrite.Files, "skip_reason": p.rewrite.Reason})
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
// request shape (the attachments), which is worth one retry without them.
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
	if rewritten {
		active.Add(1)
	}
	resp, err := hostExecute(p.hostRequest(p.send, false))
	if rewritten {
		active.Add(-1)
	}
	if err != nil && rewritten && p.cfg.fallback && rejected(err) {
		logf(p.cfg, "warn", "upstream rejected the file-mode request, resending the original once", map[string]any{"format": p.format, "model": p.model, "error": truncate(err.Error(), 300)})
		rewritten = false
		resp, err = hostExecute(p.hostRequest(p.original, false))
	}
	if err != nil {
		return errorFrom(err)
	}
	body := resp.Body
	if rewritten {
		var restored bool
		body, restored = filemode.Restore(p.format, body, p.cfg.toolName)
		logf(p.cfg, "info", "response", map[string]any{"format": p.format, "model": p.model, "restored": restored, "tool_left": strings.Contains(string(body), p.cfg.toolName)})
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
	if rewritten {
		active.Add(1)
	}
	resp, err := hostStream(p.hostRequest(p.send, true))
	if rewritten {
		active.Add(-1)
	}
	if err != nil && rewritten && p.cfg.fallback && rejected(err) {
		logf(p.cfg, "warn", "upstream rejected the file-mode request, resending the original once", map[string]any{"format": p.format, "model": p.model, "error": truncate(err.Error(), 300)})
		rewritten = false
		resp, err = hostStream(p.hostRequest(p.original, true))
	}
	if err != nil {
		return errorFrom(err)
	}
	restorer := filemode.NewStreamRestorer("", "")
	if rewritten {
		restorer = filemode.NewStreamRestorer(p.format, p.cfg.toolName)
	}
	go pump(p.req.StreamID, resp.StreamID, restorer)
	headers := cleanHeaders(resp.Headers)
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "text/event-stream")
	}
	return okEnvelope(map[string]any{"headers": headers})
}

// pump copies the host model stream into the plugin stream through the restorer.
func pump(out, in string, r filemode.StreamRestorer) {
	errMsg := ""
	defer func() {
		if rec := recover(); rec != nil {
			errMsg = fmt.Sprintf("filemode stream panic: %v", rec)
		}
		closeHostStream(in)
		_, _ = callHost(pluginabi.MethodHostStreamClose, streamClose{StreamID: out, Error: errMsg})
	}()
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
	for {
		raw, err := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: in})
		if err != nil {
			errMsg = err.Error()
			return
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if err := json.Unmarshal(raw, &chunk); err != nil {
			errMsg = err.Error()
			return
		}
		if len(chunk.Payload) > 0 {
			if !emit(r.Process(chunk.Payload)) {
				return
			}
		}
		if chunk.Error != "" {
			errMsg = chunk.Error
			return
		}
		if chunk.Done {
			emit(r.Finish())
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

// ---------- upstream normalizer ----------

func normalizeRequest(raw []byte) []byte {
	if active.Load() == 0 {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	var req pluginapi.RequestTransformRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	cfg := loadConfig()
	if !cfg.fixups || !filemode.IsOurRequest(req.Body, cfg.toolName, "") {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	out, changed := filemode.FixUpstream(req.ToFormat, req.Body, "")
	if !changed {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	logf(cfg, "info", "upstream payload fixed", map[string]any{"from": req.FromFormat, "to": req.ToFormat, "model": req.Model})
	return okEnvelope(pluginapi.PayloadResponse{Body: out})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
