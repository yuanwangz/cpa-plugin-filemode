# cpa-plugin-filemode

CLIProxyAPI（CPA，[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)）的「文件模式」原生插件（Go，C ABI 动态库）。

## 用途

客户端请求的模型名带前缀（默认 `file-`）时生效，例如 `file-gpt-5.5` 实际按 `gpt-5.5` 路由和执行：

- **请求侧**：本轮新消息（最后一条 assistant 之后的 user 消息；工具循环中间步骤沿用同一轮）的文字正文各写成一个 `msg-N.md` 附件，正文位置只留一行占位 `[message text in the attached file msg-N.md]`；图片等非文字部件原样保留；历史消息不动。注入一个插件自己的工具（默认 `submit_reply`，参数 `{content: string}`），并在最后一条新消息后追加一句英文说明：用这个工具提交完整回复，而且它必须是那一步唯一的调用。
- **响应侧**：模型调用了 `submit_reply` 时，这次调用改写成客户端格式里普通的 assistant 文字，客户端看不到这个工具（Responses 响应里回显的 `tools` 列表也会去掉它）。和别的工具调用混在同一步时，`submit_reply` 转成文字，其它调用照常保留。模型直接回文字时照常放行。非流式和流式都处理。
- **被拒回退**：上游以 400/413/415/422 拒收改写后的请求时，用原始请求（只去掉前缀）重发一次。流式请求在首字节前判断，所以也能回退。

不带前缀的请求不做任何改动。

## 工作方式

| 环节 | 作用 |
|---|---|
| `model_router` | 只看 `RequestedModel` 是否以前缀开头（不解码 body），命中就交给本插件的 executor（`TargetKind: self`）。 |
| `executor`（input/output 格式：`openai-response`、`openai`、`claude`） | 在客户端格式（source format）上改写请求，再通过 `host.model.execute` / `host.model.execute_stream` 用去掉前缀的模型名交回 CPA 原生链路（选渠道、凭证、翻译、计费都由 CPA 完成）。拿回同一格式的响应后还原 `submit_reply`。流式时插件自己读宿主流、压住 `submit_reply` 的增量，结束时作为文字发出。 |
| `request_normalizer` | CPA 内置翻译器把请求翻成上游格式之后运行，按 ToFormat 修补附件（见下表）。没有文件模式请求在途时直接返回，不解析 body。 |
| `model_registrar` | 把带前缀的别名登记进 `/v1/models`。 |

为什么不用 `request_interceptor` / `response_interceptor` / `response_stream_interceptor`：

1. 模型名到渠道的解析发生在拦截器之前，拦截器改不了路由；只有 `model_router` 能接住一个注册表里不存在的 `file-xxx`，而它若返回 `provider` 目标，只能指定一个渠道，丢掉 CPA 的多渠道轮询和别名池。
2. 拦截器拿不到上游的错误响应，做不了「被拒回退」。
3. 流式拦截器每次只能替换或丢弃一个 chunk，且 `HistoryChunks` 只保留最近 64 个 / 1 MiB；用 executor 自己读宿主流更可控。
4. `host.model.*` 嵌套执行时，宿主会跳过发起插件自己的拦截器（`host_callback_id` 防递归），所以「按 ToFormat 在 intercept_after 里补附件」在这条链路上不会被调用；改用不受此限制、同样在翻译之后运行的 `request_normalizer`。

## 各上游支持情况

基于 CPA v8.0.72 内置翻译器（`test/translator-probe/` 有探测代码和原始输出）和 mock 上游的端到端测试：

| 客户端格式 → 上游 | 附件到上游的形态 | 插件修补 | 工具调用还原 | 端到端 |
|---|---|---|---|---|
| Responses / Chat → OpenAI 兼容 | `file` 部件（`data:text/markdown`，带文件名） | 无 | ✓ | ✓ mock |
| Responses / Chat → Codex | `input_file`（带文件名） | 无 | ✓ | ✓ mock |
| Responses / Chat → Claude | 翻译器产出 base64 `document`，media_type `text/markdown`（Anthropic 的 base64 只收 PDF） | 改成 `source.type: text`、`text/plain`，按占位补 `title` | ✓ | ✓ mock |
| Responses / Chat → Gemini | `inline_data` / `inlineData`，`text/markdown` | 改成 `text/plain` | ✓ | ✓ mock |
| Responses / Chat → Antigravity | `request.contents[].parts[].inlineData`，`text/markdown` | 改成 `text/plain` | 同 Gemini 链路 | ✗ 未测（需 OAuth） |
| Claude → Claude | base64 `document` `text/plain` + `title` | 改成 text source | ✓ | ✓ mock |
| Claude → OpenAI 兼容 | `file` 部件 `text/plain`，翻译器丢了文件名 | 按占位补 `filename` | ✓ | ✓ mock |
| Claude → Gemini / Antigravity | `inline_data` `text/plain` | 无 | ✓ | Gemini ✓ mock，Antigravity 未测 |
| Claude → Codex | **翻译器只保留 PDF，文本文档会被丢掉** | 无法补：插件改为把正文留在原位（仍注入 `submit_reply`） | ✓ | ✓ mock |
| Gemini 客户端（`/v1beta/models/file-xxx:generateContent`） | CPA 先把请求翻成 Responses 交给插件，之后同 Responses 行 | 同上 | ✓ | ✓ mock（→ OpenAI 兼容） |

