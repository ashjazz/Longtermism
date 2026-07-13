# Specification Quality Checklist: Real Observability Backends

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-07-10
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

- Validation completed on 2026-07-10. Implementation choices are intentionally kept in ADR-0008 and the decision workbench; this specification defines observable user outcomes and acceptance boundaries.

## Post-task Traceability Review

**Reviewed**: 2026-07-13

**Scope**: `spec.md` functional requirements (FR) and success criteria (SC) against `tasks.md` after task generation.
**Interpretation**: “任务已覆盖”只表示存在计划任务，**不表示已实现、已通过测试或已完成真实后端验收**。本表不关闭 `real-backend-acceptance.md` 中的任何项。

### Functional requirements

| Requirement | Planned task / phase coverage | Review result | Remaining risk |
| --- | --- | --- | --- |
| FR-001 | Phase 3 / US1 T054-T066（Grafana 主线）；Phase 6 / US4 T136-T148（SigNoz） | 任务已覆盖 | 两个 Compose profile、健康检查和真实启动证据均未实现。 |
| FR-002 | Phase 2 T015/T027；Phase 3 / US1 T038/T050；Phase 4 / US2 T071/T091、T082/T102 | 任务已覆盖 | 双平面路由与 infra-only 的 AI negative query 尚无真实查询证据。 |
| FR-003 | Phase 3 / US1 T037-T040、T049-T052、T064-T066 | 任务已覆盖 | 受保护路由、限流和后端负向验证尚未实现。 |
| FR-004 | Phase 4 / US2 T068-T076、T088-T096、T105/T108-T109 | 任务已覆盖 | 真实模型调用仍未接入；默认离线测试不能证明真实 chat。 |
| FR-005 | Phase 4 / US2 T070/T072-T081、T092-T106 | 任务已覆盖 | 模型用量、低敏 debug 摘要、本地 evidence 与平台 score 的完整关联尚未实现。 |
| FR-006 | Phase 2 T012/T024/T034；Phase 4 / US2 T077/T087、T097/T107 | 任务已覆盖 | 三种 payload 策略和真实后端/queue/report 的敏感 canary 扫描尚无实现证据。 |
| FR-007 | Phase 2 T017-T019、T029-T031；Phase 4 / US2 T085-T086；Phase 5 / US3 T112/T115/T116/T122/T125 | 任务已覆盖 | 观测、score、查询与业务/模型故障的隔离尚未在真实故障中验证。 |
| FR-008 | Phase 5 / US3 T113/T123/T124 | 任务已覆盖 | persistent queue、重启恢复、重复识别和 120 秒窗口尚未实现或验收。 |
| FR-009 | Phase 3 / US1 T046/T062；Phase 5 / US3 T117/T127 | 任务已覆盖 | 四类告警的 provision、firing 与 resolved 证据尚未产生。 |
| FR-010 | Phase 3 / US1 T045/T061/T067；Phase 6 / US4 T140/T145/T146/T149 | 任务已覆盖 | Grafana 面板与 SigNoz checklist/dashboard 均仍是计划资产。 |
| FR-011 | Phase 1 T009；Phase 3 / US1 T066；Phase 4 / US2 T109；Phase 5 / US3 T127-T135；Phase 7 / US5 T150-T159 | 任务已覆盖 | 当前仅部分 Level 0 入口可用；真实 E2E、恢复层与最终门禁仍未实现。 |
| FR-012 | Phase 2 T019-T022、T031-T034；Phase 3 / US1 T048/T064；Phase 4 / US2 T085-T108 | 任务已覆盖 | marker、时间窗口、failure_stage、schema 与 cleanup 的生产 smoke 报告仍未实现。 |
| FR-013 | Phase 5 / US3 T111/T121/T128；Final Phase T164/T169 | 任务已覆盖 | scoped reset、preview/confirmation 和无残留 cleanup 尚未实现或实测。 |
| FR-014 | Phase 1 T006-T008；Final Phase T164/T168 | 任务已覆盖 | 当前文档只覆盖早期资产边界；完整运行、故障注入、清理与轮换 runbook 待最终任务。 |
| FR-015 | Phase 4 / US2 T073/T078、T093/T098/T101/T106；Phase 5 / US3 T115/T125 | 任务已覆盖 | evidence store 与异步 score projection 尚未实现，不能证明投影失败不改写本地事实。 |

### Success criteria

| Criterion | Planned task / phase coverage | Review result | Remaining risk |
| --- | --- | --- | --- |
| SC-001 | Phase 3 / US1 T037-T067 | 任务已覆盖 | 60 秒内 Tempo/Loki/Prometheus 正向查询与 Langfuse negative query 尚无机器报告。 |
| SC-002 | Phase 4 / US2 T068-T109 | 任务已覆盖 | 60/120 秒双平面与 score 时间界限尚无真实模型/平台证据。 |
| SC-003 | Phase 5 / US3 T110-T135 | 任务已覆盖 | 至少 8 类故障的分类、恢复和业务结果不变仍待逐项运行。 |
| SC-004 | Phase 5 / US3 T113/T123/T124 | 任务已覆盖 | queue drain、重复识别与不宣称 exactly-once 的报告尚未生成。 |
| SC-005 | Phase 2 T012/T022/T024/T034；Phase 4 / US2 T087/T107；Final Phase T169 | 任务已覆盖 | 全部输出面与真实持久队列的零未脱敏命中尚未实测。 |
| SC-006 | Phase 3 / US1 T046/T062/T067；Phase 5 / US3 T117/T127 | 任务已覆盖 | 四类告警的 firing/resolved 证据尚未获得。 |
| SC-007 | Phase 1 T009；Phase 7 / US5 T150-T159 | 任务已覆盖 | 当前有离线入口和测试，但 30 秒/零连接/完整报告要求仍待 US5 实现与验收。 |
| SC-008 | Phase 3 / US1 T067；Phase 6 / US4 T136-T149 | 任务已覆盖 | Grafana 与 SigNoz 的独立 checklist、三信号和 AI/eval 闭环都尚未真实验收。 |

### Review conclusion

- [x] FR-001 至 FR-015 均能追溯到至少一个后续 phase/task。
- [x] SC-001 至 SC-008 均能追溯到可生成机器证据的计划任务。
- [x] 已把“任务覆盖”与“实现/测试/E2E 验收”分开记录；没有因已有任务而宣称功能完成。
- [x] 主要残余风险为真实 backend 资产、真实模型/平台、故障恢复和最终 release evidence 尚未交付；这些风险保留到对应 US 与 Final Phase 任务处理。
