# Chat HTTP Transport Boundary 分层复盘

**日期**：2026-07-27
**关联任务**：T068、T088；为 T090/T095 建立边界
**关联模块**：`api/v1/chat`、`internal/controller/chat`、`internal/logic/chat`、`pkg/ai/llm`
**状态**：已修复 / 已复盘

## 发生了什么

T088 首次实现 chat HTTP 契约时，`api/v1/chat/chat.go` 不只包含 GoFrame `Req/Res` DTO，还同时承担了严格 JSON 解码、UTF-8 与 32KiB 校验、`gvalid` 的执行逻辑、debug eval summary 的低敏策略、错误 envelope 构造和 `MarshalJSON` 状态码约束。

这些行为虽然能让单个 API 包测试通过，但会让 `api/v1` 从“公开契约”变成“HTTP adapter + 安全策略实现”。如果后续 `internal/logic/chat` 为了取得这些 helper 或 DTO 而导入 API，就会形成 `logic -> api` 的反向依赖；LLM 领域事实、HTTP 状态和 JSON 细节也会被混入同一个模型。

## 理论误区

误区不是“DTO 不应有任何类型约束”，而是把公开 wire shape 的定义与传输行为、领域决策混为一层。GoFrame 的 API 目录可以声明路由和数据形状，但 HTTP body 如何读取、未知字段是否拒绝、错误如何分类、是否输出 debug 诊断，都是 controller 作为 adapter 的职责。

另一个误区是把 provider 的返回值直接当作 API 返回值。`pkg/ai/llm` 必须保留 provider 的原始、可扩展事实；logic 负责把它组织为领域结果；HTTP API 则只能公开版本稳定、低敏且与 OpenAPI 一致的投影。三层共享同一个 DTO 会使 provider 新增值或内部错误详情意外成为对外兼容承诺。

## 工程根因

1. T088 先关注 OpenAPI schema 与安全边界，缺少“实现应位于哪一层”的显式守卫。
2. 严格 JSON 解码不属于 GoFrame 默认 DTO binding，因而被放到了最容易拿到 `ChatReq` 的 API 包。
3. 为了防止 error data、AI identity 和 eval summary 的非法组合，最初把构造器和自定义 JSON marshaler附着在 DTO 上；这保护了输出，却模糊了 controller 的 mapper 责任。
4. 先行的 T090/T095 只有 RED 测试而无生产实现，容易让当前 T088 为未来 controller/logic 提前承载职责。

## 修复

- `api/v1/chat` 收敛为 route metadata 与公开 DTO；`Message` 仅通过 `v:"required"` 声明必填契约，不再定义 decoder、validator、factory、`MarshalJSON` 或全局 validator 注册。
- 在 `internal/controller/chat/contract.go` 显式执行 GoFrame `gvalid` 的声明式必填规则，并集中实现严格 JSON 输入边界：拒绝未知字段、尾随 JSON、无效 UTF-8、空白和超过 32KiB 的消息。`gvalid` 的长度规则以 Unicode 字符计数，不能替代本接口的字节预算。
- controller contract factory 负责安全地构造成功、AI 前错误和 AI 后错误 envelope：错误 data 固定为 `null`，AI identity 按失败时机保留或省略，debug eval summary 只接受低敏 allowlist 并执行 1KiB 限制与防御性复制。
- 新增 API AST 边界测试，防止 `api/v1/chat` 再次出现行为函数；新增 controller contract 测试，确保传输行为留在 adapter。
- 在 `AGENTS.md` 固化清晰的 import/调用方向：controller 依赖 API DTO 与窄 logic 接口，logic 依赖 `pkg/ai`，logic 不得反向依赖 API。

验证命令：

```bash
go test ./api/v1/chat -count=1
go test internal/controller/chat/contract.go internal/controller/chat/contract_test.go
git diff --check
```

## 学到什么

DTO 的“薄”不是指字段少，而是指它不主动决定如何读取 HTTP、如何调用模型、如何解释 provider 结果或如何处理运行时安全策略。公开 DTO 可以表达稳定的 JSON schema；但从不可信 HTTP bytes 到 command、从领域 result 到 JSON envelope 的转换，应始终落在 controller。

对于 AI 系统，边界尤其重要：provider 保真、领域编排和 API 稳定性具有不同演进速度。让 `pkg/ai/llm` 保留未知 provider finish reason，让 logic 处理领域事实，让 controller 明确映射到公开 enum，能避免上游能力变化直接破坏客户端兼容性。

## 后续预防

- 增加或调整测试：每个新 API 包增加“DTO-only”架构测试；每个 controller 覆盖 strict decode、未知字段、敏感错误不回显和 request/AI identity 时机。
- 增加或调整评估 case：chat eval 的内部 evidence 与 debug summary 必须分别测试，禁止将原始模型输入、输出或 provider body 投影到公开 summary。
- 增加或调整 trace 字段：`ai_trace_id` 由 logic 在模型调用前生成；controller 只能投影已有身份，不能在失败后伪造。
- 增加或调整降级路径：provider/telemetry/eval 的内部失败由 logic 分类，controller 仅映射稳定 HTTP 结果，不回显内部错误文本。
- 需要补充的 ADR / ROADMAP / tasks：T090 实现领域 `ChatCommand/ChatResult` 与 LLM 编排；T095 实现完整 controller/route handler 并复用本次 controller contract helper；若引入第二种传输协议，再为该 adapter 复用领域 command/result，而非复用 HTTP DTO。
