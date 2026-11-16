# cpa-plugin-filemode

CLIProxyAPI（CPA，[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)）的「文件模式」原生插件（Go，C ABI 动态库）。

## 用途

客户端请求的模型名带前缀（默认 `file-`）时生效，例如 `file-gpt-5.5` 实际按 `gpt-5.5` 路由和执行：

- **请求侧**：本轮新消息（最后一条 assistant 之后的 user 消息；工具循环中间步骤沿用同一轮）的文字正文各记作一个文件 `msg-N.md`，正文位置只留一行占位 `[message text in the file msg-N.md]`；图片等非文字部件原样保留；历史消息不动。紧跟这一轮之后，插件补上一步「模型已调用 `read_message`」和它的返回（这一轮全部文件的正文，多个文件用 `<file name="msg-N.md">…</file>` 分隔），上游看到的就像模型已经读过一遍，不多花一次请求。同时注入两个插件自己的工具：`submit_reply`（参数 `{content: string}`）和 `read_message`（可选参数 `{file}`；历史里有这个调用，就必须声明它，否则 Claude 等接口会报错），并在最后一条新消息后追加一句英文说明：用 `submit_reply` 提交完整回复，而且它必须是那一步唯一的调用。补的调用 id 固定（`call_filemode_read_1`），工具循环每一步改写结果一致，前缀缓存不受影响。
- **模型自己再调 `read_message`**：插件内部答掉，客户端看不到。非流式：回应里只有这个调用（没有文字、没有别的调用）时，把调用和返回接在请求末尾再发一次。流式：插件先压住开头的输出，直到看出模型第一件事是文字/别的调用（照常放行）还是 `read_message`（丢掉这次流，答掉后重发）；推理/思考块不算，最多压 64 个分块。每个请求最多重发 `max_reads` 次（默认 2）；之后，或调用夹在别的输出里时，这个调用直接去掉。
- **响应侧**：模型调用了 `submit_reply` 时，这次调用改写成客户端格式里普通的 assistant 文字，客户端看不到这两个工具（Responses 响应里回显的 `tools` 列表也会去掉它们）。和别的工具调用混在同一步时，`submit_reply` 转成文字，其它调用照常保留。模型直接回文字时照常放行。非流式和流式都处理。
- **被拒回退**：上游以 400/413/415/422 拒收改写后的请求时，用原始请求（只去掉前缀）重发一次。流式请求在首字节前判断，所以也能回退。

不带前缀的请求不做任何改动。

## 工作方式

| 环节 | 作用 |
|---|---|
| `model_router` | 只看 `RequestedModel` 是否以前缀开头（不解码 body），命中就交给本插件的 executor（`TargetKind: self`）。 |
| `executor`（input/output 格式：`openai-response`、`openai`、`claude`） | 在客户端格式（source format）上改写请求，再通过 `host.model.execute` / `host.model.execute_stream` 用去掉前缀的模型名交回 CPA 原生链路（选渠道、凭证、翻译、计费都由 CPA 完成）。拿回同一格式的响应后还原 `submit_reply`、答掉 `read_message`。流式时插件自己读宿主流、压住 `submit_reply` 的增量，结束时作为文字发出。 |
| `model_registrar` | 把带前缀的别名登记进 `/v1/models`。 |

正文走的是工具调用和工具返回，CPA 内置翻译器对每种上游都会转（OpenAI 兼容的 `tool` 消息、Codex 的 `function_call_output`、Claude 的 `tool_result`、Gemini 的 `functionResponse`），所以不再需要翻译后的附件修补（旧版的 `request_normalizer` 和 Claude→Codex 退回内联都去掉了）。

前缀缓存：只改写最新一轮，注入的两个工具每次都按同样字节追加在工具表末尾，补的读调用 id 固定（`call_filemode_read_1`），所以同一轮工具循环的各步前缀一致。`tool_choice: none` 的请求不改写消息，但照样补上这两个工具，让工具表（前缀最前面）和平时一致，不会整条重算。进入新一轮时，上一轮从「文件 + 读取」变回原文，缓存从上一轮的用户消息起断开，这是只改写最新一轮的代价。

