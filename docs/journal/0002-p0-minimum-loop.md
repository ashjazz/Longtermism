# P0 最小闭环实现沉淀：从占位门禁到可验证 AI 工程路径

**日期**：2026-06-26
**关联任务**：T051-T055
**关联模块**：cmd/eval-smoke、internal/eval/smoke、pkg/ai/prompt、pkg/ai/llm、pkg/ai/obs、pkg/ai/eval
**状态**：已复盘

## 发生了什么

P0-E 阶段把前面已经完成的 prompt、LLM provider 抽象、trace 和 eval runner 串成了默认离线可运行的本地门禁。

实现前，`make eval-smoke` 仍然是 Phase 1 里留下的占位命令，只会打印 TODO 并以非零退出：

```text
TODO: eval-smoke is not implemented yet. Complete P0-E tasks T051-T053 before enabling this gate.
```

T051 先建立 `cmd/eval-smoke` 命令入口，默认使用 fake predict 和本地 golden dataset，不要求真实 API key。T052 把 `Makefile` 的 `eval-smoke` 目标接到真实命令。T053 再把临时直连 eval runner 的实现推进成完整组合层：

```text
golden dataset -> prompt 渲染 -> fake LLM -> trace recorder -> eval runner -> JSON report
```

最终 `make eval-smoke` 的稳定输出为：

```text
go run ./cmd/eval-smoke
{
  "datasetVersion": "p0-smoke-local",
  "sampleCount": 3,
  "scores": {
    "context_hit": 1,
    "exact_match": 1
  }
}
```

## 根因

这轮暴露了三个值得记录的工程风险。

第一，**占位门禁容易制造“看起来已经有命令”的错觉**。Phase 1 先放 `eval-smoke` TODO 是必要的，因为当时闭环能力还不存在；但如果后续只实现 eval runner，而没有把 Makefile 接到真实路径，本地门禁仍然无法证明 P0。

第二，**只跑 eval runner 不能证明 AI 工程闭环完整**。T051 最初可以用 fake predict 直接回放 golden answer，让 `exact_match` 和 `context_hit` 通过；但这只能证明 dataset、metric、runner 能工作，不能证明 prompt 模板被渲染、模型抽象被调用、trace 被记录。

第三，**测试覆盖率本身也是设计反馈**。T053 初版实现后，`go test -cover ./internal/eval/smoke` 只有 69.0%。功能虽然通过，但默认路径、fake provider 边界、防御性复制和 trace 返回不可变性还没有被测试覆盖。这类缺口在生产环境里会变成“改坏了也没有红灯”的隐患。

## 修复

实际修复分四步完成。

1. 在 `cmd/eval-smoke/main.go` 实现可测试的命令入口。入口负责解析 `-dataset` 和 `-dataset-version`，失败时打印 `eval smoke failed: ...` 并返回非零退出码。
2. 在 `Makefile` 中将 `eval-smoke` 接为 `go run ./cmd/eval-smoke`，让本地门禁和文档命令一致。
3. 在 `internal/eval/smoke/p0.go` 新增 P0 组合层。组合层加载 golden dataset，渲染 `resource/prompt/p0_smoke/v1.tmpl`，调用按 sample ID 返回 golden answer 的 fake LLM，记录安全 trace，再交给 eval runner 评分。
4. 在 `internal/eval/smoke/p0_test.go` 补充完整闭环、prompt 缺变量失败、默认本地资产、fake provider 边界、dataset/trace 防御性复制等测试，将覆盖率提升到 86.5%。

验证命令与结果：

```bash
go test ./cmd/eval-smoke ./internal/eval/smoke
go test -cover ./internal/eval/smoke
go test ./...
go test -race ./...
go vet ./...
make eval-smoke
```

其中 `internal/eval/smoke` 覆盖率为：

```text
coverage: 86.5% of statements
```

## 学到什么

P0 最小闭环不是一个“能打印 JSON 的 demo”，而是一条可复用的工程路径。

```text
prompt -> llm -> obs -> eval -> local gate
```

这条路径的价值在于：以后替换 fake LLM 为真实 provider、替换静态 context 为 RAG 检索、替换本地 recorder 为 LangFuse/OTEL adapter 时，门禁形态不需要重写。我们只替换某一段实现，仍然用同一个 report 和 trace 证据证明能力没有退化。

这次最重要的失败模式是：**如果评估绕过 prompt/LLM/trace，只直接回放答案，就会把“评估框架可运行”误判成“AI 工程闭环已完成”**。修复方式不是让 fake 更复杂，而是让 fake 也走真实抽象边界：prompt 仍然要渲染，provider 仍然要被调用，trace 仍然要记录。

另一个经验是：普通 trace 必须只记录摘要。P0 smoke 的 trace 记录 query hash、prompt hash、token、model、retrieval count 和 outcome status，不记录原始 query 或完整 prompt。这让本地门禁从第一天就保持和生产隐私边界一致。

## 后续预防

- 增加或调整测试：后续 P0-E 改动必须继续跑 `go test ./cmd/eval-smoke ./internal/eval/smoke`，并保持 `internal/eval/smoke` 覆盖率不低于 80%。
- 增加或调整评估 case：当前 `p0_smoke.json` 只有 3 条样例；进入 RAG/Agent 阶段后，应为检索命中、工具错误、自我纠错和步数限制补独立 golden case。
- 增加或调整 trace 字段：后续真实 provider 接入时，应继续验证普通 trace 不包含原始 query、完整 prompt、API key 或 tool 参数原文。
- 增加或调整降级路径：当前 fake LLM 只覆盖成功路径和本地错误边界；后续 resilience 阶段应加入上游错误、超时、限流和降级缓存路径的 smoke。
- 需要补充的 ADR / ROADMAP / tasks：T054 已同步 quickstart；T055 后进入 Phase 5 前，应在后续 Final Phase 更新 ROADMAP 和 P0 retrospective。
