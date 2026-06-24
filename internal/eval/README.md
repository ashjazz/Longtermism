# Internal Eval

`internal/eval` 存放应用层的本地评估资产和评估冒烟组合。它负责把 `pkg/ai` 中的可复用评估能力接入到项目默认门禁，但不把评估框架本身绑定到 GoFrame 或真实外部服务。

## 目录职责

```text
internal/eval/
  golden/   稳定的评估样例数据，供 eval runner 和回归门禁读取。
  smoke/    本地冒烟流程组合，负责串联 prompt、fake llm、trace recorder 和 eval runner。
```

### `golden/`

`golden/` 用于保存小而稳定的 golden case。每个样例都应有稳定 ID、输入、期望结果或相关上下文，并能被确定性指标重复评分。

约束：

- 样例必须适合本地重复运行。
- 样例不应依赖实时外部 API、真实向量库或线上可观测平台。
- 样例不得包含 API key、token、用户隐私、完整敏感 prompt 或原始 tool 参数。
- 新增样例时，应说明它覆盖的能力、失败模式或回归风险。

### `smoke/`

`smoke/` 用于保存 P0 本地冒烟流程。它验证的是最小工程闭环是否能跑通：

```text
prompt -> fake/model interaction -> trace recorder -> eval runner -> report
```

默认 smoke 必须使用 fake provider、in-memory recorder、本地 prompt 和本地 golden case。真实模型服务、真实向量库、LangFuse、OTEL 或其他平台集成只能作为显式 opt-in 的扩展 smoke，不能进入默认门禁。

## 默认评估边界

默认评估不依赖真实模型服务，也不要求 API key。这样做是为了保证本地开发、CI 和学习复盘都具备稳定、低成本、可重复的反馈。

真实服务 smoke 可以在后续任务中加入，但必须满足：

- 通过显式环境变量或命令参数开启。
- 失败时标记为外部服务 smoke 失败，不覆盖离线确定性评估结论。
- 输出不得泄露原始敏感输入、完整 prompt 或凭据。
- 不作为默认 `make eval-smoke` 的必要前置，除非后续 ADR 明确改变门禁策略。