为什么不用 `request_interceptor` / `response_interceptor` / `response_stream_interceptor`：

1. 模型名到渠道的解析发生在拦截器之前，拦截器改不了路由；只有 `model_router` 能接住一个注册表里不存在的 `file-xxx`，而它若返回 `provider` 目标，只能指定一个渠道，丢掉 CPA 的多渠道轮询和别名池。
2. 拦截器拿不到上游的错误响应，做不了「被拒回退」，也没法答掉 `read_message` 再发一次。
3. 流式拦截器每次只能替换或丢弃一个 chunk，且 `HistoryChunks` 只保留最近 64 个 / 1 MiB；用 executor 自己读宿主流更可控。

## 各上游支持情况

基于 CPA v8.0.72 内置翻译器和 mock 上游的端到端测试（`test/e2e/last-run.txt`）：

| 客户端格式 → 上游 | 正文到上游的形态 | 工具调用还原 | 模型再调 read_message | 端到端 |
|---|---|---|---|---|
| Responses / Chat → OpenAI 兼容 | `tool` 消息 | ✓ | ✓ | ✓ mock |
| Responses / Chat → Codex | `function_call_output` | ✓ | ✓ | ✓ mock |
| Responses / Chat → Claude | `tool_result` | ✓ | ✓ | ✓ mock |
| Responses / Chat → Gemini | `functionResponse`（Chat 来的：CPA 把工具内容按原始 JSON 放进 `result`，即带引号的字符串） | ✓ | ✓ | ✓ mock |
| Claude → Claude / OpenAI 兼容 / Codex / Gemini | 同上 | ✓ | ✓ | ✓ mock |
| Antigravity | 同 Gemini 链路 | 同 Gemini | 同 Gemini | ✗ 未测（需 OAuth） |
| Gemini 客户端（`/v1beta/models/file-xxx:generateContent`） | CPA 先把请求翻成 Responses 交给插件，之后同 Responses 行 | ✓ | ✓ | ✓ mock（→ OpenAI 兼容） |

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
      tool_name: "submit_reply"  # 回复工具名（字母、数字、_、-，最长 64）
      read_tool_name: "read_message"  # 读消息工具名（同上，不能和 tool_name 相同）
      reply_tool: true           # 回复走不走回复工具；false = 回复工具和提示都不注入，模型直接回文字（可逐字流式），只留读取工具送正文
      max_reads: 2               # 模型自己再调读工具时，插件答掉并重发的次数上限（0..5；0 = 不重发，调用直接去掉）
      fallback: true             # 上游拒收（400/413/415/422）时用原始请求重发一次
      list_models: "auto"        # /v1/models 别名：auto = CPA 已知的全部模型 + models；config = 只列 models；off = 不列
      models: []                 # 额外要列出的基础模型名
      debug_log: false           # 每个文件模式请求记一行（编号、格式、模型、文件数、是否回退/重读），从不记正文
      recent_size: 20            # 内存里保留最近多少条请求记录给管理界面看（0 = 不记，最多 500）
