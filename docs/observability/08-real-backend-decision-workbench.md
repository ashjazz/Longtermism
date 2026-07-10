# 真实可观测后端接入决策工作台

**关联规格**：`specs/003-real-observability-backends/spec.md`
**关联 ADR**：`docs/adr/0006-observability-adapter-boundary.md`、`docs/adr/0007-dual-plane-observability-evaluation-v1.md`
**状态**：discussion
**创建日期**：2026-07-09

## 目的

本文是 Observability v1 进入“真实可观测组件 + 真实后端服务 + 最小 HTTP API 闭环”前的临时决策工作台。

它不是最终 ADR，而是用来承载讨论中的问题、候选方案、当前倾向、未决风险和后续裁决入口。等关键选择稳定后，再把结论沉淀为新的 ADR 或修订 ADR-0007。

本阶段的核心问题不是“接哪个平台最快”，而是：

1. 如何把基础设施观测、AI 语义观测和评估证据接到真实后端。
2. 如何保持 `pkg/ai` 核心事实模型不被平台 schema 绑架。
3. 如何通过一个最小 HTTP 端点证明全流程观测闭环。
4. 如何让后续 chat、RAG、tool、MCP、context compression 和 sub-agent 能自然接入同一套观测体系。

## 已有项目约束

- 本项目采用双平面观测：基础设施观测平面和 AI 语义观测平面职责分离。
- `pkg/ai/obs.Trace` 是 AI 语义源模型；平台 adapter 只负责映射。
- 默认验证必须离线可运行；真实平台只做显式 opt-in smoke。
- 原始 query、完整 prompt、完整 tool args 和模型输出只能按显式 payload policy 进入观测链路；密钥、token 和已识别的 PII 在任何模式下都不得导出。
- 观测上报失败不得影响主业务流程。
- 本阶段仍以观测与评估驱动 Harness 演进，而不是一次性建设完整 LLMOps 平台。

## 决策 1：应用内可观测组件职责

**当前状态**：已形成当前共识，后续沉淀到 ADR。

### 确认的分层

| 层级 | 候选/组件 | 主要职责 | 当前倾向 |
| --- | --- | --- | --- |
| 框架自动埋点 | GoFrame tracing / gtrace / glog / gdb / gredis | 为 HTTP server/client、log、ORM、Redis 等基础设施行为自动产生或关联基础 span/log | 采用 |
| 遥测 API | OpenTelemetry API | 为库、框架和手工埋点提供稳定接口，例如 tracer、span、context、baggage | 采用 |
| 遥测 SDK | OpenTelemetry Go SDK | 在应用入口装配 TracerProvider、MeterProvider、resource、sampler、processor、exporter 和 shutdown | 采用 |
| AI 语义模型 | `pkg/ai/obs.Trace` / `Tracer` | 表达 generation、retriever、tool、agent、evaluator 等 AI 事实 | 保持核心 |
| 平台映射 | OTel/Langfuse adapter | 将核心 AI 语义映射到 span/event/attribute 或平台 observation | 采用 adapter，不污染核心 |
| 旧 tracing API | `opentracing-go` | 历史 tracing API | 不用于新实现 |

### 确认的理解

GoFrame 与 OTel SDK 不是二选一关系。

- GoFrame 是基础设施埋点来源，负责“哪些框架动作应该留下事实”。
- OTel SDK 是应用内遥测管道，负责“这些事实如何采样、批处理、加 resource、导出和 shutdown”。
- OTel API 是埋点接口标准，适合库/框架/业务代码依赖。
- OTel SDK 是 API 的运行时实现，适合应用入口装配。
- `pkg/ai/obs.Trace` 继续作为 AI 语义源模型，避免 generation、retriever、tool、agent 和 eval 字段散落为平台专属 attributes。
- 平台 adapter 只做映射和出口控制，不反向决定核心事实模型。
- 新实现不引入 `opentracing-go`；如遇历史依赖，只作为迁移兼容问题处理。

### 实现细化项

- **Trace 初始化优先复用 GoFrame contrib trace**。这与使用 OTel Go SDK 并不互斥：GoFrame contrib 本身也是围绕 OTel provider/exporter 做装配。实现上应采用 GoFrame-first 的启动路径；只有当 GoFrame contrib 无法表达必要配置、fan-out、resource 或测试替身时，才在 `internal/cmd` 封装窄接口补充 OTel SDK 装配。单个进程内必须避免重复注册多个互相竞争的全局 TracerProvider。
- **Logs 第一阶段继续使用 GoFrame `glog` + trace id 关联**。OTel logs pipeline 暂不作为 v1 必做项；后续如果接入，应作为独立切片验证日志字段、trace correlation 和隐私边界。
- **Metrics 第一阶段先定义核心指标语义，不绑定 Prometheus 或任何具体后端**。指标可以包括 request latency、error rate、LLM token/cost、export failure、eval regression 等。业务代码只允许面向 OTel Metrics/Collector 规范暴露指标，绝不允许直接对接 Prometheus、SigNoz、Grafana stack 或其它后端 SDK/API。实际存储、查询和展示后端应在“基础设施观测后端服务”决策中确定，并由 Collector/exporter 层承接。

## 决策 2：基础设施观测后端服务

**当前状态**：已形成当前共识，后续沉淀到 ADR。

### 候选方案

| 方案 | 覆盖面 | 优点 | 风险/代价 | 适合阶段 |
| --- | --- | --- | --- | --- |
| Prometheus + Loki + Tempo + Grafana | metrics/logs/traces/dashboard | 经典、可学习、组件边界清晰、Grafana 生态强 | 组件多，docker compose 和运维复杂度较高 | 学习和生产架构理解 |
| Grafana LGTM/Mimir 栈 | metrics/logs/traces/dashboard | 与 Grafana Cloud/OSS 路线一致，后续大盘和告警自然 | Mimir 对当前小项目可能偏重 | 后续规模化 |
| SigNoz | logs/metrics/traces/exceptions/alerts 一体化 | OTel-native，一体化体验强，部署和理解成本较低 | AI 语义能力不如 Langfuse/Phoenix 专门 | 快速打通基础设施观测 |
| Aspire Dashboard | local OTLP viewer | 轻量、本地开发快、适合 smoke | 不是目标生产观测后端 | 本地调试 |
| Jaeger/Zipkin | traces | 简单专注 traces | 不覆盖 logs/metrics，AI 语义弱 | 临时 trace 验证 |

### 当前共识

基础设施观测后端采用“主线方案 + 备选方案”的支持策略：

1. **主线方案**：`OTel Collector + Prometheus + Loki + Tempo + Grafana`。
   - 这是本框架优先验证、优先文档化、优先提供 compose 示例和 dashboard 模板的经典路线。
   - 选择理由：组件职责边界清晰，能系统学习 metrics、logs、traces、dashboards、alerts 和 retention 的生产分工。
   - 阶段边界：可观测基础设施与接入层面要在经典路线中扎实落地，包括 Collector、Tempo、Prometheus、Loki、Grafana、`glog` 到 Loki 的本地采集示例、首批 dashboard 和 smoke/checklist。业务能力的全面覆盖可以按 V1/V2 分阶段推进，但观测栈接入不能只停留在 trace demo。

2. **备选方案**：`OTel Collector + SigNoz`。
   - 这是受支持的用户备选路线，用于提供更一体化的 logs/metrics/traces 使用体验。
   - 选择理由：SigNoz 对 OTel-native 工作流友好，适合希望降低多组件部署和 UI 整合成本的用户。
   - 支持边界：应用层不出现 SigNoz 专属埋点或 SDK；通过 Collector/exporter 配置切换。文档应说明 SigNoz 接入方式、能力差异和验证 smoke，并维护专门的 dashboard/checklist。实施计划以 Grafana 经典路线优先，SigNoz 任务优先级靠后。

无论选择主线还是备选，应用侧都只面向 OTel API/SDK 和 OTel Collector。Prometheus、Loki、Tempo、Grafana、SigNoz 都是 Collector/exporter 之后的后端服务，不允许业务代码直接对接。

### 实现细化项

- 仓库内提供两个 docker compose profile：`observability-grafana` 和 `observability-signoz`，方便用户在两种方案下小规模快速验证项目能力。
- Grafana 主线首批 dashboard 同步覆盖基础设施指标和 AI 关键指标：request/error/latency、export failure、AI trace correlation、LLM token/cost、eval score/regression。
- SigNoz 备选维护专门的 dashboard/checklist 和 smoke 验证，但实施排期低于 Grafana 经典主线。
- Loki 日志接入第一阶段提供 `glog` 到 Loki 的本地采集示例，确保经典路线具备 logs/metrics/traces 的完整接入路径。

## 决策 3：AI 语义观测后端服务

**当前状态**：已形成当前共识，后续沉淀到 ADR。

### 候选方案

