# 调研结论：真实可观测后端与最小 HTTP 闭环

**日期**：2026-07-10
**输入**：003 规格、ADR-0008、真实后端决策工作台、当前代码与官方项目文档。

## 1. 应用遥测出口

**Decision**：应用只连接 OTel Collector；默认 OTLP/gRPC，保留 OTLP/HTTP protobuf override。应用配置中不得出现 Tempo、Loki、Prometheus、Grafana、SigNoz 或 Langfuse trace endpoint。

**Rationale**：OTLP 保留完整 OTel 数据模型，Collector 是 vendor-neutral 的处理与 fan-out 边界。协议 override 解决受限网络或代理环境，不改变业务依赖。

**Alternatives considered**：应用直连多个后端；应用直接使用各平台 SDK；仅 HTTP。前两者造成平台绑定和多失败域，后者不符合已确认的默认 gRPC 路线。

**Evidence**：[OpenTelemetry Go exporters](https://opentelemetry.io/docs/languages/go/exporters/) 与 [OTLP exporter configuration](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/)。

## 2. GoFrame 自动埋点与 provider 初始化

**Decision**：复用 GoFrame HTTP/框架自动 tracing 与全局 OTel provider。初始化先通过兼容性测试评估 GoFrame contrib；由于 v2.10.2 contrib initializer 固定 AlwaysSample、全局注册、insecure transport，并且配置入口不足以同时表达当前要求的 headers/TLS/resource/test double，真实 collector 模式采用 `internal/cmd` 下的窄 OTel SDK initializer。进程内只允许一个 TracerProvider；HTTP 与 gRPC 由同一个初始化端口选择。

**Rationale**：这满足 ADR-0008 的明确回退条件，同时保留 GoFrame 自动埋点，不复制 HTTP middleware tracing。官方 OTel SDK负责 provider、resource、batch、propagator 与 shutdown，不成为 `pkg/ai` 依赖。

**Alternatives considered**：无条件使用 GoFrame `otlpgrpc.Init`/`otlphttp.Init`；同时注册两个 provider；在业务代码中自行打 HTTP span。均无法满足配置/测试边界或会产生重复 span。

**Evidence**：本地 `github.com/gogf/gf/contrib/trace/otlpgrpc/v2@v2.10.2` 与 `otlphttp/v2@v2.10.2` 源码；[OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/)。

## 3. Collector 双平面拓扑

**Decision**：采用方案 C：一条 traces ingress pipeline，经 forward connectors 复制到 infra 与 AI downstream pipelines。infra 接收所有 trace；AI downstream 使用 `longtermism.observability.plane="ai"` 过滤，只保留 root/bridge 与 AI semantic spans。公共 memory/resource 处理位于 ingress；backend-specific filter/transform/batch/exporter 位于 downstream。

**Rationale**：避免两套 receiver/common processors 重复，同时保持采样、字段删除、队列与失败归因独立。纯基础设施请求的负向路由可由 filter outgoing-items 增量和 Langfuse 查询共同证明。

**Alternatives considered**：单 pipeline 多 exporter；两套完整独立 pipeline；两个物理 Collector。分别存在 infra 数据误入 AI、公共处理重复和本地复杂度过高的问题。

**Evidence**：[Collector architecture](https://opentelemetry.io/docs/collector/architecture/)、[Collector connectors](https://opentelemetry.io/docs/collector/extend/custom-component/connector/) 与 [transform/filter guidance](https://opentelemetry.io/docs/collector/transforming-telemetry/)。

## 4. Grafana 主线信号路径

**Decision（由 T170-T174 校准）**：Tempo 接收 OTLP traces；Collector 暴露 Prometheus scrape endpoint，Prometheus 同时抓取应用指标与 Collector self-telemetry；应用使用同一 OTel provider lifecycle 发射受控 completion OTLP logs，经 Collector 的 OTLP receiver、固定 body filter 与精确 attribute/resource allowlist 后，通过 Loki native OTLP HTTP endpoint 写入 Loki；Grafana仅配置 datasources、dashboards 与 alerts。JSONL 只保留为显式本地诊断 opt-in，不再是 smoke 或 Loki 的数据路径。

**Rationale**：应用、trace、metric、log 只连接同一 Collector，消除 Docker Desktop bind-mount/file watch 与宿主 GID 耦合；Collector 的 Loki persistent queue承担已脱敏日志的恢复；Loki native OTLP 保留 structured metadata；Prometheus pull 与 push exporter 的故障证据保持不同。

**Alternatives considered**：应用直接发 Loki；使用已趋于淘汰的 Loki exporter；Prometheus remote write；glog JSONL + shared volume + filelog。前三者增加应用绑定或偏离已确认边界；最后一种曾作为早期方案实施，但因宿主挂载可见性和权限耦合被 T170-T174 取代。

**Evidence**：[Loki native OTLP ingestion](https://grafana.com/docs/loki/latest/send-data/otel/)、[native OTLP vs Loki exporter](https://grafana.com/docs/loki/latest/send-data/otel/native_otlp_vs_loki_exporter/) 与 [Tempo Collector setup](https://grafana.com/docs/tempo/latest/set-up-for-tracing/instrument-send/set-up-collector/otel-collector/)。

## 5. Langfuse trace 与 score

**Decision**：Collector 通过 OTLP/HTTP protobuf 写 Langfuse `/api/public/otel`；包含 ingestion version header。应用内 score adapter 通过 Langfuse Public API 异步写 score，使用稳定 score ID 作为幂等键，并同时携带真实平台 trace ID；本地 eval evidence 永远先落事实源。trace exporter 与 score worker 是两个独立失败域。

**Rationale**：Langfuse 当前 OTLP endpoint 不支持 gRPC；score 是平台 API 对象，不属于 OTLP span。稳定 ID 允许 at-least-once 重试不制造多个 score。

**Alternatives considered**：应用直连 Langfuse OTLP；把 score 编成 span attribute；平台 score 作为唯一 evidence。分别破坏 Collector 边界、score 语义和本地可回归性。

**Evidence**：[Langfuse OpenTelemetry ingestion](https://langfuse.com/integrations/native/opentelemetry)、[Scores data model](https://langfuse.com/docs/evaluation/scores/data-model) 与 [Scores via API](https://langfuse.com/docs/evaluation/evaluation-methods/scores-via-sdk)。

## 6. 身份与 GenAI 属性

**Decision**：`request_id` 是 API/支持查询入口；OTel trace/span ID 必须从活动 SpanContext 读取；`ai_trace_id` 是 usecase 在调用 provider 前生成的框架身份；eval run/sample 由本地评估层生成。Langfuse score projection 另外保存平台 trace/observation ID，adapter 不把 `ai_trace_id` 猜成平台 trace ID。AI plane attribute 使用 `longtermism.observability.plane=ai`；平台专属 `langfuse.*` 只在 adapter 映射层生成。

**Rationale**：真实 OTel identity 与领域 identity 生命周期不同。显式映射避免 score 指向错误对象，也让平台可替换。

**Alternatives considered**：复用一个字符串同时表示所有 trace；adapter 根据缺失字段猜测；把高基数 identity 放入 metric labels。均会制造错误关联或高基数风险。

## 7. Persistent queue 与失败归因

**Decision**：Tempo、Loki、Langfuse 三个 push exporter 各自启用 sending queue、retry、timeout 和基于 `file_storage` 的 persistent queue；component ID 固定并进入 dashboard/alert 查询。Prometheus pull、Grafana query 与 Langfuse score worker分别使用其自身证据，不伪造 exporter send-failed。

**Rationale**：persistent queue 可跨 Collector 重启恢复短时积压，但只提供 at-least-once；permanent error、队列容量耗尽和磁盘故障仍可丢失，必须观测。

**Alternatives considered**：共享一个 queue；仅内存 queue；把 queue 当长期归档。分别破坏失败归因、重启恢复和数据生命周期。

**Evidence**：[OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) 与 Collector exporter-helper/file-storage 官方组件说明。

## 8. Payload policy 与日志边界

**Decision**：支持 `metadata_only`、`content_redacted` 与显式受限的 `content_raw`。raw 仅在 `local`/`test` 且 `raw_content_enabled=true` 时生成独立的 `LocalRawPayload` 调试工件；它不支持 JSON 序列化，也绝不进入 span、log、metric、queue、report、evidence、Collector 或 Langfuse。所有可外发的 `PayloadSnapshot` 都仍经过脱敏。应用先过滤，Collector 再做确定性字段保护。

**Rationale**：开发学习、测试与故障排查有时必须检查完整原文，但该能力与可观测后端留存是两个信任边界。通过独立、不可序列化的本地工件保留排查价值，同时让保护继续发生在 persistent queue 之前。

**Alternatives considered**：生产默认 raw；由 Collector 单独承担隐私；完全禁用所有内容。分别风险过高、保护太晚或妨碍调试学习。

## 9. 版本固定与升级策略

**Decision**：所有容器使用明确 tag 并在首次成功 E2E 后记录 digest；禁止 `latest`。在 `deploy/observability/versions.env` 维护单一兼容矩阵，升级一次只变更一个后端族并重跑 config、Grafana E2E 和恢复测试。当前官方资料基线包括 Collector v0.156 系列、Loki 3.7 系列、Prometheus 3.12 系列、Langfuse v3.185 系列和 SigNoz v0.126 系列；最终锁定值以实施当天验证成功的 patch/digest 为准。

**Rationale**：这些项目发布节奏不同；按“最新”浮动会让 compose 和查询契约不可重复。tag 便于阅读，digest 保证不可变。

**Alternatives considered**：统一使用 latest；只固定 major；自动随每次启动拉新镜像。均不满足可回归验收。

## 10. SigNoz 支持边界

**Decision**：SigNoz 作为独立基础设施 profile，在 Grafana 主线完成后实施。它接收 logs/metrics/traces，Langfuse 继续接收 AI trace/score；应用配置和埋点不因 profile 改变。SigNoz 必须有独立 dashboard/checklist 和真实查询 E2E。

**Rationale**：提供一体化选择，同时保持 AI 评估平面一致；延后实施可先沉淀经典栈的组件边界与恢复经验。

**Alternatives considered**：SigNoz 替换全部后端含 Langfuse；两个 profile 同优先级并行；只验证 compose 启动。均偏离已确认目标。

**Evidence**：[SigNoz OTel Collector metrics](https://signoz.io/docs/metrics-management/opentelemetry-collector-metrics/) 与 [SigNoz log collection methods](https://signoz.io/docs/logs-management/send-logs/collection-methods/)。
