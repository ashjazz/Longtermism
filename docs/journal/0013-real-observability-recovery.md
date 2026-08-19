# 0013-real-observability-recovery

**日期**：2026-08-19
**关联任务**：spec 003 T135（首次真实 persistent-queue 恢复实验；T130d 能力收敛的实战验证）
**关联模块**：internal/observability/smoke、internal/observability/backend、cmd/obs-smoke
**结果**：`build/observability/smoke-reports/persistent_queue-1787104476021357000.json` 及随后两份
（`...745943571000` / `...846041124000`）—— 三次连续真实 `passed` 报告，SC-004 的
第一组机器可读恢复证据。核心事实：`queue_depth=1`（暂停期积压）、`matched_spans=1`
（重启 + 恢复后 120s drain 窗口内 marker 可查询）、`duplicate_delivered=0`、
`cleanup=completed`。

## 实验设计

场景验证 FR-008：`pause tempo → 产生 marker 流量 → 重启 Collector → 恢复 tempo →
120s 窗口内验证积压被处理`。注入与恢复由 `failure.DockerControl`（参数化 argv，
compose project 作用域）执行；证据来自 Prometheus 的 otelcol_exporter_* 组件遥测
与 Tempo 的精确 marker 查询（`span."longtermism.smoke.run_id"` TraceQL）。

## 出过什么事

首跑及随后四轮全部 failed。这不是环境问题——五个缺陷全部藏在"离线契约（fake
backend 语义）与真实后端行为"的分叉里，每修一个就暴露下一个：

1. **积压单次快照撞上 scrape 相位**（`unexpected_evidence`，0.26s 即失败）：
   runner 在 trigger 后立刻查询队列快照，但真实装配的快照来自 Prometheus
   （15s scrape 周期）——积压要经历 OTLP 推送 → collector 入队 → 下一次 scrape
   三跳才可见。fake backend 里"快照即刻可见"掩盖了这条传播链。
2. **marker 身份断裂**（tempo check 恒 `query_failed`/`marker_missing`）：
   CLI trigger 按旧设计自建 `run-<hex>` 请求身份，而 runner 用自己生成的
   `marker-<hex>` 查询 Tempo——trace 带的 marker 和报告 marker 永远不同，
   凭 Tempo 属性回查（`/api/traces/{id}` 读出 span attribute）才定位。
3. **查询窗口上限契约分叉**（`invalid smoke query target`，drain 全程查询被拒）：
   marker target 的安全上限是 60s（按 infra 场景的 deadline 钉死），而本场景的
   drain 窗口是 120s——同一份 `isSafeSmokeQueryTarget` 在两个场景间自相矛盾。
4. **marker 搜索窗口排除暂停期 trace**（Tempo 200 但 0 结果，range_seconds=120、
   inspected_bytes=0）：搜索下界从 drainStart（unpause 后）起算，而 Tempo 的
   start 时间过滤把 pause 期间产生的 trace 整体排除在搜索范围外。
5. **跨 restart 的 sent 会计不可比**（`drain-incomplete`）：场景设计本身就包含
   Collector 重启——重启后 sent counter 归零、Prometheus 里旧序列 stale，
   `after.Sent - duringPause.Sent` 恒为负或持平，离线契约里的"连续计数"假设
   在真实环境永远不成立。

另有两个次级问题：Tempo 从 pause 恢复初期查询短暂 5xx 会直接终止 drain 轮询；
drain 搜索窗口若从 run 起点直顶 deadline 会再次超过安全上限（159s > 150s）。

## 根因

五个缺陷共享同一个模式：**离线契约只建模了"查询语义"，没建模"证据的传播时延
与生命周期"**。

- fake backend 是零延迟、零重启、计数单调的理想世界；真实链路里证据要跨越
  应用批量导出、collector 持久队列、Prometheus scrape、Tempo 索引四层异步边界。
- marker 的"请求身份"与"报告身份"在 infra 场景里恰好相同（都是 runID），掩盖了
  两个身份本应显式对齐的契约。
