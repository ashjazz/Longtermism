# 003 Final Verification

**审查日期**：2026-08-25  
**审查分支**：`003-real-observability-backends`  
**范围**：T169 的本地质量门禁、安全边界与 live evidence readiness；不复制 credential、endpoint、原始 payload 或本地绝对路径。

## 审查结论

当前结论为 **release decision: NOT READY**。本文件中的 `PASS` 只表示指定命令或指定安全检查在本次范围内通过；`FAIL` 表示已执行但未满足契约；`NOT_RUN` 表示没有执行，并必须保留原因与风险。T169 完成只表示审查已执行并记录，不表示 003 或真实后端已经完成。

## 命令结果

| 检查 | 结果 | 低敏证据、原因与范围 |
| --- | --- | --- |
| `git diff --check` | `PASS` | 本次执行退出码为 0，无 whitespace error。 |
| `make verify` | `PASS` | 最终整套 `go vet ./...` 与 `go test ./...` 退出码为 0；同时覆盖 T169 报告契约、coverage helper 与现有架构守护。 |
| `go test -race ./...` | `PASS` | 最终全仓竞态检测退出码为 0；所有含测试包通过。 |
| `make obs-coverage` | `FAIL` | 本次新 atomic profile 的 changed-core 结果为 `9309/11763 = 79.1%`，低于 `80%`；chat usecase 包为 `93.3%`。门禁保持原阈值并阻断 release。 |
| tracked-worktree secret scan | `PASS` | current tracked worktree（含未忽略的新文件）按 private-key、常见 provider token、JWT 等高风险形状启发式扫描；25 个候选全部位于 `_test.go`，经只显示脱敏上下文复核为 synthetic fixture、隐私负向契约或模型名测试，生产代码和文档无命中。扫描不输出候选值。 |
| `make obs-config-check` | `PASS` | 本次静态版本、Compose、Collector 与配置保护检查退出码为 0；不代表 live readiness。 |
| `make obs-release-gate` | `NOT_RUN` | 当前 live checklist 未闭合，本地 secret file 权限不满足 preflight，且真实模型 API 可能计费；未启动 Docker、未调用模型。 |
| `make obs-signoz-compat-gate` | `NOT_RUN` | SigNoz query/端口证据仍待校准，当前 live checklist 未闭合，且真实模型 API 可能计费；未启动 Docker、未调用模型。 |

## 安全审查

### Secrets 与依赖

- current tracked worktree 的启发式 secret scan 为限定范围 `PASS`；synthetic fixture 明确保留为安全负向测试，不能作为扫描器 allow-all 规则。
- `.env.local` 已被 Git 忽略，但本机文件 mode 为 `0644`，不满足 live preflight 要求的 `0600`。本次不读取内容、不修改操作者持有的 secret file，也不运行任何可能消费它的 live gate。
- 专用 git history scanner 不可用，因此 git history scanner 为 `NOT_RUN`；当前扫描不能证明历史提交从未包含 secret。
- 专用 dependency vulnerability scanner 不可用，因此 dependency vulnerability scanner 为 `NOT_RUN`；`go mod verify` 只证明 module 内容校验通过，不证明不存在已知 CVE。

### 输入、认证、网络与文件边界

- 受保护 smoke admission 使用 constant-time credential 比较、一次性 marker 与 loopback-only gate；远程来源、错误 auth 与 replay 的负向测试仍是最终 race 的一部分。
- 后端查询 transport 保持 loopback-only、no-proxy/no-redirect、拨号时地址复验、body/result/time budget 上限；错误只投影 stable low-sensitive error class。
- OpenAI-compatible adapter 的默认 HTTP client 已补齐 no-redirect，避免 adapter 脱离应用装配层独立使用时携带 Bearer credential 跟随重定向；回归测试与应用装配层共同守护该边界。
- run manifest、privacy artifact 与 live gate path 均要求路径 containment、regular-file/owner/mode/symlink 检查；当前 destructive reset 的 run-root 与 volume label hardening 尚未关闭，不能执行 confirm/reset。
- 普通 trace/report 继续禁止 raw prompt/output、Authorization、credential 与 backend response；真实 privacy 后端可见性仍必须由 schema v3 report 证明。

## Live evidence

- Grafana infra 存在 historical schema v2 本机 passed report，但该 ignored 工件不能关闭当前 schema v3 live acceptance。
- `obs-status` 只产生 `diagnostic_only` 容器投影；它不查询后端，也不能替代 SmokeReport。
- 本次没有新的受审 passed report。Grafana release/resilience、privacy、alert、reset hardening 与 SigNoz query/health 仍有未关闭证据项。
- 因此不能声明 Grafana release/resilience 或 SigNoz live support。

## 剩余风险

1. **High — live secret file 权限**：本地 `.env.local` 为 `0644`；在操作者改为 `0600` 前，preflight 必须 fail closed。
2. **High — destructive reset**：volume labels 与 run-root canonical containment/symlink 防护尚未闭合；只允许 inventory preview。
3. **High — 覆盖率门禁失败**：changed-core 覆盖率为 `79.1%`，未达到 `80%`；必须补测试并重新生成 profile，不能降低阈值或扩大 allowlist。
4. **Medium — dependency/history 扫描缺口**：git history scanner 与 dependency vulnerability scanner 未运行，无法给出历史泄漏或已知漏洞清零声明。
5. **High — live acceptance 缺口**：没有当前 schema v3 Grafana release/resilience/privacy/alert 或 SigNoz passed report。
6. **Medium — 任务状态**：T035 仍未关闭；即使 T169 记录完成，003 整体仍为 `in-progress`。

## 完成判定

T169 的本地必跑门禁均已获得最终结果，失败/未运行项已如实记录且本报告契约通过，因此任务可标记完成。该任务勾选只表示审查账本完成；release decision 仍为 `NOT READY`，直到覆盖率、剩余任务与 live evidence 按各自契约关闭。