| 方案 | 定位 | 优点 | 风险/代价 | 当前倾向 |
| --- | --- | --- | --- | --- |
| Langfuse via OTLP/HTTP | LLM trace、generation、session、score、prompt/eval 后续能力 | AI 观测产品成熟，支持 OTLP endpoint，适合 Go 通过 OTel 接入 | 需要严格控制 input/output 原文和属性映射；不要污染核心模型 | 首选 |
| Arize Phoenix | AI tracing、eval、dataset、experiment | eval 和 experiment 工作流强，基于 OTel/OpenInference | Go 一等 SDK/自动集成相对有限，可能更适合作对照平台 | 备选/对照 |
| Grafana AI Observability | AI stack 观测，和 Grafana 生态整合 | 与基础设施观测同一 Grafana 体系，Go thin SDK 路线有吸引力 | 当前仍需关注 preview/成熟度和平台绑定风险 | 观察/后续评估 |
| OpenLIT/OpenLLMetry/Traceloop | OTel-native GenAI instrumentation | 自动采集和标准化 GenAI 指标 | 可能与本项目自建 `obs.Trace` 源模型重叠，需避免双重事实源 | 谨慎评估 |
| 自建 AI 观测 UI | 完全可控 | 语义贴合 | 成本过高，偏离当前目标 | 暂不采用 |

### 当前共识

- **AI 语义观测后端采用 Langfuse**。Langfuse 是平台出口和分析界面，不是领域事实源；`pkg/ai/obs` 与 `pkg/ai/eval` 继续拥有 AI trace、score 和 evidence 的核心语义。
- **正式拓扑通过 OTel Collector fan-out 到 Langfuse OTLP/HTTP endpoint**。应用只维护一个 OTLP 出口，Langfuse endpoint、Basic Auth、批处理、重试、路由和第二道字段过滤由 Collector 管理。Langfuse 当前只接收 OTLP/HTTP，不使用 OTLP/gRPC 作为 Collector 到 Langfuse 的出口。
- Collector 不得只筛出孤立的 generation/tool span 发往 Langfuse。Langfuse 的 span-level filtering 可能造成不完整 trace，并要求 root span 正确创建 trace；实现时需要稳定的 AI 平面路由标记，并确保对应 root/bridge span 与 AI 子 span 一起进入 Langfuse pipeline。
- **另保留一个直连 Langfuse endpoint 的诊断 smoke**。它只用于隔离验证应用侧 OTel 映射、Langfuse attributes、凭据和 endpoint 是否正确，不是常规运行拓扑，也不能让业务进程长期维护第二套平台出口。
- **开发学习阶段允许受控记录 input/output 原文**，使 Langfuse 中的 generation 和完整调用链可读；但原文采集必须经过统一 payload policy，不能由各 adapter 或调用点自行决定。
- **敏感信息检测不能由全局 `is_debug` 关闭**。`is_debug` 可以影响本地开发的默认 payload mode，但密钥/token 检测、PII 扫描、字段 allow/deny list 和导出前保护在所有模式下都必须存在。生产环境不得仅因 `is_debug=true` 就允许原文外发。
- **eval evidence 同步写入 Langfuse score**。本地 eval evidence 仍是可回归、可审计的事实记录；Langfuse score 是平台投影。同步失败不得影响业务和本地评估结果，并且必须记录 export failure。

### 直连 smoke 与 Collector fan-out 的职责区别

| 维度 | 直连 Langfuse OTLP endpoint | 通过 OTel Collector fan-out |
| --- | --- | --- |
| 主要目的 | 最短路径验证 Langfuse ingestion 和 attribute mapping | 正式运行、统一治理和多后端路由 |
| 应用配置 | 应用持有 Langfuse endpoint 和认证头 | 应用只持有 Collector endpoint |
| 后端切换 | 需要修改应用出口配置 | 修改 Collector exporter/pipeline 即可 |
| 多后端上报 | 应用侧需要多个 exporter 或特殊装配 | Collector 可将同一批信号 fan-out |
| 过滤与脱敏 | 主要依赖应用内 processor/adapter | 应用内第一道保护 + Collector 第二道保护 |
| 故障诊断 | 链路短，适合确认问题在应用还是 Collector | 组件更多，但具备队列、批处理、重试和出口级指标 |
| 项目定位 | opt-in diagnostic smoke | 默认真实平台拓扑 |

两条路径不是相互竞争的产品方案。直连 smoke 是“校准仪”，Collector fan-out 是“正式管道”。二者必须复用同一套 `pkg/ai/obs -> OTel attributes` 映射，防止 smoke 与正式链路产生两套语义。

### Payload policy 与敏感信息检测

建议把“是否调试”和“允许导出什么内容”拆成两个配置维度：

```yaml
app:
  environment: local # local / test / staging / production
  is_debug: true

observability:
  payload:
    mode: content_redacted # metadata_only / content_redacted / content_raw
  sensitive_data:
    on_match: redact # redact / drop_field / drop_export
```

- `metadata_only`：只导出 hash、length、category、status、token、cost、score 等低敏事实。
- `content_redacted`：导出经过检测与脱敏的 input/output，作为开发与一般环境的推荐模式。
- `content_raw`：仅允许在 `local/test` 显式启用，用于自托管小规模学习和使用合成数据的 smoke；检测器仍运行，命中禁止项时仍执行 `on_match`，不存在“debug 就跳过扫描”的旁路。
- 敏感检测是 exporter 前的强制处理阶段，不提供通用 `enabled=false` 配置。单元测试如需替身，应注入 deterministic scanner，而不是绕过该阶段。
- `is_debug` 只控制日志级别、诊断信息及本地默认值。最终 payload mode 必须由独立配置显式确定，并在启动时校验环境组合；`production + content_raw` 应 fail fast。
- 敏感检测应在应用内进入 exporter 之前完成，Collector 再做基于属性键和规则的第二道过滤。Collector 适合确定性字段删除，不应被当成理解 prompt 语义和识别全部 PII 的唯一防线。

### Langfuse score 同步边界

- OTel/Collector 管道负责 traces；Langfuse score 是平台评价对象，不是标准 OTLP trace/metric/log 信号。
- `pkg/ai/eval` 先产生平台无关的 `EvalEvidence`/score 事实并完成本地持久化或 report，再由 Langfuse score adapter 通过 Langfuse API 投影到对应 trace、observation、session 或 dataset run。
- score adapter 必须使用稳定 correlation identity，禁止通过名称或时间窗口猜测归属。Langfuse 通过 OTLP 接入时，其 trace id 与 OTel trace id 共享；写 observation-level score 时必须同时提供 trace id 和 observation id。核心事实至少保留 `service_trace_id`/OTel trace id、可选 OTel span id、`ai_trace_id`、`eval_run_id`、dataset identity、score name、data type、value、evaluator/version 和 evidence reference，再由 adapter 完成平台 ID 映射。
- score 同步采用非阻塞、可重试、幂等的出口语义；平台失败只产生 export failure，不改写本地 eval 结果，也不阻断 HTTP 响应。
- Langfuse 专属 score 字段和 API 调用只存在于 adapter/infrastructure 层，不进入 `pkg/ai/eval` 核心模型。

### 后续对照项

- Phoenix 可作为后续对照实验平台，验证 OpenInference/eval 工作流是否更适合某些场景。
- Grafana AI Observability 作为“更高整合度”候选继续观察，但不应在 v1 把核心绑定到它。

## 决策 4：Collector 与 fan-out 拓扑

**当前状态**：已形成当前共识，后续沉淀到 ADR。

### 候选拓扑

```text
App -> OTel Collector -> Infra backend
                     -> AI backend
```

或：

```text
App -> Infra backend
App -> Langfuse
```

### 已确认原则

优先采用 Collector fan-out：

- 应用只维护一个 OTLP 出口。
- 后端替换和多路上报在 Collector 层完成。
- 可以在 Collector 层做 filtering、redaction、sampling、routing。
- 更符合“平台是出口，不是领域事实源”。
- app 内先执行 payload policy 和敏感信息保护，Collector 再做第二道确定性字段过滤。

### 两个 pipeline 是否会重复

两个下游 pipeline 会有“数据经过上的交叉”，但不等于产生两份领域事实：

- 这里的“两条 pipeline”特指两个 trace 下游分支。Collector 的 traces、metrics、logs 是不同信号类型的 pipeline；Prometheus metrics 与 Loki logs 按各自 pipeline 处理，不需要进入 Langfuse 分支。
- 对 AI API，请求同时具有 HTTP latency/error 等基础设施事实，以及 generation/tool/eval 等 AI 语义事实。因此同一个 OTel trace 可以在 Tempo 和 Langfuse 中形成不同投影，这是有意的双平面关联，不是重复埋点。
- 对纯基础设施 API，完整 trace 进入 infra pipeline；数据即使在 Collector 内短暂进入 AI 分支，也会因为缺少 AI 平面路由标记而被过滤，不会发送到 Langfuse。
- 公共 receiver 将数据 fan-out 给多个 pipeline 时可能产生 Collector 内部的数据共享或复制开销；只要同一 exporter 没有在多条路径中重复配置，就不会向同一后端重复写入。
- AI 分支过滤必须保留带 AI 平面标记的 root/bridge span 和 semantic child spans，不能只保留孤立 generation span。

