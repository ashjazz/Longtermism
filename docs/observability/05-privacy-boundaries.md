# 隐私边界

**关联任务**：T076
**关联规格**：`specs/002-dual-plane-observability/spec.md`
**状态**：drafted

## 理论概念

观测体系的隐私边界要先区分两类数据面：

- **普通观测面**：日志、span attributes、metrics labels、eval evidence、smoke result、平台 trace UI。它的目标是支持排障、性能分析和回归判断，必须默认低敏。
- **审计/回放面**：需要保存原始 query、完整 prompt、完整模型回答、完整 tool args 或外部响应原文的专门链路。它必须另行设计加密、权限、保留期限、访问审计和删除策略。

Observability v1 的决策是：普通观测面不保存敏感原文，只保存能支持诊断的摘要。常见摘要包括：

- hash：证明内容是否变化，但不暴露原文。
- length：判断输入规模、prompt 膨胀、tool 参数大小。
- category：表达语言、工具类型、安全分类或错误类别。
- count：表达 chunk 数、工具调用次数、命中数量。
- score：表达检索 top score、eval score 或置信度。
- status/error class：表达 success、miss、timeout、loop_detected、budget_exceeded 等稳定状态。

敏感原文包括但不限于：原始用户输入、完整 prompt、完整工具参数、API key、JWT、session token、password、个人隐私、外部 API 响应原文和模型完整回答。它们不应进入普通 trace、日志、span、baggage 或测试失败报告。

## 关键问题

- 这个主题解决什么生产问题？
  它解决“为了排障把敏感原文写进日志或 trace，导致观测平台、CI 输出或跨服务传播面泄露数据”的问题。

- 它在传统后端观测和 AI Agent 观测中有什么差异？
  AI Agent 的 prompt、RAG context、tool args 和模型响应往往包含更多用户隐私、业务机密和外部数据。传统后端日志里一个 query string 可能已足够危险；AI Agent 里完整 prompt 和 tool args 的泄露面更大。

- 哪些信息必须可见，哪些信息必须隐藏？
  必须可见的是低敏关联身份、hash、长度、计数、分数、状态、错误分类和成本摘要。必须隐藏的是原始 query、完整 prompt、完整 tool args、密钥、token、个人隐私、外部响应和模型完整回答。

## 工程实验

本项目用四层机制保护普通观测面：

1. `pkg/ai/obs/safe_summary.go` 定义 `SafeSummary`，只允许 hash、length、category、count、score、status 和 error class 这类低敏摘要。
2. `pkg/ai/obs/redaction.go` 集中扫描 forbidden key/value。扫描结果只返回字段名和原因，不返回命中的敏感值，避免检测报告二次泄露。
3. `pkg/ai/obs/logger.go` 和 `pkg/ai/obs/otel_mapper.go` 在实际输出前扫描字符串字段，避免 raw query、prompt、tool args 或 key 被写入日志和 OTel-style span snapshot。
4. `pkg/ai/obs/baggage_policy.go` 对 baggage 使用更窄的 key allowlist，并复用 value 敏感检测。baggage 是跨进程传播面，因此比普通 span attribute 更严格。
5. `internal/eval/smoke/observability_privacy.go` 组合 logger、span sink、OTel mapper、baggage 和 smoke payload 做端到端扫描，要求敏感原文命中数为 0。

可运行的验证命令：

```bash
go test ./pkg/ai/obs -run 'TestScanForbiddenPayloadFields|TestCrossAdapterPrivacyContractRejectsRawPayload|TestBaggagePrivacy' -count=1
go test ./internal/eval/smoke -run TestObservabilityPrivacySmoke -count=1
```

观察结果应满足：

- 普通观测出口不包含敏感原文。
- baggage 只传播 allowlist 中的低敏关联身份。
- privacy smoke 的泄露报告不回显敏感值本身。
- hash/length/summary 等低敏诊断字段仍然保留。

## 最佳实践

本项目在隐私边界上采用以下实践：