「Claude → Codex 退回纯文字」由插件通过 `host.auth.models` 查这个模型由哪些渠道提供（缓存 30 秒），只要其中有 codex 就退回。

## 安装

**从插件商店装**（推荐）：在 CPA 配置里加上本仓库的插件源，再到管理界面的插件商店安装 File Mode，或调用 `POST /v0/management/plugin-store/filemode/install`：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/yuanwangz/cpa-plugin-filemode/main/registry.json"
```

商店从本仓库最新的 release 下载对应平台的 `filemode_<版本>_<goos>_<goarch>.zip`，并用 `checksums.txt` 校验。release 由打 `v*` tag 触发 CI 自动生成，覆盖 linux（amd64、arm64）、darwin（amd64、arm64）和 windows（amd64）。

**手动装**：从 release 下载对应平台的 zip，把里面的动态库放到 `plugins/<GOOS>/<GOARCH>/`，再按下面的配置启用。

## 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    filemode:
      enabled: true
      priority: 10
      prefix: "file-"            # 触发前缀
      tool_name: "submit_reply"  # 注入的工具名（字母、数字、_、-，最长 64）
      fallback: true             # 上游拒收（400/413/415/422）时用原始请求重发一次
      upstream_fixups: true      # 翻译后修补附件（request_normalizer）；关掉则不声明该能力
      list_models: "auto"        # /v1/models 别名：auto = CPA 已知的全部模型 + models；config = 只列 models；off = 不列
      models: []                 # 额外要列出的基础模型名
      debug_log: false           # 每个文件模式请求记一行（格式、模型、附件数、是否回退），从不记正文
```

动态库文件名决定插件 ID：放在 `plugins/<GOOS>/<GOARCH>/filemode.so`（或 `plugins/filemode.so`），配置键就是 `filemode`。

插件不需要临时文件，所以没有 `keep_files` 选项。

## 构建

需要 CGO：

```bash
make build                                        # 产出 build/filemode.so（Linux）
make build CC="zig cc -target aarch64-linux-gnu"  # 没有 gcc 时可用 zig
make test                                         # Go 单元测试
make e2e CPA=/path/to/cpa-binary                  # 端到端：起 mock 上游 + 本地 CPA
```

CPA 本体也必须是 CGO 构建（管理接口响应头 `X-CPA-SUPPORT-PLUGIN: 1`）。依赖的 SDK 版本是 `github.com/router-for-me/CLIProxyAPI/v8 v8.0.72`。

## 测试

- `internal/filemode/*_test.go`：请求改写（三种格式、轮次判定、工具循环、`tool_choice: none`、重名工具、cache_control、内联模式）、非流式还原、三种流式还原（含混合调用、工具序号重排、Codex 的 event/data 分块）、上游修补。
- `test/e2e/`：`mock_upstream.py` 同时扮演 OpenAI 兼容、Codex、Claude、Gemini 四种上游；`e2e_test.py` 覆盖不带前缀原样、带前缀剥离 + 进文件 + 注入工具、非流式和流式还原、混合调用、直接回文字、被拒回退、`/v1/models`、Gemini 客户端格式。

## 限制

- **只认文字正文**：图片、音频和客户端自带的文件原样保留；一条消息里多个文字部件合成一个文件（用换行连接）。
- **历史形态会变**：一轮结束后，这轮的消息在下一轮作为历史以原文出现（附件只给新一轮），按设计如此；这一步前缀缓存会失效一次。
- **流式回复整段发出**：`submit_reply` 的内容在它的调用结束时一次性作为文字发出，不是逐字流出。
- **回退时机**：只在上游给出 400/413/415/422 且尚未开始输出时重发；流开始后的错误不重发。
- **token 计数**：`count_tokens` 对带前缀的模型返回按原始请求字节数 / 4 的估算值（宿主没有在另一个模型上计数的回调）。
- **`/v1/models` 刷新时机**：别名在插件加载和配置重载时登记；之后新增的模型要等下一次配置重载才会出现。路由不依赖这个列表，任何 `file-` 前缀都能用。
- **开销**：声明了 `model_router` 后，CPA 会在每个请求上调一次插件的 `model.route`，宿主会把请求体序列化给插件；插件只解析模型名，不解码 body。`request_normalizer` 同理，插件在没有文件模式请求在途时立即返回；如不需要修补可以设 `upstream_fixups: false`。
- **未在真实上游验证**：OpenAI 官方 API 对 `text/markdown` 的 `input_file` / `file` 是否接受、Antigravity（含其背后的 Claude 模型）对 `inlineData text/plain` 的处理，都只按翻译器输出推断；被拒时靠回退兜底。
- 未测试 Responses 的 WebSocket 通道、`Home` 模式（该模式下 CPA 不允许插件 executor 路由）。