### Pipeline 组织候选

| 方案 | 拓扑 | 优点 | 风险/代价 | 当前建议 |
| --- | --- | --- | --- | --- |
| A. 单 pipeline、多 exporter | `OTLP -> common processors -> Tempo + Langfuse` | 配置最少 | 所有纯基础设施 span 也会进入 Langfuse；无法为两个后端采用不同字段策略 | 不采用 |
| B. 两个 sibling pipeline 共享 receiver | `OTLP -> traces/infra` 与 `OTLP -> traces/ai` | 直观；纯基础设施和 AI 出口职责明确 | common processors 容易重复配置和漂移；receiver fan-out 后两个分支分别处理 | 可用于最小原型 |
| C. ingress pipeline + forward connectors | `OTLP -> traces/ingress -> forward/infra + forward/ai`，再进入两个下游 pipeline | 公共 enrichment、memory limit 等只做一次；下游独立过滤、batch、queue、retry 和 exporter | 配置比 B 多一层；需要理解 connector | **采用** |
| D. 两个 Collector 实例或两级 gateway | app/agent Collector -> infra gateway 与 AI gateway | 故障、扩缩容和凭据隔离最强 | 部署和运维成本高，不适合当前小规模 v1 | 后续生产规模化 |

采用方案 C，其逻辑结构为：

```text
App
  -> OTLP receiver
  -> traces/ingress
       -> common processors
       -> forward/infra
            -> traces/infra
            -> infra-safe transform + batch + queue/retry
            -> Tempo
       -> forward/ai
            -> traces/ai
            -> AI-plane filter + Langfuse mapping + batch + queue/retry
            -> Langfuse OTLP/HTTP
```

该方案在 Collector 配置中严格来说包含 3 条 trace pipeline：1 条 ingress pipeline 和 2 条 downstream pipeline；“infra/AI 两条 pipeline”指的是两个最终出口分支。

其中：

- `traces/infra` 接收所有请求，包括没有进入 AI 业务的普通 API。
- `traces/ai` 只导出带稳定 AI 平面标记的完整 AI trace 子树。
- 公共 processors 只放两个平面都需要且不会改变平台语义的处理，例如 memory limiting、基础 resource normalization。
- backend-specific attributes、字段删除、采样和 batch/queue 放在各自下游 pipeline，防止一个平面的策略污染另一个平面。

### Exporter 失败归因候选

#### 方案 1：按 Collector component identity 观测内部指标

为出口使用稳定且可读的 component id，例如：

```text
otlp/tempo
otlphttp/loki
otlphttp/langfuse
```

按 exporter component 分组观察：

- `otelcol_exporter_sent_spans`
- `otelcol_exporter_send_failed_spans`
- `otelcol_exporter_enqueue_failed_spans`
- `otelcol_exporter_queue_size`
- `otelcol_exporter_queue_capacity`

优点是直接利用 Collector 自观测能力，可以区分 Tempo、Loki 与 Langfuse OTLP 出口。局限是“send failed”可能仍在重试，不能单独等同于最终数据丢失。

#### 方案 2：每个 exporter 独立 queue/retry 与告警

- Tempo、Loki 与 Langfuse OTLP exporters 分别配置 sending queue、retry、超时和独立告警。
- queue 长时间接近 capacity 表示后端持续变慢；enqueue failed 或重试耗尽才接近实际丢数。
- 本地/生产允许时，为关键出口采用 persistent queue，Collector 重启后继续发送。

该方案同时解决故障隔离和短时后端不可用，是方案 1 的必要配套，而不是替代品。

#### 方案 3：端到端 canary/smoke

周期性发起带唯一 marker 的低敏 synthetic request，使其产生 trace、log 和 AI semantic observation，然后分别查询 Tempo、Loki 和 Langfuse：

- Collector 指标成功但后端查不到数据，可发现认证、租户、索引、映射或查询侧问题。
- canary 分别记录 Tempo trace、Loki log 与 Langfuse AI delivery 结果，避免一个后端成功掩盖另一个失败。

这是最接近“用户真的能在 UI 中看到数据”的验证，但不适合作为高频监控。

#### 方案 4：物理隔离 Collector

基础设施与 AI 后端由不同 Collector 实例负责，天然区分故障域。它适用于不同团队、凭据边界、网络区域或独立扩缩容需求；当前阶段成本过高。

### Exporter 失败归因共识

v1 采用“方案 1 + 方案 2”：

1. Collector 内部指标按 exporter component 分开。
2. Tempo、Loki 与 Langfuse OTLP exporters 使用独立 queue/retry。
3. dashboard 按 Tempo、Loki 和 Langfuse exporter component 分别展示 sent/failed/queue 指标。
4. Langfuse score 不经过 Collector，其失败由独立 score adapter 指标记录，例如 `eval_score_export_total{backend="langfuse",status="failed"}`，不能混入 Langfuse OTLP trace exporter failure。

方案 3 的端到端 canary 不作为 exporter 失败归因主机制，但保留为 opt-in 平台 smoke 和验收手段，用于证明 Tempo、Loki 与 Langfuse 中的数据实际可查询。

### AI 平面路由标记共识

统一使用：

```text
longtermism.observability.plane = "ai"
```

- 使用项目自有的 `longtermism.*` namespace，不占用 OpenTelemetry 保留的 `otel.*` namespace，也不把项目路由语义伪装成标准 `gen_ai.*` 语义。
- 标准 AI operation、model、token、tool 等事实继续使用适用的 OTel GenAI semantic conventions；`longtermism.observability.plane` 只负责本项目双平面路由。
- AI usecase 是设置路由标记的唯一业务责任边界。AI usecase 启动时给当前 request root/bridge span 设置该 attribute，再调用 `pkg/ai` 内核。
- `pkg/ai/obs` 的 OTel adapter 给 generation、tool、retrieval、agent、evaluator 等 AI semantic spans 设置同一 attribute，但不自行猜测某个普通 span 是否属于 AI usecase。
- 单进程内通过 `context.Context` 传递当前 trace/span context；跨服务时使用 baggage 传播 `longtermism.observability.plane=ai`，并在下游 AI usecase 边界校验 allowlist 后显式写入 bridge/semantic spans。
- 不把 baggage 无差别复制到所有 DB、Redis、HTTP client span。只标记 request root/bridge span 和 AI semantic spans，避免 Langfuse 接收基础设施噪声。

### Persistent queue 共识

本地 compose 从第一阶段起直接启用 persistent queue，并按生产级标准设计：

- Tempo、Loki 与 Langfuse OTLP exporters 分别配置独立 sending queue、retry 和 timeout，使用 Collector `file_storage` extension 持久化未发送批次。
- 为 Collector storage 配置独立 named volume、最小文件权限、容量边界和明确清理方式；不得依赖容器临时文件系统。
- queue 必须有界，不能用磁盘无限增长掩盖后端长期不可用；同时监控 queue size/capacity、enqueue failed、send failed、Collector 磁盘使用和重启恢复结果。
- 应用内敏感信息保护必须发生在数据进入 Collector 之前。即便本地启用 `content_raw`，密钥、token 和已识别 PII 也不得写入 persistent queue；普通原文可能落盘这一事实必须进入本地数据保留与清理说明。
- smoke 覆盖“暂停后端 -> 形成积压 -> 重启 Collector -> 恢复后端 -> 队列排空并成功送达”，证明持久化不是只写配置而未验证恢复语义。
- permanent error、queue capacity 耗尽和磁盘故障仍可能导致数据丢失；persistent queue 提高短时故障恢复能力，不承诺 exactly-once，也不替代失败指标与告警。

## 决策 5：最小 HTTP API 端点范围

**当前状态**：已形成当前共识，后续沉淀到 ADR。

### 候选路径

| 路径 | 覆盖能力 | 优点 | 风险 | 当前倾向 |
| --- | --- | --- | --- | --- |
| 非流式 chat | HTTP -> prompt -> LLM -> obs -> eval summary -> response | 最小闭环清晰，适合验证链路 | 不覆盖 streaming/tool/RAG | 第一阶段采用 |
| 流式 chat | SSE/stream -> LLM stream -> TTFT/tokens -> obs | 能验证 TTFT 和 streaming 观测 | 实现复杂度上升 | 第二阶段 |
| chat + tool call | LLM -> tool -> tool result -> final | 覆盖 Agent/tool 关键语义 | 需要工具权限、schema、错误处理 | Harness tool 阶段 |
| stream + tool call | streaming tool call / partial args | 接近现代 Agent 真实路径 | 复杂且 provider 差异大 | 后续 |
| RAG chat | query -> retriever -> generation | 观测 retrieval miss、scores、latency | 需要 RAG pipeline 真实化 | RAG 阶段 |

### 当前共识

第一阶段提供两个职责明确的端点：

```text
POST /api/v1/chat
GET  /api/v1/observability/infra-smoke
```

