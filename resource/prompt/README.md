# Prompt 资产

`resource/prompt` 存放本地 prompt 模板。Prompt 必须像代码一样版本化、可审查、可测试，并能通过 trace 追踪到模板名称、版本和渲染 hash。

## 命名约定

模板按能力或场景建立目录，目录名使用小写 snake_case：

```text
resource/prompt/
  p0_smoke/
    v1.tmpl
  rag_answer/
    v1.tmpl
    v2.tmpl
```

约束：

- 目录名表达业务能力或评估场景，例如 `p0_smoke`、`rag_answer`。
- 版本文件使用 `vN.tmpl`，从 `v1.tmpl` 开始递增。
- 不在原文件上覆盖历史语义；需要改变提示词行为时新增版本。
- 模板文件只保存提示词内容，不保存 API key、token、用户隐私或真实生产样本原文。

## 变量约定

模板变量使用 Go `text/template` 风格：

```gotemplate
请回答用户问题：{{ .Question }}
可用上下文：
{{ .Context }}
```

约束：

- 变量名使用 `PascalCase`，与 Go 侧渲染数据字段保持一致。
- 新增变量时必须同步更新对应测试、golden case 或 smoke 样例。
- 不允许模板依赖隐式全局变量；所有变量必须由调用方显式传入。
- 缺失变量必须渲染失败，不能静默替换为空字符串。实现侧应使用 `missingkey=error` 或等价机制。

## 版本与追踪

每次渲染后的 prompt 都应能追踪到：

- 模板名称，例如 `p0_smoke`。
- 模板版本，例如 `v1`。
- 渲染内容 hash，例如 SHA-256 短 hash。
- 调用场景或关联 eval case ID。

普通 trace 不保存完整 prompt 原文，只记录名称、版本、hash、长度、模型、用量和状态等安全字段。
