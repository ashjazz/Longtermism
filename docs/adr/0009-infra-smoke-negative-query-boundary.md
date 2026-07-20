# ADR-0009：Infra Smoke 的 AI 平面负向查询边界

**日期**：2026-07-20
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

T064D 的 infra runner 已把“Langfuse 中没有本次纯基础设施 marker”和“AI 平面没有接收该 marker”建模为两个负向 count 端口。然而平台查询协议、认证引用和响应边界尚未固定；若 CLI 直接解析平台 JSON、复用 ingest/write 凭据或把失败当成零记录，便会制造无法审计的假阳性。

US1 需要证明纯 infra 请求不会进入 AI 平面，但不承担真实 chat、generation 或 score 的正向闭环；这些仍属于 US2。默认门禁也必须继续离线，不能因为新增真实查询而默认外连。

## Decision（决策）

infra smoke 的 runner 只消费两个 count-returning backend ports：`langfuse_trace` 与 `collector`。具体协议、认证和原始响应只允许存在于 `internal/observability/backend` adapter；CLI、runner、报告和业务代码只处理非负 count 与既有稳定 error class。

两个查询端口分别配置 base-URL 与 credential 的环境变量**名称**，采用项目级只读最小权限，不复用 ingest、score write 或管理权限。每次查询精确绑定 runner 生成的 marker 与当前最多 60 秒窗口；成功零计数才是负向证据，任何配置或查询失败都必须 fail-fast 或生成失败报告。

T065B 通过 `httptest` 才决定实际 Langfuse/AI-plane API 路径、请求参数和受限响应字段。T065A 不把 SDK、REST 路径或平台 schema 写入核心/CLI 契约。

## Alternatives Considered（备选方案）

### 方案 1：CLI 直接调用平台 API 并解码响应

- **优点**：初期文件数量少，能快速发起查询。
- **缺点**：CLI 会持有 endpoint、认证、JSON schema 和错误文本，容易泄露并与后端绑定。
- **未采用原因**：违背平台 adapter 边界，也绕开 T063/T064C 的 raw-response 隔离。

### 方案 2：缺少配置或查询失败时视为零命中

- **优点**：本地命令看似更容易通过。
- **缺点**：把平台不可用、认证失败和路由错误伪装成“未进入 AI 平面”。
- **未采用原因**：生产验收必须由证据驱动；失败不是负向证据。

### 方案 3：复用 Langfuse ingest 或管理员凭据

- **优点**：少管理一组环境变量。
- **缺点**：读诊断获得不必要的写入或管理权限，轮换和审计边界不清楚。
- **未采用原因**：采用独立、项目级、只读的查询凭据引用。

## Consequences（影响）

### 正面影响

- runner 的负向语义保持平台无关，后端可替换且报告只保存低敏 count。
- 认证、窗口、响应上限与稳定错误类在实际 HTTP 实现前已有可测试契约。
- 纯 infra 路由的反证与 US2 的 AI 正向证据保持清楚的责任边界。

### 负面影响

- 需要维护独立只读凭据和两个查询端口的配置校验。
- T065B 必须通过受控测试确认平台协议，不能依赖未经验证的记忆或 UI 行为。

### 风险

- **SSRF 或敏感数据泄露**：只接受受控 profile endpoint、禁止 redirect，限制 1 MiB 响应，并在 adapter 内丢弃 raw data。
- **并发流量干扰负向判定**：查询只匹配本次 runner 生成的 marker；不以高基数 metric label 替代它。
- **协议漂移**：API path/schema 被隔离在 adapter 合同，变更时更新 T065B/T065C，而非污染 runner 或 CLI。

## References（参考）

- ADR-0008：真实可观测后端接入与最小 HTTP 观测闭环。
- [Runtime Configuration Contract](../../specs/003-real-observability-backends/contracts/runtime-configuration.md)。
- [Langfuse Public API](https://langfuse.com/docs/api-and-data-platform/features/public-api)。
- [Langfuse Observations API](https://langfuse.com/docs/api-and-data-platform/features/observations-api)。
