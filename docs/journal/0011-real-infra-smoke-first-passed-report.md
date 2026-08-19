# 0011-real-infra-smoke-first-passed-report

**日期**：2026-08-14
**关联任务**：spec 003 T176 / T201（首次真实 Grafana infra smoke）
**结果**：`build/observability/smoke-reports/infra-1786684631434977000.json` —— 真实
`passed` 报告，六个 checks 全部 attempted/passed，26.3s ≤ 60s。

## 出过什么事

按 README 启动完整 Grafana+Langfuse profile 与应用后，前四次 `make obs-infra-smoke`
全部 failed。四次失败暴露了三个“离线契约 vs 真实后端”的分叉和两个环境事实：

1. **Loki 3.7.2 的真实响应形状**（`malformed_response`）：契约假设 OTLP attributes 以
   “逐条三元素 `[ts, line, metadata]`”返回；真实 query_range 响应把 structured metadata
   放进 stream label map，values 是两元素 `[ts, line]`。
2. **Prometheus counter 重置**（`metric_delta_missing`）：应用重启会重置进程内 counter，
   而旧实例的 stale 样本（值 1）与新实例重置后的样本（值 1）相等，裸 counter 基线比较
   永远 delta=0。`increase()[60s]` 是 Prometheus 原生 reset-aware 语义；同时它返回外推
   小数（如 `1.33`），decoder 的 `ParseInt` 会拒绝，需要 `round()` 收拢。
3. **Langfuse v2 observations API 不存在**（`authentication_failed` → 实测 404）：
   锁定版本 3.185.0 的 `/api/public/v2/observations` 需要 v4 write mode；v3 实际服务的
   是 v1 `/api/public/observations`，且接受相同的 stringObject/datetime filter 形状。
4. **本地 `.env.local` 的 OTLP credential 顺序写反了**：`LANGFUSE_OTLP_AUTHORIZATION`
   的 base64 解出来是 `sk-lf-...:pk-lf-...`（secret 在前），API 查询要求
   `pk-lf-...:sk-lf-...`。顺序颠倒只在查询时暴露（OTLP 摄入尚未有真实流量），
   提示：诊断日志只报了 401，没报顺序。
5. **环境变量生命周期**：`LANGFUSE_INIT_*` 与 `LANGFUSE_EE_LICENSE_KEY` 是 compose
   插值的必填项，但只被 headless init（幂等 upsert）使用；warm start 也需要它们存在。

## 修好了什么（都先改契约测试，再改实现）

- `internal/observability/backend/grafana_smoke_adapter.go`：Loki decoder 接受真实
  两元素形状（marker 从 stream labels 精确核对，保留三元素兼容路径，label 与 metadata
  矛盾拒绝）；`DecodePrometheusHTTPCount` 把空成功向量解码为 0 counter 基线（与畸形文档
  严格区分，check 仍要求真实正向 delta 才通过）。
- `grafana_infrastructure_smoke.go`：`round(sum(increase(...[60s])))` 替代裸 counter
  查询，reset-aware + 整数化；测试断言同步钉死新语义。
- `langfuse_smoke_query.go`：负向查询路径 v2 → v1（与 chat/privacy 客户端已有的 v1 对齐）。
- 夹具全部改为真实形状，并新增空向量基线、label/metadata 矛盾等回归用例。

## 学到了什么（面试故事库 §15 素材）

- “离线契约正确”≠“真实后端正确”：三个假设同时被真实运行推翻，而每个失败都被
  schema-valid failed 报告以稳定 error_class 记录，修正路径可审计——这正是
  failure_stage/error_class 设计的价值。
- 计数器语义要写进查询本身：依赖“进程不重启”的 delta 比较在生产必然翻车，
  PromQL 的 increase()/rate() 才是 counter 的正确读法。
- 凭据格式错误只有 401 一个信号；把“Basic base64(pk:sk)”的顺序、前缀写进 README
  运行手册，避免下一个操作者重复踩坑。
- 环境先决条件（镜像、`.env.local`、INIT 变量）要可重复：warm start 的必填变量集合
  与冷启动一致，只是值不再被消费。
