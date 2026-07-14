# HolmesGPT Alertmanager API 调查

调查日期：2026-07-14。主要基线是 HolmesGPT [0.36.0](https://github.com/HolmesGPT/holmesgpt/releases/tag/0.36.0)（2026-07-13，commit [`ab8896a`](https://github.com/HolmesGPT/holmesgpt/commit/ab8896aee2d53dcbfc80646eeecb667a6b9da32c)）；同时核对了 0.35.0 commit [`046ffde`](https://github.com/HolmesGPT/holmesgpt/commit/046ffdef466700a6ce5cb4fea43875e6dfdee487)，关键结构相同。

## 结论

**当前应由 AlertLens 在调用 Holmes 前验证 active alert。** Holmes CLI 的 Alertmanager 支持本身也是“确定性预查询，然后把 alert 数据交给 LLM”，并不是 Alertmanager toolset。Holmes HTTP API 目前没有 Alertmanager 端点或请求字段，因此无法替 AlertLens 实现可信的硬前置条件。

## CLI 实际如何处理 Alertmanager

1. `holmes investigate alertmanager` 读取 URL、basic auth、alertname/label filter，然后在进入 LLM 之前调用 `source.fetch_issues()`；查询失败就返回，零结果则不调用 LLM。[官方源码](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/holmes/main.py#L445-L538)
2. `AlertManagerSource` 直接 `GET {url}/api/v2/alerts`，参数为 `active=true&silenced=false&inhibited=false`，可加 `filter`；非 200 立即失败。[官方源码](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/holmes/plugins/sources/prometheus/plugin.py#L61-L117)
3. 每个查到的 alert 被转成 `Issue`；`_investigate_issue` 再把 `issue.raw` 嵌入标准 user prompt，通过常规 `ai.call()` 调查。[官方源码](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/holmes/main.py#L163-L189)

因此，“CLI 是不是用 ask + prompt”的精确答案是：**Alertmanager 查询不经过 LLM；查询成功后的调查阶段使用普通 prompt + tool-calling LLM。**

## HTTP API 能力边界

- 公开调查入口是 `POST /api/chat`，官方参考列出的请求字段只是 `ask`、history、model、stream、structured output、frontend tools、additional prompt 等，没有 Alertmanager URL、filter 或 verification 字段。[官方 API 参考](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/docs/reference/http-api.md#L82-L101) [Pydantic 请求 schema](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/holmes/core/models.py#L195-L339)
- `/api/chat` 使用服务端已配置的 `CORE`/`CLUSTER` toolsets，没有每请求选择 Alertmanager source 的逻辑。[服务端实现](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/server.py#L483-L560)
- 共享 `Config` 类虽然能读取 `ALERTMANAGER_URL/USERNAME/PASSWORD`，但 `create_alertmanager_source()` 只被 CLI 入口调用，服务器没有连接这段逻辑。[配置与 factory](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/holmes/config.py#L94-L111) [CLI 唯一调用点](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/holmes/main.py#L503-L516)
- 0.35.0 也是同一设计：CLI 预查询 Alertmanager，HTTP server 只暴露通用 chat/model/health 端点。[0.35.0 CLI](https://github.com/HolmesGPT/holmesgpt/blob/046ffdef466700a6ce5cb4fea43875e6dfdee487/holmes/main.py#L445-L538) [0.35.0 server](https://github.com/HolmesGPT/holmesgpt/blob/046ffdef466700a6ce5cb4fea43875e6dfdee487/server.py#L475-L755)

Holmes 可以通过服务端配置的 generic HTTP connector 访问 Alertmanager：connector 支持 host/path/method 白名单和 basic/bearer/header auth。但它会生成一个“由 LLM 决定是否调用”的 tool，不是 API 入口的确定性前置处理。[官方 HTTP connector 文档](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/docs/data-sources/api-toolsets.md#L1-L76) [tool 生成语义](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/docs/data-sources/api-toolsets.md#L220-L224)

## AlertLens 可用的 API 契约

AlertLens 现在可继续使用 `POST /api/chat`：

- `ask`：传入已验证、已限长和清理的 current-alert snapshot 及 Slack 上下文；
- `additional_system_prompt`：说明 active alert 已由 AlertLens 验证，并维持 read-only 约束；
- `request_source: "alert_investigation"`、`source_ref`、`conversation_id`：仅用于分类、引用和会话关联，它们不会触发 Alertmanager 查询。这些字段在官方 schema 中也被定义为前端提供的标签/不透明引用。[官方 schema](https://github.com/HolmesGPT/holmesgpt/blob/ab8896aee2d53dcbfc80646eeecb667a6b9da32c/holmes/core/models.py#L211-L267)

若硬要让 Holmes 自己查，当前只能在服务端配置 GET-only Alertmanager HTTP connector，再用 `ask`/`additional_system_prompt` 要求模型调用它；`response_format` 可要求 `{verified, reason, analysis}` 结构。但 `verified` 仍是模型输出，不是 Holmes API 保证的证据，不应作为 hard gate。

## 方案对比

| 维度 | AlertLens 先查 Alertmanager | Holmes 查 Alertmanager |
|---|---|---|
| 硬前置条件 | 确定性分支；查询失败/零匹配都能在 LLM 前结束 | 现有 HTTP API 无原生支持；connector 是模型可选 tool call |
| Alert Identity | 可用 `alertname + namespace` 精确匹配现有 marker | CLI 面向所有 alerts/通用 filter；connector 还需要模型正确构造 filter |
| 失败语义 | 能区分 transport/API 失败和零匹配，并映射为 `x` | `/api/chat` 的成功响应可能只在 `analysis` 里描述 tool 失败 |
| 可观测性 | Alertmanager 与 Holmes 请求指标分开 | 查询失败易与 RCA 失败混在一次 chat 中 |
| 配置/凭证 | AlertLens 多一个网络依赖，并自己处理 auth | Holmes connector 可集中凭证和 endpoint 白名单 |
| 与 VictoriaMetrics/Logs 协作 | 验证后 Holmes 仍可用已配置 tools 做深度 RCA | 同一 agent 内可查三个数据源，但对“是否允许开始 RCA”没有额外保证 |
| 改动量 | 复用 AlertLens 现有 client，加一个 guard | 需要 connector/prompt/结构输出约定，或上游新增确定性 API |

## 决策建议

保留分工：

1. AlertLens 用现有 Alertmanager client 完成 Active Alert Verification；查询错误或零匹配都不调用 Holmes。
2. 验证成功后，AlertLens 把当次 snapshot 与“已验证 active”的明确指令发给 `/api/chat`。
3. Holmes 专注使用 VictoriaMetrics、VictoriaLogs、Kubernetes 等 tools 做 RCA。

只在 Holmes HTTP API 将来提供一个**非 LLM 控制**的 Alertmanager investigation/verification 入口，且契约明确区分“查询失败”、“零匹配”、“验证成功后的 RCA 失败”时，再重新考虑把责任下沉到 Holmes。