```

管理界面里每个配置项的说明是中文的（CPA 的插件配置说明只有一种语言）。

旧配置里的 `upstream_fixups` 不再起作用，留着也不报错。

动态库文件名决定插件 ID：放在 `plugins/<GOOS>/<GOARCH>/filemode.so`（或 `plugins/filemode.so`），配置键就是 `filemode`。

「文件」只是给模型看的名字，插件不落盘，所以没有 `keep_files` 选项。

## 最近请求（可观测性）

插件在内存里保留最近 `recent_size` 条（默认 20）经过文件模式的请求，不落盘、不加数据库，重启 CPA 后清空，也不记录请求和回复的正文。不带前缀的请求不记。

正常与否按**这条请求进来时**的 `reply_tool` 判断：开着时走了回复工具才算正常，关着时直接回文字就算正常；切换开关不影响已有记录。

每条记录：时间、编号（和 `debug_log` 日志行里的 `id` 对得上）、请求的模型（带前缀）、格式、是否流式、客户端（User-Agent）、耗时，以及两个结论：

- **入站处理**：`正常`（本轮正文换成了 N 个文件，模型又自己读了几次）/ `只补工具表`（`tool_choice: none`，消息不动，只补工具保前缀缓存）/ `未改写`（附原因，比如没有新用户消息、工具重名）/ `被上游拒绝`（已退回原请求，附上游状态码和信息）。
- **回复**：`正常`（模型调了 `submit_reply`，已还原成文字）/ `直接回文字`（没走回复工具，原样转发）/ `文字回复`（这次 `reply_tool` 是关的，直接回文字就是正常）/ `原样转发`（这次没改写）/ `失败`（上游错误或流中断，附原因）/ `进行中`。

两处都能看：

- 管理界面的插件菜单「文件模式 · 最近请求」（`/v0/resource/plugins/filemode/recent`）。CPA 的插件资源页不经管理鉴权，所以这一页里没有数据：填一次管理密钥（只存在这个浏览器的 localStorage），页面再带着密钥去取，页面适配手机和深色模式：顶部按结论计数（点一下筛选），可按模型或客户端搜索，点一条展开详情，可开每 5 秒自动刷新（切到后台时暂停）。
- 管理 API `GET /v0/management/filemode/recent`（需要管理密钥），返回 `{ "size": 20, "records": [...] }`，最新的在前。

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

- `internal/recent/*_test.go`：环形记录（最新在前、缩容、0 = 不记）、错误截断、菜单页嵌入接口路径。
- `internal/filemode/*_test.go`：请求改写（三种格式、轮次判定、工具循环里补的调用位置、`tool_choice: none` 只补工具且工具表和改写时一致、重名工具、cache_control 挪到工具返回上）、读工具的返回和追加、非流式里「只调了读工具」的判定、流式闸门、非流式和三种流式还原（含混合调用、读调用去掉、工具序号重排、Codex 的 event/data 分块）。
- `test/e2e/`：`mock_upstream.py` 同时扮演 OpenAI 兼容、Codex、Claude、Gemini 四种上游；`e2e_test.py` 覆盖不带前缀原样、带前缀剥离 + 正文只在读工具返回里 + 注入工具、非流式和流式还原、混合调用、直接回文字、模型再调读工具（重发一次、客户端只见回复）、被拒回退、`/v1/models`、Gemini 客户端格式、最近请求记录（要管理密钥、各种结论、不记不带前缀的请求、不含正文、菜单页不带数据）。

## 限制

- **只认文字正文**：图片、音频和客户端自带的文件原样留在消息里；一条消息里多个文字部件合成一个文件（用换行连接）。
- **历史形态会变**：一轮结束后，这轮的消息在下一轮作为历史以原文出现（补的读调用只给新一轮），按设计如此；这一步前缀缓存会失效一次。
- **带思考签名的上游**：Claude 开着 extended thinking 时，当前轮里的 assistant 工具调用要求带 thinking 块，插件补的调用没有，接口可能 400，此时走「被拒回退」按原样发（这一轮不是文件模式）。Gemini 的 thought signature 由 CPA 翻译器自己补。
- **流式回复整段发出**：`submit_reply` 的内容在它的调用结束时一次性作为文字发出，不是逐字流出。流式开头会压住最多 64 个分块等判定（见上），直接回文字的回复在第一段文字出现时放行。
- **回退时机**：只在上游给出 400/413/415/422 且尚未开始输出时重发；流开始后的错误不重发。
- **token 计数**：`count_tokens` 对带前缀的模型返回按原始请求字节数 / 4 的估算值（宿主没有在另一个模型上计数的回调）。
- **`/v1/models` 刷新时机**：别名在插件加载和配置重载时登记；之后新增的模型要等下一次配置重载才会出现。路由不依赖这个列表，任何 `file-` 前缀都能用。
- **开销**：声明了 `model_router` 后，CPA 会在每个请求上调一次插件的 `model.route`，宿主会把请求体序列化给插件；插件只解析模型名，不解码 body。
- 未测试 Responses 的 WebSocket 通道、`Home` 模式（该模式下 CPA 不允许插件 executor 路由）。