- 60s 窗口上限是为 infra 场景定制的，没有把"恢复场景的窗口是另一个场景参数"
  纳入同一份契约演算。
- sent counter 的 reset 语义在单进程假设下从未被审视——而"跨进程重启后恢复"
  正是本场景要验证的东西：**验证恢复的工具自己必须先正确处理重启**。

## 修好了什么（契约先行，fake 语义同步修正）

1. `persistentQueueWaitForBacklog`：积压等待改为有界轮询（每 poll interval 复查
   `QueueSize > before`，直到 deadline）；fake 测试新增 snapshotFn 注入点支撑
   "永远零积压"与"延迟可见"两类夹具。
2. `NewPersistentQueueSmokeIdentity` 成为显式公开契约：live composition 预生成
   identity，trigger 请求头与 runner 的 Tempo 查询共享同一 marker；exporter-failure
   场景不注入（其证据是组件遥测 delta，与请求身份无关）。
3. marker 查询窗口上限 60s → 150s（覆盖 120s drain + 轮询余量），两个契约测试
   的"超限拒绝"夹具同步改为 151s/240s——上限的意图（拒绝无界扫描）不变。
4. marker 搜索下界回溯到 run 起点（覆盖 pause 期 trace），同时钳制
   `max(startedAt, drainEnd-150s)` 保持有界。
5. **reset-aware 会计**：counter 回退即重启的机器可判证据——此时 marker 命中是
   "积压已处理"的一等证据，`duplicate_delivered` 跨重启不可推导、记 0 且不宣称；
   只有 marker 未命中且 sent 不足才判 drain-incomplete。drain-incomplete 的契约
   测试夹具同步改为"marker 缺失 + sent 不足"双证据。
6. drain 轮询对 Tempo 查询错误容错（ctx 取消除外），窗口耗尽才返回最后错误——
   后端刚从 pause 恢复时的短暂 5xx 是预期路径而非终止条件。

## at-least-once 重复投递（SC-004 的诚实结论）

三份真实报告的 `duplicate_delivered` 均为 0，且这一轮**无法从 sent counter 推导
重复数**——重启使计数跨进程不可比（缺陷 5 的直接后果）。诚实结论是：

- 投递语义按契约声明为 **at-least-once**（`PersistentQueueDeliveryGuarantee`），
  恢复证据（marker 命中）成立；
- 本轮 **未观察到重复投递的证据，也不宣称没有发生**——计数不可比时重复数
  不可知，报告记 0 是"不宣称"而非"证实为零"；
- 要取得真实的 duplicate>0 证据，需要单进程内的 sent 连续计数窗口（不重启
  collector 的短暂 pause 恢复），或对 Tempo 内同一 traceID 的重复 span 计数。
  这作为后续实验记录在 resilience 清单的已知边界里，不冒充已完成。

## 学到了什么

- **验证工具自身的重启语义必须先于被验证对象被审视**。恢复场景的每个参与者
  （trigger、查询、会计）都在跨越重启边界，任何"计数单调/身份恒同/立即可见"
  的隐式假设都会在真实环境里变成失败。
- **fake 不是错的，错的是 fake 只有一种时间**。给 fake 补"延迟可见"与"永不
  上涨"两个时间维度后，离线契约才真正覆盖了传播时延类缺陷。
- **一个安全上限服务多个场景时，上限必须从场景参数推导**，而不是取第一个
  场景的值钉死——窗口上限与 drain 窗口从此在代码注释里互相引用。
- **"不可推导"要显式成为报告语义**。counter 不可比时把 duplicate 记 0 并在
  journal 说明，比静默报一个貌似合理的数字更接近事实；at-least-once 的声明
  价值恰恰在于承认重复可能发生。
- 排障路径本身可复用：低敏临时诊断（stderr 打印快照事实/查询错误）把五层
  异步边界里的失败点逐个钉出来，事后删除——比对着终态报告猜时序快得多。
