# SigNoz 备选基础设施 profile 兼容性清单

## 定位声明

- 本清单验证的是**备选**基础设施方案：SigNoz profile（`compose.signoz.yaml` + `collector-signoz.yaml`）替换 infra 三信号后端，Langfuse AI 平面保持不变。
- **优先级低于 Grafana 主线**：主线（grafana-infra / real-backend-acceptance 清单）未通过验收前，本清单的任何通过项都不构成对备选方案的支持声明；备选 profile 的故障不阻塞主线发布。
- 应用契约与主线完全一致（OTLP 入口、marker、identity、隐私边界不因 profile 改变）。

## 证据规范

- 每一项验收都必须以**真实查询证据**关闭：`make obs-signoz-e2e`（T148 落地）输出的 schema-valid report，或等价的 SigNoz/Langfuse 查询 API 结果。
- **禁止**以 compose 容器 healthy、UI 截图或"服务已启动"代替查询证据——备选方案的验收标准与主线相同（research.md 决策 10）。
- 查询必须限定在本次 run 的窗口（唯一 marker + StartedAt/Deadline），历史 run 的数据不能满足当前验收。

## 逐项验收

- [ ] SigNoz **logs** 查询证据：infra smoke run 的 marker 可在 SigNoz logs 中查询到（`signoz_logs` check passed，report 含 matched_logs）。
- [ ] SigNoz **metrics** 查询证据：infra smoke 触发后 HTTP 请求计数出现正向增量（`signoz_metrics` check passed，report 含 metric_delta > 0）。
- [ ] SigNoz **traces** 查询证据：chat run 的 service trace 可在 SigNoz traces 中查询到，且逐字段匹配 request/trace identity（`signoz_traces` check passed）。
- [ ] Langfuse **trace** 查询证据：chat run 的 AI marker 观测在 Langfuse trace 中可查询（`langfuse_trace` check passed）。
- [ ] Langfuse **score** 查询证据：chat run 的 score 投影在 Langfuse 中可查询（`langfuse_score` check passed，缺失投影记为 score_projection_missing 失败，不允许静默跳过）。
- [ ] infra-only 负向证据：纯 infra smoke 中 AI 平面零命中（`langfuse_trace`/`collector` negative checks passed；信号缺失时记 skipped 而非伪造通过）。
- [ ] 备选 profile E2E 产出 schema-valid passed report：`make obs-signoz-e2e` 单次运行同时覆盖以上三信号与 AI 平面检查。
- [ ] dashboard 资产与实际信号一致：`deploy/observability/signoz/dashboard.json` 的面板查询在真实数据下可执行（T145 资产与 E2E 证据源映射一致，不混淆出口归因）。

## 边界提醒

- SigNoz 的 `signoz-otel-collector` 无 shell（官方最小基础镜像），健康性由上述查询闭环证明，不依赖容器 healthcheck。
- AI trace/score 的深度证据（隐私扫描、投影幂等、worker 故障恢复）由主线 Langfuse 清单承担；本清单只验证备选 profile 下 AI 平面的存在性与关联性。