- **出口级防护**：不是只要求领域对象“不存 raw 字段”，而是在 logger、mapper、baggage、smoke payload 等实际出口前防护。
- **baggage 更窄**：baggage 会跨进程传播，只允许稳定低敏身份字段，不允许 prompt/query/tool args 这类业务内容。
- **检测报告低敏化**：发现泄露时只报告 surface、field 和 reason，不报告原始 value。
- **摘要优先**：排障优先使用 hash、length、count、score、status、error class，而不是原文。
- **审计链路另开 ADR**：如果未来确实需要原文留存、回放或人工标注，应设计独立加密审计链路，不放宽普通观测面。
- **新增字符串字段默认可疑**：任何新增到 trace/log/span 的字符串字段，都应说明是否可能承载原文，并接入扫描或改为摘要。

暂不采用的做法：

- 不把完整 prompt、query、tool args 或模型回答写入普通 Langfuse/OTel trace。
- 不通过简单 masking 替代结构化摘要；masking 容易漏掉嵌套结构和非预期格式。
- 不把隐私扫描当成生产级 DLP；v1 是框架级底线，后续真实部署仍需要数据分类、权限和审计。

## 失败模式

隐私边界常见失败方式包括：

- **日志调试泄露**：为了复现问题临时打印 query、prompt、tool args 或 header token。
- **span attribute 泄露**：adapter 为了平台 UI 可读性，把 prompt 或外部响应作为字符串属性上报。
- **baggage 污染**：把业务 payload 放进 baggage，导致下游服务和 collector 都能看到敏感内容。
- **检测报告二次泄露**：测试发现敏感值后，把敏感值写进 failure message 或 smoke result。
- **eval evidence 泄露**：把 sample query、model answer 或 context 原文放进普通 evidence。
- **hash/summary 混用**：字段名叫 hash，实际传入了原文；字段名叫 summary，实际包含完整内容。
- **平台默认采集过宽**：真实平台 SDK 自动采集 prompt、messages 或 tool args，突破核心契约。

## 降级路径

隐私边界的降级原则是“宁可少记录，也不能泄露”：

1. **字段可疑**：丢弃该字段或只记录 error class，不进入普通观测。
2. **baggage 校验失败**：拒绝传播该字段；本地可以记录低敏失败分类。
3. **privacy smoke 命中泄露**：默认门禁失败；报告 surface/field/reason，但不回显原文。
4. **需要原文排障**：通过受控审计链路申请访问，而不是临时放宽日志或 trace。
5. **平台 SDK 自动采集过宽**：关闭自动采集或在 adapter 层过滤；不能让平台便利性反向改变核心隐私契约。

降级不能做的事是把敏感原文改名成 metadata 继续发送，或把检测到的敏感值写入错误消息“方便定位”。

## 复盘问题

- 这次实现证明了哪个理论概念？
  隐私边界必须按输出 surface 建模。领域模型不主动保存 raw 字段还不够，logger、span sink、mapper、baggage 和 smoke result 都是出站边界。

- 哪个字段或边界最容易被误用？
  baggage 最危险，因为它会跨服务传播；`tool_name`、`model`、`requested_model` 等普通字符串字段也可能被错误塞入外部响应或参数原文。

- 如果线上出问题，应该先看哪条记录？
  先看 privacy smoke 或 adapter contract 失败的 surface/field/reason，再回到对应出口的字段映射，确认是否应该改为 hash/summary 或直接丢弃。

- 后续阶段需要补什么能力？
  需要在真实 Langfuse/OTel 平台 smoke 中验证 SDK 不自动采集敏感 payload，并为原文审计/回放单独设计加密链路。

## 关联任务 / 测试

- 任务：T013-T014、T061-T071、T076
- 测试：
  - `pkg/ai/obs/safe_summary_test.go`
  - `pkg/ai/obs/redaction_test.go`
  - `pkg/ai/obs/privacy_contract_test.go`
  - `pkg/ai/obs/baggage_privacy_test.go`
  - `internal/eval/smoke/observability_privacy_test.go`
- ADR：
  - `docs/adr/0007-dual-plane-observability-evaluation-v1.md`
- Journal：
  - `docs/journal/0007-observability-privacy-boundary.md`
