package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/yuanwangz/cpa-plugin-filemode/internal/recent"
)

// history holds the last few file-mode requests for the management page
// (memory only, sized by recent_size).
var history = recent.New(defaultConfig().recentSize)

// recentAPIPath is the management API route of the records, set when CPA registers the routes.
var recentAPIPath atomic.Pointer[string]

const recentResource = "/recent"

// Field names match pluginapi's untagged Management structs.
type managementRegistration struct {
	BasePath         string
	ResourceBasePath string
}

type managementRoute struct {
	Method      string
	Path        string
	Menu        string `json:",omitempty"`
	Description string `json:",omitempty"`
}

type managementRequest struct {
	Method string
	Path   string
}

type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// registerManagement exposes the records twice: as JSON on the management API
// (authenticated by the management key) and as a menu page in the management
// UI. The page carries no data, since resource pages are not authenticated;
// the browser fetches the JSON with the key.
func registerManagement(raw []byte) []byte {
	var req managementRegistration
	_ = json.Unmarshal(raw, &req)
	base := strings.TrimRight(req.BasePath, "/")
	if base == "" {
		base = "/v0/management"
	}
	api := base + "/" + pluginName + recentResource
	recentAPIPath.Store(&api)
	return okEnvelope(map[string]any{
		"routes": []managementRoute{{Method: http.MethodGet, Path: api}},
		"resources": []managementRoute{{
			Path: recentResource, Menu: "文件模式 · 最近请求",
			Description: "最近几条经过文件模式的请求：哪个客户端发的、入站改写是否生效、回复是否经回复工具正常还原。只在内存里，重启清空。",
		}},
	})
}

func handleManagement(raw []byte) []byte {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorEnvelope("invalid_request", err.Error(), http.StatusBadRequest)
	}
	if req.Method != "" && !strings.EqualFold(req.Method, http.MethodGet) {
		return okEnvelope(managementResponse{StatusCode: http.StatusMethodNotAllowed})
	}
	path := strings.TrimRight(req.Path, "/")
	api := ""
	if p := recentAPIPath.Load(); p != nil {
		api = *p
	}
	switch {
	case api != "" && path == api:
		body, _ := json.Marshal(map[string]any{"size": history.Size(), "records": history.List()})
		return okEnvelope(managementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}}, Body: body})
	case strings.HasSuffix(path, recentResource):
		if api == "" {
			api = "/v0/management/" + pluginName + recentResource
		}
		return okEnvelope(managementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}}, Body: recent.Page(api)})
	}
	return okEnvelope(managementResponse{StatusCode: http.StatusNotFound})
}