`POST /api/v1/chat` 承载真实 AI 闭环：

```text
HTTP request
  -> request_id / service_trace_id / span_id
  -> AI usecase 设置 longtermism.observability.plane=ai
  -> prompt identity
  -> 真实 OpenAI-compatible LLM provider
  -> AI generation observation
  -> eval evidence summary 或 smoke score
  -> unified response envelope
```

`GET /api/v1/observability/infra-smoke` 只验证基础设施平面：

```text
HTTP request
  -> request_id / service trace/span
  -> glog / HTTP metrics
  -> unified response envelope
```

- 该端点不进入 AI usecase，不创建 `ai_trace_id`，也不设置 `longtermism.observability.plane=ai`。
- 预期结果是 Tempo/基础设施 dashboard 可查询，Langfuse 中不存在对应 trace，用于验证双平面拆分、方案 C pipeline 和 AI usecase 打标职责。
- 该诊断端点只在 `observability.smoke.enabled=true` 时注册或可访问，默认关闭，避免形成生产环境的无认证诊断入口。
- 不复用高频健康探针 `/api/v1/health/ping` 做确定性 smoke，以免健康检查采样、过滤或探针流量影响验证结论。

暂不把 stream、tool、RAG、MCP、context compression、sub-agent 都塞进第一个 chat 端点。它们随着 Harness 能力建设逐步接入，并复用同一套观测模型。

### 真实 LLM 接入共识

- 第一阶段接入真实的 OpenAI-compatible LLM，上游默认模型为 `gpt-5.5`。
- 复用现有 `pkg/ai/llm/openai` Chat Completions adapter，不在 HTTP controller 或 usecase 中直接拼装上游请求。
- 上游契约以 `POST {base_url}/chat/completions`、Bearer API key、OpenAI messages/usage/finish reason 兼容为基线。`base_url` 应配置到包含 `/v1` 的 API 根路径，不能硬编码具体供应商域名。
- `base_url`、API key 和模型名由服务端配置注入；API key 只来自环境变量或密钥管理器，不进入客户端响应、日志、trace 或 eval evidence。
- 对外 `/api/v1/chat` 不允许客户端任意指定 provider/base URL/API key。模型选择第一阶段也由服务端配置控制，避免客户端绕过成本、能力和安全策略。
- 运行真实服务时，chat 能力启用但凭据缺失应在启动阶段 fail fast；默认离线单元测试和 eval fixture 继续使用 deterministic fake provider，不依赖真实上游。
- 第一阶段沿用 Chat Completions 是为了复用现有稳定 adapter；未来需要 Responses API 的 hosted tools、MCP 或更复杂 agent 能力时，再通过 provider/transport adapter 扩展，不改变 AI usecase 契约。

### Response correlation metadata 共识

统一响应 envelope 的 `meta` 中：

| 字段 | 普通/生产模式 | debug 模式 | 理由 |
| --- | --- | --- | --- |
| `request_id` | 始终返回 | 始终返回 | 客户支持、错误回查和基础设施 trace 关联的稳定入口 |
| `ai_trace_id` | AI 请求始终返回 | AI 请求始终返回 | AI 平面回查入口；普通 infra smoke 不生成也不返回 |
| `eval_summary` | 默认省略 | 返回有界、低敏摘要 | 避免把内部 evaluator 结构和异步状态固化为公共 API 契约 |

- `request_id` 同时写入 `X-Request-ID` response header；成功和错误响应都必须可回查。
- `ai_trace_id` 在 AI usecase 开始时生成，因此即使 provider 调用失败，也应随错误 envelope 返回；它必须是 opaque identity，不能包含平台 URL、用户信息或其它业务含义。
- 两个 ID 加上 JSON 字段名通常只是几十到一百余字节，带宽开销可以忽略。真正风险是把内部 eval 结构永久暴露为公共契约，以及泄露不必要的诊断信息。
- debug 下的 `eval_summary` 设置 1 KiB 序列化上限，只包含实际执行的 evaluator 名称、status、score 和低敏原因分类；不返回 prompt、output、evidence 原文或平台内部对象。
- 如果本次请求没有执行 evaluator，应明确返回 `status: "not_run"`，不能伪造默认 score。异步评估结果后续通过独立查询能力或内部平台查看，不阻塞 chat 响应。

## 决策 6：配置、密钥与运行模式

**当前状态**：已形成当前共识，后续沉淀到 ADR。

### 配置所有权原则

应用、Collector 和后端不能共享一个扁平的 `observability.*` 配置对象：

1. **应用配置**只描述如何产生遥测以及如何连接 Collector。
2. **Collector 配置**描述如何接收、处理、分流并写入 Tempo、Loki、Prometheus 暴露面、Langfuse 和 SigNoz。
3. **后端/compose 配置**描述存储、retention、端口、Grafana datasource 和 Prometheus scrape target。
4. **平台 adapter 配置**只承载 OTLP 以外的平台能力，例如 Langfuse score API。

因此，应用侧的 `observability.otlp.endpoint` 不应拆成 Prometheus/Loki/Tempo/Langfuse 地址，而应收紧并重命名为 `observability.collector.endpoint`。具体后端端点必须在 Collector 或 compose 层拆分，不能让业务进程直接认识这些后端。

### 各服务端点并不是同一种东西

| 组件 | 数据方向 | 协议/用途 | 配置归属 |
| --- | --- | --- | --- |
| App -> Collector | 应用写入 Collector | OTLP gRPC 或 OTLP HTTP | 应用配置 |
| Collector -> Tempo | Collector 写 traces | OTLP gRPC/HTTP ingestion | Collector exporter |
| Collector -> Loki | Collector 写 logs | Loki native OTLP/HTTP ingestion | Collector exporter |
| Prometheus -> Collector | Prometheus 抓取 metrics | Prometheus scrape endpoint | Collector exporter + Prometheus scrape config |
| Collector -> Prometheus-compatible remote | Collector 主动推 metrics | Prometheus Remote Write，可选生产路径 | Collector exporter |
| Grafana -> Prometheus/Loki/Tempo | Grafana 查询后端 | datasource/query URL | Grafana provisioning |
| Collector -> Langfuse | Collector 写 AI traces | Langfuse OTLP/HTTP ingestion | Collector exporter |
| score adapter -> Langfuse | adapter 写 eval scores | Langfuse API | adapter/infrastructure config |
| Collector -> SigNoz | 备选 profile 写三信号 | SigNoz OTLP ingestion | Collector exporter |

本地经典主线优先使用 Prometheus pull 模式：Collector 暴露 metrics scrape endpoint，由 Prometheus 定时抓取。Prometheus 因而不是和 Tempo/Loki 完全同形的 `endpoint`。Grafana 更不是遥测出口，它只需要 datasource 地址。

### 第一层：应用配置草案

```yaml
app:
  environment: local
  is_debug: true

observability:
  enabled: true
  mode: collector # noop / local / collector

  resource:
    service_name: longtermism
    service_version: dev

  collector:
    protocol: grpc # grpc / http_protobuf
    endpoint: otel-collector:4317
    insecure: true
    headers_env: OTEL_EXPORTER_OTLP_HEADERS

  signals:
    traces_enabled: true
    metrics_enabled: true
    logs_transport: glog_file # glog_file / otlp

  tracing:
    sampling_ratio: 1.0

  payload:
    mode: content_redacted # metadata_only / content_redacted / content_raw

  sensitive_data:
    on_match: redact # redact / drop_field / drop_export

  smoke:
    enabled: false
```

关键边界：

- 应用只有一个 Collector 地址，不包含 Tempo、Loki、Prometheus、Grafana、SigNoz 的地址。
- `mode=local` 用于离线 sink/测试替身；`mode=collector` 才启用真实 OTLP 出口。使用 `collector` 比 `otlp` 更准确，因为 OTLP 是协议，不是运行模式。
- v1 logs 仍走 `glog`；本地示例通过共享日志卷与 Collector `filelog` receiver 采集，再由 Collector 转成 OTLP logs 发往 Loki。
- `headers_env` 只记录环境变量名称或使用启动期环境插值，配置快照不得包含 header 原值。
- `production + content_raw` 启动时 fail fast；`is_debug` 不直接授权原文外发。

### 第二层：Collector 配置草案

Collector 配置需要分别表达：

```yaml
extensions:
  file_storage:
    directory: /var/lib/otelcol/storage

exporters:
  otlp/tempo:
    endpoint: ${env:TEMPO_OTLP_GRPC_ENDPOINT}

  otlphttp/loki:
    endpoint: ${env:LOKI_OTLP_HTTP_ENDPOINT}

  prometheus/app:
    endpoint: 0.0.0.0:8889

  otlphttp/langfuse:
    endpoint: ${env:LANGFUSE_OTLP_ENDPOINT}
    headers:
      Authorization: ${env:LANGFUSE_OTLP_AUTHORIZATION}

  otlp/signoz:
    endpoint: ${env:SIGNOZ_OTLP_GRPC_ENDPOINT}
```

具体值在 compose profile 中注入，例如：

