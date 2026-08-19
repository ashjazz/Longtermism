# 全量套件两处既有失败：流 started 语义演进与 APFS 并发创建竞态

**日期**：2026-08-17
**关联模块**：pkg/ai/resilience、internal/eval
**状态**：已修复

## 出过什么事

`go test ./...` 存在两处与 spec 003 无关的既有失败，长期被当作"环境问题"搁置：

1. **`pkg/ai/resilience` 测试超时**：`TestProviderWrapperStreamTerminalLifecycleCancelsAndSanitizes`
   挂起直到包级 timeout panic。堆栈显示双 goroutine 对锁：调用方阻塞在
   `ChatStream` 等 `started` 信号，生产侧阻塞在 `openStreamBeforeFirstOutput` 等
   首 chunk——fake 的 unbuffered stream 无人发送，谁也等不到。
2. **`internal/eval` 子进程测试 flaky**：`TestLocalEvidenceStoreCoordinatesAppendsAcrossProcesses`
   约 60-70% 概率失败，子进程报 `OpenLocalEvidenceStore() error = open_lock`。

## 根因

**案例 1（resilience）是实现演进未同步契约测试**：`ChatStream` 的 started 信号
语义已从"流打开时发送"改为"首个 chunk 跨越 wrapper 边界后发送"（replay-safe
重试窗口的一部分——首个 chunk 前失败可安全重试，之后重放会复制文本/工具调用
副作用）。两个测试用例仍按旧语义编写：

- `caller_cancellation...` 用例的 fake stream 完全无产出，started 永不触发，
  `ChatStream` 在测试 cancel 之前就永久阻塞；
- `terminal_chunk_error...` 用例读了第一个 chunk 就断言 terminal 错误，但第一个
  chunk 现在是被转发的 partial 内容。

生产环境里这不会挂死（`context.WithTimeout(ctx, 60s)` 兜底），但测试注入的
runtime 把执行上下文直接换成父 ctx（无超时），把"慢"变成了"永久"。

**案例 2（eval）是 macOS/APFS 文件系统竞态**：两个子进程并发
`openat(O_CREAT)` 同一路径时，败者的 lookup 可能撞上目录项尚未可见的窗口，
`openat` 返回 ENOENT。独立探针稳定复现（约 1/4000 并发轮，两进程同时启动时
整体失败率 8/12；启动间隔 ≥5ms 则 0 失败——窗口极窄）。`retryUnixInt` 只重试
EINTR，ENOENT 被当成终态上抛为 `open_lock`。

## 修好了什么

1. **resilience 测试对齐现行 started 契约**（实现不动——实现是对的）：
   - cancellation 用例预置一个首块（buffered channel）让 `ChatStream` 能返回，
     随后调用方在不再读取流的情况下 cancel——用例意图（预算释放不依赖消费者
     读取）完整保留，还更贴近"consumer stops reading"的字面场景；
   - terminal 用例改为读两个 chunk（partial + sanitized terminal），并顺带断言
     partial 内容被如实转发。
2. **eval 的 `openPrivateEvidenceFileWithCreateRetry`**：仅当调用方显式要求
   `O_CREAT` 且错误为 ENOENT 时做有界重试（64 次，平方退避）；普通打开的
   ENOENT 立即返回——那是真实事实，重试会把缺失掩盖成等待。

## 学到了什么

- **语义演进必须带着契约测试走**。started 语义这种跨 goroutine 的可见性承诺
  一变，所有依赖旧时序的测试都会坏，而且坏法是挂死而非断言失败——在 CI 里
  表现为"超时/flaky"，最容易被误判为环境问题而搁置。
- **测试注入 runtime 会改变故障形态**。生产有 60s 超时兜底，测试的 fake
  `withTimeout` 直接透传父 ctx，把可恢复的慢变成永久挂起。注入点越深，越要
  保持与生产等价的资源回收路径。
- **macOS 不是"确定性文件系统"**。并发 `openat(O_CREAT)` 的 ENOENT 竞态在
  Linux 上（O_CREAT 语义保证创建或打开二选一）不可复现，在 APFS 上是稳定
  可见的。跨平台持久层代码的错误分类必须区分"事实缺失"（ENOENT on plain
  open——立即失败）与"创建竞态"（ENOENT with O_CREAT——瞬时状态，可重试）。
  修复后 12 万次并发打开零错误。
- **被吞掉的 errno 让排障变成考古**。`open_lock` 类别错误不透出底层 errno，
  最终靠独立 Go 探针复刻完整 open 路径才抓到真凶。低敏错误类别是对的（不泄
  露路径细节），但开发诊断路径需要能拿到底层原因。