```text
TEMPO_OTLP_GRPC_ENDPOINT=tempo:4317
LOKI_OTLP_HTTP_ENDPOINT=http://loki:3100/otlp
LANGFUSE_OTLP_ENDPOINT=http://langfuse:3000/api/public/otel
SIGNOZ_OTLP_GRPC_ENDPOINT=signoz-otel-collector:4317
```

并按已经确认的方案 C 配置 ingress/infra/AI pipelines、filter、persistent queue、retry 和 backend-specific transform。上例只表达端点所有权，不是最终可直接运行的完整 Collector 文件。

### 第三层：后端与 Grafana provisioning

经典栈还需要独立配置：

- Prometheus scrape `otel-collector:8889`，并抓取 Collector 自身 `:8888` internal telemetry。
- Grafana Prometheus datasource 指向 `http://prometheus:9090`。
- Grafana Loki datasource 指向 `http://loki:3100`。
- Grafana Tempo datasource 指向 `http://tempo:3200`。
- Tempo、Loki、Prometheus 分别配置本地 volume、retention 和容量边界。
- SigNoz profile 使用自己的服务和 datasource/checklist，不复用 Grafana profile 的内部地址。

### 第四层：Langfuse score 与直连诊断

Langfuse 需要区分三个用途：

1. `LANGFUSE_OTLP_ENDPOINT`：Collector 写入 AI traces。
2. `LANGFUSE_BASE_URL` + public/secret key：score adapter 写入 eval score。
3. direct diagnostic smoke 的 endpoint/credential：仅 smoke 命令使用，不进入常规应用遥测拓扑。

Langfuse score adapter 可以读取平台配置，但只能存在于 infrastructure/adapter 层；AI usecase 和 `pkg/ai/eval` 不得读取 Langfuse 配置。

### LLM provider 配置草案

真实 chat 还需要补齐：

```yaml
ai:
  llm:
    default_provider: openai
    providers:
      openai:
        base_url: ${env:OPENAI_BASE_URL}
        api_key_env: OPENAI_API_KEY
        default_model: gpt-5.5
```

- 配置文件不提供真实 API key 默认值。
- 客户端请求不能覆盖 `base_url`、API key 或 provider。
- chat 能力启用但真实 provider 配置缺失时启动失败；离线测试显式注入 fake provider。

### 传输与采集共识

- **App -> Collector 默认使用 OTLP gRPC**，默认连接 `observability.collector.endpoint`；保留 `protocol=http_protobuf` override，用于网络代理、托管平台或仅开放 HTTP 的环境。
- **本地 Prometheus 使用 pull 模式**，抓取 Collector Prometheus exporter；Prometheus Remote Write 不进入本地 v1 主线，只作为后续生产或远端托管指标后端的备选。
- **`glog` 本地采集采用结构化 JSON 文件 + shared volume + Collector `filelog` receiver**。应用不直接调用 Loki API，Collector 负责解析、补充 resource/trace correlation 并通过 OTLP/HTTP 写入 Loki。

### Langfuse score adapter 共识

第一阶段在应用进程内异步执行，但必须是受控 adapter worker：

- `pkg/ai/eval` 先生成并保存平台无关的本地 evidence，再把不可变 score projection 放入有界队列。
- 使用单个或固定数量 worker 处理发送，禁止每个请求无界创建 goroutine。
- score 使用稳定幂等键，worker 负责有限重试、指数退避、失败分类和 `eval_score_export_total` 等指标。
- 队列满、Langfuse 不可用或重试耗尽时，不阻塞 chat 响应，也不改写本地 evidence；记录明确的 dropped/failed 指标和结构化错误。
- 应用 shutdown 时使用有界超时 flush；不能为等待平台上报而无限阻塞退出。
- v1 不引入 outbox/独立 worker，因此进程崩溃时尚未发送的平台 projection 可能丢失。本地 evidence 仍保留事实，后续当 score 可靠投递成为生产 SLO 时再升级为 outbox/worker。

### 本地配置与密钥共识

- 非敏感 endpoint、端口、profile 内部 DNS、protocol、sampling、retention 等值允许直接写入受版本管理的默认配置或 compose profile，不强制全部环境变量化。
- 机器或部署环境专属的本地 override 文件不得上传到外部仓库，例如使用已加入 `.gitignore` 的 `config.local.yaml`/`.env`；仓库只提交无密钥的 example/default 配置。
- API key、secret key、Bearer/Basic Auth、OTLP authorization headers、数据库密码等凭据无论配置文件是否上传，都不得以明文写入普通 YAML；仍必须通过环境变量、secret file 或密钥管理器注入。
- 启动日志、配置快照、错误响应和 debug 输出只能显示“凭据是否存在”或脱敏摘要，不能输出原值。
- 本地“不上传”是减少误提交的措施，不是密钥安全边界；实现和测试仍需包含 secret scanning 与配置脱敏。

## 决策 7：隐私、采样与数据保留

**当前状态**：采用当前方案，后续沉淀到 ADR。

### 数据分级共识

#### Span attributes 允许项

- OTel resource：service name/version/instance、deployment environment。
- 关联身份：request id、service trace/span id、opaque AI trace/observation id、eval run/sample id。
- HTTP 事实：method、模板化 route、status code、duration；禁止原始 URL query string。
- AI 事实：标准 `gen_ai.*` operation/model/provider/token/finish reason，以及项目 AI plane、prompt version/hash、tool registered name、retrieval count/top score、eval metric/score。
- 失败事实：status、error class、retry/degraded/rate-limit 标记和脱敏 error summary；禁止上游原始 response body。
- correlation identity 可以作为 trace attribute，但 request/user/session/trace id 等高基数字段不得成为普通 metrics label。

#### Langfuse metadata 允许项

- environment、release、feature、prompt identity、model/provider、token/cost、latency、status、error class、tool/retrieval/eval 低敏摘要和 opaque correlation ids。
- input/output 必须进入 Langfuse 专用 input/output 字段并受 payload policy 控制，禁止把原文绕道塞进 metadata。
- metadata key 使用稳定 allowlist，禁止动态使用用户输入作为 key。

#### Baggage allowlist

- `longtermism.observability.plane`
- opaque `request_id`
- opaque `ai_trace_id`
- 必要时的 opaque `eval_run_id`
- `session_id` 只有在不可逆、无业务含义且跨服务确有需要时才允许显式启用。

OTel trace/span context 通过标准 propagation 传播，不重复塞入 baggage。Baggage 在跨服务和第三方调用前必须经过 allowlist；不得携带 user id、tenant name、prompt、query、tool args、score explanation 或任何凭据。

#### 本地安全摘要

普通生产观测默认只记录：

- content hash/fingerprint、length、category。
- count、status、error class、finish reason。
- latency、token、cost、retrieval score、eval score。
- prompt/model/tool/dataset 的稳定 identity/version。

摘要用于聚合和回归，不得提供可还原敏感原文的拼接片段。对高敏内容使用不可逆、带环境隔离策略的 fingerprint，不能把短密码或 token 的普通 hash 当成脱敏。

### Payload policy 共识

- `metadata_only`：生产推荐默认值，只输出低敏事实。
- `content_redacted`：输出经过敏感检测和脱敏的 input/output。
- `content_raw`：仅限 local/test 显式启用，生产环境启动时拒绝；扫描器仍运行，密钥、token 和已识别 PII 仍执行 redact/drop。
- prompt、query、tool args、retrieval content、模型 output 和外部 response 都必须走同一个 payload policy，不允许某个 adapter 私自绕过。
- debug 只影响诊断体验，不关闭扫描器，也不自动开启 `content_raw`。

### v1 完全禁止项

在 logs、traces、metrics、baggage、Langfuse、persistent queue 和 eval evidence 中均禁止：

- API key、password、access/refresh token、Authorization/Cookie header、session secret。
- 数据库 DSN 中的凭据、私钥、证书私钥内容和云服务 secret。
- 已识别的身份证号、银行卡、完整手机号/邮箱等 PII 原值。
- 未经 payload policy 处理的 HTTP body、prompt、tool args、外部 response 和 provider error body。
- 将 request id、user id、session id、prompt hash 等高基数 identity 作为 metrics label。

命中禁止项时按字段策略执行 redact、drop field 或 drop export；观测失败不能把敏感原文写进错误日志作为补偿。

### 采样共识

- local/test/smoke 默认 traces 100% 保留，便于学习、链路核对和双平面验收。
- collector 模式下应用 v1 不根据结果做 head sampling；由 Collector 在完整 trace 到达后执行 tail sampling。
- failure、degraded、rate limited、eval、tool error、retrieval miss、budget exceeded、loop detected 和 exporter diagnostic traces 100% 保留。
- 普通成功 infra/API trace 与普通成功 AI chat 允许分别配置采样比例；比例是部署参数，不写死在业务代码。生产示例必须显式给值，不能依赖隐藏默认。
- AI 与 infra 分支可以采用不同成功采样比例，但同一后端内必须按 trace 一致采样，不能只保留孤立 child span。
- 未来 Collector 水平扩展时，tail sampling 前必须按 trace id 做一致路由。

### Retention 共识

本地 compose 提供明确、可覆盖的默认保留期：

| 数据 | 本地默认 retention | 说明 |
| --- | --- | --- |
| Prometheus metrics | 15 天 | 用于趋势和 dashboard 学习 |
| Loki logs | 7 天 | 日志体量和泄露风险更高 |
| Tempo traces | 7 天 | 支持近期故障回查 |
| Langfuse metadata/redacted traces | 14 天 | 支持 AI 调试和短期对比 |
| `content_raw` 调试数据 | 最长 24 小时 | 使用独立 local/test 环境或项目，便于整体清理 |
| 低敏 eval evidence/report | 90 天 | 支持回归比较，不包含 prompt/output 原文 |

- 所有后端必须配置容量上限和清理策略，禁止依赖无限期默认保留。
- `content_raw` 不得与长期保留的生产项目混用；如果后端无法按记录设置 retention，就使用独立项目、实例或 volume。
- persistent queue 只用于投递恢复，不是长期归档；已发送批次应及时清理，磁盘容量与队列年龄必须监控。
- 生产 retention 由业务、法规、成本和删除请求共同确定，但必须显式配置；本地默认值不能直接视为生产合规结论。
- 删除或清理需要同时覆盖 Tempo、Loki、Langfuse、本地 eval evidence、Collector queue 和备份，不能只删除 Grafana 中的展示。

## 决策 8：完成标准与验证命令

**当前状态**：已采纳。以八类完成证据、分层验证命令和阶段发布门禁作为真实双平面可观测闭环的验收契约。

### 完成定义

本阶段只有同时满足以下八类证据，才可以声明“真实双平面可观测闭环完成”。

#### 1. 默认离线质量门禁

- `gofmt`/`goimports` check、`go mod tidy` diff check、`go build ./...`、`go vet ./...`、`go test -race ./...` 全部通过。
- 覆盖率使用可重复的 `coverprofile` 统计：新增/变更代码整体不低于 80%，AI usecase 不低于 90%，HTTP controller/handler 不低于 70%，纯工具函数不低于 100%。暂时不存在的层级不伪造统计。
- `gosec` 与 secret scanning 通过；本地 override、真实凭据、生成的平台报告不得进入 Git。
- 默认命令不启动 Docker、不访问真实 LLM、不要求 Langfuse/OTel 凭据，也不访问付费服务。
- contract 覆盖 correlation identity、AI plane attribute、OTel mapping、payload policy、baggage allowlist、score projection 和 exporter failure。
- 缺少真实平台配置时必须明确 skip 或 fail fast，不能静默切换到伪造成功。

#### 2. Collector 与配置门禁

- Grafana 与 SigNoz 两套 compose profile 都能通过静态解析和 Collector config validation。
- Grafana 主线包含 ingress、infra、AI 三条 trace pipeline，forward connectors、Tempo/Loki/Langfuse 独立 push exporters、Prometheus metrics exporter 和 `file_storage`。
- App 常规遥测配置只包含 Collector endpoint；扫描应用配置与代码不得出现 Tempo/Loki/Prometheus/Grafana/SigNoz 直连逻辑。Langfuse 只允许在已批准的 score adapter 和 direct diagnostic smoke 配置边界出现。
- Tempo、Loki、Langfuse OTLP exporters 使用独立 persistent queue/retry/timeout，component id 与 dashboard/alert 查询一致。
- Grafana datasource、Prometheus scrape target、retention、volume 和本地配置忽略规则都有可审查配置证据。
- 所有容器镜像使用明确版本或 digest，禁止 `latest`；compose 明确端口、healthcheck、CPU/内存/磁盘预算与端口冲突处理。

#### 3. 纯基础设施平面门禁

命令在隔离的 smoke 窗口中生成低敏 `longtermism.smoke.run_id`，记录 Prometheus route/status 维度的 counter/histogram 基线，再调用 `GET /api/v1/observability/infra-smoke`。在 60 秒超时窗口内：

- API 返回统一 envelope、`request_id` 和 `X-Request-ID`，不返回 `ai_trace_id`。
- Tempo 能按 marker/request id 找到 HTTP root span。
- Prometheus 中模板化 route/status 维度的 request counter 相对基线增加，latency histogram 新增样本；不使用 request id 或 run id 作为 metrics label。
- Loki 能找到带 request id、trace id、method、route、status 和 duration 的结构化 `glog`。
- Langfuse 查询不到该 marker/request id。
- 任何 span 都不包含 `longtermism.observability.plane=ai`。

负向路由需要两类证据：在无并发 AI smoke 的隔离窗口中，Collector AI filter 的 outgoing-items 增量为 0；待 traces pipeline drain window 结束后，Langfuse 仍查不到 run id/request id。不允许只验证 Tempo 有数据。

#### 4. 真实 AI 闭环门禁

调用真实 `POST /api/v1/chat`，使用服务端配置的 OpenAI-compatible GPT-5.5。API/Tempo/Loki/Prometheus/Langfuse trace 验证窗口为 60 秒，应用内异步 Langfuse score 验证窗口为 120 秒：

- API 返回真实模型内容、统一 envelope、`request_id` 和 opaque `ai_trace_id`；debug 模式返回不超过 1 KiB 的低敏 `eval_summary`。
- Tempo 能查到 HTTP root/bridge 和 AI semantic spans 的关联链路。
- root/bridge 与 AI semantic spans 包含 `longtermism.observability.plane=ai`；无关 DB/Redis/普通 HTTP client span 不被误标记。
- Langfuse 能查到对应 trace/generation，model、token、latency、status、input/output policy 和 correlation ids 映射正确。
- 本地 eval evidence 已生成；Langfuse score adapter 使用稳定幂等键异步写入对应 trace/observation。
- Prometheus 能查询 request/error/latency、LLM token/cost-ready、eval/export 指标。
- Loki 日志可通过 request/trace identity 与 Tempo 关联。

#### 5. 隐私与配置模式门禁

- `metadata_only`、`content_redacted`、`content_raw` 三种模式都有正常、边界和禁止项测试。
- `production + content_raw` 在启动阶段失败。
- 使用包含 synthetic API key、Authorization、token、password、PII、prompt、tool args 和 provider error body 的 canary 请求后，所有实际后端查询结果中的未脱敏禁止项命中数为 0。
- baggage 只包含 allowlist 字段，高基数 correlation ids 不进入 metrics labels。
- persistent queue、Collector 日志、应用日志、错误响应和 score worker 失败日志不能成为敏感原文旁路。
- 本地 override 文件不被 Git 跟踪，仓库配置与历史扫描不包含真实凭据。

#### 6. 故障隔离与恢复门禁

分别模拟各个失败域，其归因证据不能混用：

| 故障对象 | 主证据 | 恢复验收 |
| --- | --- | --- |
| Tempo OTLP | `otlp/tempo` exporter sent/send-failed/enqueue-failed/queue | 队列排空，trace marker 可查 |
| Loki OTLP/HTTP | `otlphttp/loki` exporter 指标与 queue | 队列排空，log marker 可查 |
| Langfuse OTLP/HTTP | `otlphttp/langfuse` exporter 指标与 queue | 队列排空，trace/generation marker 可查 |
| Prometheus pull | Prometheus `up`、scrape duration/error 和 target health | target 恢复 `up=1`，指标再次增长 |
| Grafana | datasource health API 和 query error | Prometheus/Loki/Tempo datasource 全部 healthy |
| Langfuse score API | 应用内 score worker queued/sent/failed/dropped | 幂等 score 最终可查，本地 evidence 一直存在 |
| Collector storage | disk/volume health、queue age/capacity、storage extension error | storage 可写，无静默丢失，积压可排空 |

- `/api/v1/chat` 和 infra smoke 的业务结果不因观测后端故障被改写；真实 LLM 自身失败仍按业务错误契约返回，不得被误报为观测失败。
- 对 push exporter，一个 exporter 失败不阻止其它 exporter 成功；Prometheus/Grafana 的 pull/query 故障不伪造 Collector send-failed 证据。
- Tempo、Loki、Langfuse OTLP 后端暂停后产生积压，重启 Collector，再恢复后端；120 秒内对应 persistent queue 排空，marker 最终可查询。
- queue 满、permanent error、磁盘不可写和 shutdown flush 超时时有明确 dropped/failed 指标，不静默丢失。
- Langfuse score worker 队列满、重试耗尽和 shutdown timeout 不阻塞 chat；本地 evidence 仍存在。

Persistent queue 验收只要求 at-least-once 恢复，不声称 exactly-once；score 依靠幂等键避免重复平台对象。

#### 7. Dashboard、retention 与备选后端门禁

- Grafana 自动 provision Prometheus、Loki、Tempo datasource，首批 dashboard 不需要手工点击创建。
- dashboard 同时覆盖 request/error/latency、Collector exporter、LLM token/cost、eval score/regression，并能从指标或日志定位 trace。
- 至少 provision HTTP error rate、exporter send/enqueue failure、queue saturation/age 和 Collector storage pressure 告警规则，并通过可控故障证明规则能进入 firing 与 resolved 状态。
- Prometheus/Loki/Tempo/Langfuse/eval evidence 的 retention 与容量配置符合决策 7；`content_raw` 隔离且最长 24 小时。
- `observability-signoz` profile 仅替换基础设施后端：SigNoz 承载 logs/metrics/traces，Langfuse 仍承载 AI trace/score。`obs-signoz-e2e` 必须同时证明 SigNoz 三信号可查和 Langfuse AI 闭环，并维护独立 dashboard/checklist；不能只证明 compose 能启动。

#### 8. 文档与决策证据门禁

- quickstart 包含环境准备、启动、验证、故障注入、清理 volume 和密钥轮换说明。
- ADR-0008 记录决策 1-8、备选方案、代价和重新审视条件。
- Grafana 与 SigNoz 各有可执行 checklist；命令、配置文件名和实际 Make target 一致。
- journal 至少记录一次真实接入或故障恢复实验，包括现象、根因、指标、恢复和学到的内容。
- spec/plan/tasks 必须更新，解决旧规格中“v1 不新增持久化存储、原文完全不进入观测、只验证单平台 smoke”等与当前决策冲突的描述。

### 验证命令分层

以下命令是本阶段要实现并保持稳定的 Make target 契约，不代表当前 Makefile 已全部具备。当前已有 `test`、`test-race`、`vet`、`eval-smoke`、`obs-smoke`；其余 target 必须随真实后端实施按 TDD 补齐，未实现的 target 不能被文档中的同名草案视为已验收。

#### Level 0：默认离线门禁

```bash
make verify
make obs-contract
make obs-smoke-offline
make eval-smoke
```

目标定义：

```text
verify            -> fmt-check + mod-tidy-check + build + vet + test-race + coverage-check + security-check
obs-contract      -> correlation/mapping/privacy/baggage/config contract
obs-smoke-offline -> 不访问 Docker/LLM/平台的完整离线双平面 smoke
eval-smoke        -> 固定 dataset、eval evidence 与 trace 回链
```

`security-check` 至少包含 `gosec` 和 secret scanning；`coverage-check` 按“默认离线质量门禁”定义的分层阈值判定，失败时输出低于阈值的 package/文件。

#### Level 1：配置与基础设施栈

```bash
make obs-config-check
make obs-platform-smoke
make obs-grafana-up
make obs-stack-health OBS_PROFILE=grafana
make obs-infra-smoke
```

`obs-config-check` 必须同时验证 compose 解析、Grafana/Signoz Collector 配置、Grafana provisioning 和必需环境变量名称，不访问真实服务。

`obs-platform-smoke` 是本地轻量集成命令：以受控 `PlatformSmokeSender` 验证显式启用、低敏最小双平面 payload、关联 identity 和默认 skip；它不启动 Docker、不连接真实 Collector/Tempo/Loki/Langfuse，也不能替代 `obs-grafana-e2e`。

#### Level 2：真实 AI 与双平面 E2E

```bash
make obs-chat-smoke
make obs-langfuse-score-smoke
make obs-privacy-platform-smoke
make obs-grafana-e2e
```

- 这些命令要求显式提供 OpenAI-compatible 上游配置和 Langfuse 凭据。
- 每条命令生成唯一 run id/marker 和起始时间，自动轮询后端 API；Tempo/Loki/Prometheus/Langfuse trace 默认 60 秒，Langfuse score 默认 120 秒，超时返回非零退出码。查询必须限定本次 run 的 marker/时间窗口，不得被旧 smoke 数据误判通过。
- `obs-grafana-e2e` 聚合 infra、chat、score、privacy、metrics/logs/traces 和 dashboard datasource 验证。

#### Level 3：故障与恢复

```bash
make obs-exporter-failure-smoke
make obs-persistent-queue-smoke
make obs-score-worker-failure-smoke
make obs-resilience-e2e
```

故障命令必须自行恢复被暂停的容器，并在退出时报告残留状态；删除 volume 只能由显式的 reset 命令执行。

#### Level 4：备选后端兼容性

```bash
make obs-signoz-up
make obs-stack-health OBS_PROFILE=signoz
make obs-signoz-e2e
```

#### 诊断与清理

```bash
make obs-direct-langfuse-smoke
make obs-status
make obs-grafana-down
make obs-signoz-down
make obs-reset OBS_PROFILE=grafana
```

- `obs-direct-langfuse-smoke` 仅用于隔离 Collector 与 Langfuse ingestion 问题，不属于常规应用拓扑。
- `obs-reset` 是显式破坏性命令，必须要求 `CONFIRM_RESET=1`，先输出待删除清单，且只能删除当前 Compose project 带标签的 observability containers/volumes/raw 调试数据；不得匹配应用数据库或无关 volume。普通 `down` 不删除 volume。

### 门禁运行频率

- **每个 PR**：必须运行 Level 0，不需要 Docker、真实 LLM 或平台凭据。
- **变更 compose/Collector/backend provisioning/dashboard/retention 的 PR**：在 Level 0 之外必须运行 `obs-config-check`；有可用 CI 容器环境时运行 Level 1。
- **本阶段首次完成验收**：Grafana 主线的 Level 2 和 Level 3 必须真实通过一次，不能用 fake provider 替代。
- **后续发布**：只有当变更触及 LLM provider、OTel mapping、Collector pipeline/exporter、Langfuse adapter、privacy policy 或 backend profile 时，才必须重跑真实 E2E；普通业务 PR 不因第三方短时故障被阻塞。
- **定期 canary**：至少在每个 release candidate 和上述配置变更后运行；可另配 scheduled job，失败产生运维告警而非伪装成代码回归。

### 阶段完成与发布门禁

Grafana 主线声明完成前必须依次通过：

```bash
make verify
make obs-config-check
make obs-grafana-e2e
make obs-resilience-e2e
```

SigNoz 支持声明完成前额外通过：

```bash
make obs-signoz-e2e
```

命令输出应生成机器可读报告，至少包含 run id、profile、marker、各后端结果、耗时、失败阶段和清理状态；真实凭据和受保护 payload 不得进入报告。

## 已沉淀 ADR

已新增 [ADR-0008：真实可观测后端接入与最小 HTTP 观测闭环](../adr/0008-real-observability-backends-and-minimal-http-loop.md)，记录应用内组件分工、Collector fan-out、Grafana 主线与 SigNoz 备选、Langfuse AI 平面、最小 HTTP API、验证分层、隐私与重新审视条件。

## 当前临时建议

在没有新反证前，下一步可以按以下方向继续设计：

```text
GoFrame HTTP/API
  -> GoFrame 基础 tracing + OTel API
  -> OTel SDK 在应用入口装配
  -> pkg/ai/obs 产生 AI 语义事实
  -> OTel adapter 生成 span/event/attribute
  -> OTel Collector fan-out
      -> 基础设施后端：Grafana stack 或 SigNoz
      -> AI 语义后端：Langfuse OTLP
```

最小 API 提供非流式 `POST /api/v1/chat` 和受配置保护的 `GET /api/v1/observability/infra-smoke`。真实运行的 chat 使用服务端配置的 OpenAI-compatible GPT-5.5 上游；默认离线测试和 eval fixture 继续使用 deterministic fake provider。

## 讨论记录

### 2026-07-09

- 初始讨论确认：需要先梳理可观测组件、可观测后端服务和最小 HTTP API 范围。
- 初始倾向：基础设施平面与 AI 语义平面分离；HTTP API 端点跟随 Harness 能力逐步增长，不在第一阶段塞满所有路径。
- 补充讨论：GoFrame 和 OTel SDK 不是竞争关系。GoFrame 更像框架自动埋点来源；OTel SDK 是应用内遥测管道；OTel API 是库/业务埋点接口；Collector 是应用外遥测路由器。
- 当前共识：应用内组件分工采用“GoFrame 基础设施自动埋点 + OTel API/SDK 标准遥测层 + `pkg/ai/obs.Trace` AI 语义源模型 + 平台 adapter 映射”的方案；不为新实现引入 `opentracing-go`。
- 实现细化共识：Trace 初始化优先复用 GoFrame contrib trace；必要时再通过窄接口补充 OTel SDK 装配，但不能重复注册竞争性的全局 provider。Logs 先走 `glog` trace id 关联。Metrics 先定义语义，业务代码只面向 OTel Metrics/Collector，不直接对接任何具体后端；实际后端由基础设施观测后端决策决定。
- 基础设施后端共识：主线采用 `Prometheus + Loki + Tempo + Grafana` 经典方案，备选支持 `SigNoz`。两者都通过 OTel Collector 接入，应用层不出现任何后端专属埋点或 SDK。
- 基础设施实现细化共识：提供 `observability-grafana` 与 `observability-signoz` 两个 docker compose profile。Grafana 主线优先实现，并同步覆盖基础设施指标、AI token/cost 和 eval 指标；经典路线需要提供 `glog` 到 Loki 的本地采集示例。SigNoz 也维护专门 dashboard/checklist，但实施优先级靠后。
- AI 语义后端共识：采用 Langfuse。正式链路通过 OTel Collector fan-out；另保留直连 Langfuse OTLP endpoint 的诊断 smoke，用来隔离验证 ingestion、属性映射、endpoint 和凭据。
- Payload policy 共识：开发阶段允许受控记录原文以帮助学习和追踪，但 `is_debug` 不能关闭敏感信息检测。payload mode 与 debug 分离，`content_raw` 仅限 local/test 显式启用，生产环境 fail fast；应用内保护和 Collector 二次过滤共同构成出口边界。
- 评估同步共识：本地 eval evidence 保持事实源，同时通过独立 Langfuse score adapter 同步为平台 score。OTLP 只承载 trace，score 通过 Langfuse API 投影；同步失败不影响业务和本地评估结果。
- Collector 拓扑共识：优先采用 Collector fan-out，应用内敏感保护后由 Collector 做第二道确定性字段过滤。两个观测平面允许对同一 AI trace 做不同后端投影；纯基础设施请求只落入 infra 后端。
- Collector pipeline 共识：采用方案 C，即 `ingress pipeline + forward connectors + infra/AI downstream pipelines`，避免公共 processor 重复，同时保留后端专属过滤、batch、queue 和 retry。
- 出口失败归因共识：采用方案 1 + 方案 2，即 Collector 内部指标按 Tempo/Loki/Langfuse OTLP exporter component 区分，三个 push 出口各自使用 queue/retry；端到端 canary 保留为平台 smoke，不作为主要失败归因机制。Prometheus scrape、Grafana datasource 和 Langfuse score adapter 分别属于独立失败域。
- AI 平面路由共识：使用 `longtermism.observability.plane = "ai"`。AI usecase 负责在 root/bridge span 设置标记，`pkg/ai/obs` OTel adapter 负责 semantic spans；单进程走 context，跨服务走 allowlist baggage，只标记 root/bridge 与 AI semantic spans。
- Persistent queue 共识：本地 compose 第一阶段即启用 Collector `file_storage` 持久化队列，Tempo/Loki/Langfuse OTLP 出口分别配置 queue/retry/timeout，并验证后端暂停、Collector 重启和恢复排空路径。Prometheus 采用 pull/scrape，不适用 exporter persistent queue。
- 最小 HTTP API 共识：`POST /api/v1/chat` 承载真实 AI 闭环，另提供仅在 smoke 开关启用时可访问的 `GET /api/v1/observability/infra-smoke`，用于验证纯基础设施请求只进入 Tempo、不进入 Langfuse。
- 真实 LLM 共识：复用现有 `pkg/ai/llm/openai` Chat Completions adapter，服务端配置 OpenAI-compatible base URL、API key 和默认 `gpt-5.5`；真实运行 fail fast，离线测试继续使用 deterministic fake provider。
- Response metadata 共识：`request_id` 始终返回并写入 `X-Request-ID`，AI 请求始终返回 opaque `ai_trace_id`，有界低敏 `eval_summary` 仅 debug 返回。
- 配置分层共识：应用只配置 Collector；Tempo/Loki/Prometheus/Langfuse/SigNoz 地址归 Collector 或后端 profile；Grafana 只配置 datasource；Langfuse score API 归 infrastructure adapter。
- 传输与采集共识：App 默认 OTLP gRPC 并保留 HTTP/protobuf override；本地 Prometheus 使用 pull；`glog` 通过 JSON 文件、shared volume 和 Collector `filelog` receiver 进入 Loki。
- Langfuse score 共识：第一阶段采用应用进程内有界异步 worker，本地 evidence 保持事实源；平台投影失败不影响业务，并明确接受进程崩溃时未发送 projection 可能丢失的 v1 边界。
- 配置安全共识：非敏感 endpoint 可进入默认/profile 配置，环境专属本地文件不得上传；所有凭据仍必须通过环境变量、secret file 或密钥管理器注入。
- 隐私与采样共识：生产默认低敏 metadata，开发按 payload policy 受控查看内容；baggage 使用低敏 allowlist，禁止项跨所有信号和 persistent queue 生效。local/smoke 全量采集，失败/降级/eval 等由 Collector tail sampling 全量保留，普通成功请求使用可配置比例。
- Retention 共识：本地 metrics/logs/traces/Langfuse/eval evidence 使用明确且有界的分层保留期，`content_raw` 最长 24 小时并与长期项目隔离；persistent queue 不作为归档。
- 完成标准重设计草案：验收分为默认离线、Collector 配置、infra-only、真实 AI、隐私、故障恢复、dashboard/retention/Signoz 和文档八类证据；命令分为离线、基础设施、真实 AI、恢复、备选后端五个等级，真实平台验证必须自动查询后端并生成机器可读报告。
- 完成标准审查修订：Prometheus 使用 route/status 指标增量而不是 request id label；各后端使用独立故障证据；PR、配置变更、阶段验收和发布 canary 分层运行；SigNoz 只替换 infra backend，Langfuse 仍承载 AI 平面；补齐分后端超时、安全检查、告警、镜像/资源固定和 reset 防误删。
- 决策 8 采纳：八类完成证据、Level 0-4 验证命令、运行频率和阶段发布门禁正式作为本阶段实施与验收契约。
- 平台 smoke 共识：新增 `obs-platform-smoke` 作为本地轻量集成命令，复用受控 sender 验证最小平台接入契约；它不连接真实后端，真实 Grafana/SigNoz/Langfuse 查询继续由 E2E 命令负责。

## 后续问题清单

- [X] 基础设施后端第一阶段选择 Grafana stack 还是 SigNoz？当前共识：Grafana 经典栈为主线，SigNoz 为备选。
- [X] AI 语义后端第一阶段是否只接 Langfuse，Phoenix 作为后续对照？当前共识：是。
- [X] 是否以 OTel Collector fan-out 作为唯一正式平台拓扑？当前共识：是；另保留直连 Langfuse endpoint 的诊断 smoke，但不作为常规运行拓扑。
- [X] Collector pipeline 是否采用一个 ingress pipeline，再通过 forward connectors 分为 infra/AI 两个下游 pipeline？当前共识：采用方案 C。
- [X] AI 平面路由 attribute、设置边界与传播规则如何定义？当前共识：`longtermism.observability.plane = "ai"`，AI usecase 设置 root/bridge，OTel adapter 设置 semantic spans，单进程 context、跨服务 baggage。
- [X] 本地 compose 是否启用 persistent queue？当前共识：第一阶段即按生产级标准启用并验证重启恢复。
- [X] 第一个 HTTP 端点是否默认 fake provider，真实 LLM opt-in？当前共识：真实运行的 chat 接入 OpenAI-compatible GPT-5.5，fake provider 只用于默认离线测试和 eval fixture。
- [X] 是否提供纯基础设施 API 验证双平面路由？当前共识：提供配置保护的 `GET /api/v1/observability/infra-smoke`。
- [X] response metadata 是否采用 `request_id`/`ai_trace_id` 始终返回、`eval_summary` 仅 debug 返回？当前共识：采用。
- [X] App -> Collector 使用何种协议？当前共识：默认 OTLP gRPC，保留 OTLP HTTP/protobuf override。
- [X] 本地 Prometheus 使用 pull 还是 remote write？当前共识：使用 scrape Collector exporter 的 pull 模式，remote write 作为后续生产备选。
- [X] `glog` 如何进入 Loki？当前共识：结构化 JSON 文件 + shared volume + Collector `filelog` receiver。
- [X] Langfuse score 第一阶段如何执行？当前共识：应用进程内受控异步 worker。
- [X] endpoint 与凭据如何配置？当前共识：非敏感 endpoint 可写默认/profile 配置；本地 override 不上传；凭据始终使用环境变量、secret file 或密钥管理器。
- [X] 隐私、采样与 retention 如何设计？当前共识：采用字段分级、payload policy、baggage allowlist、Collector tail sampling 和有界分层 retention 方案。
- [X] 是否采用决策 8 中重新设计的八类完成证据、五级验证命令和最终发布门禁？当前共识：采用，作为本阶段实施与验收契约。
- [X] 是否新增独立 `obs-platform-smoke` 命令？当前共识：新增，以本地轻量集成测试为核心，不替代真实后端 E2E。
- [X] 是否新增 ADR-0008？当前共识：新增并接受，记录真实后端接入与最小 HTTP 观测闭环。
- [X] 是否需要 docker compose 管理本地 collector/backend/langfuse？当前共识：需要，基础设施后端提供 `observability-grafana` 与 `observability-signoz` 两个 profile，并在 AI 语义方案中纳入本地 Langfuse 小规模验证。
- [X] v1 是否同步 Langfuse score，还是只验证 generation observation？当前共识：同步 score，同时保留本地 eval evidence 作为事实源。
