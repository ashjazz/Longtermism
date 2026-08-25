# Live 失败报告未持久化

- **报告日期**：2026-08-25
- **修复日期**：2026-08-25
- **报告来源**：用户聊天与本地 live smoke 结果
- **报告人**：项目维护者
- **严重程度**：P2

## 现象描述

Chat live smoke 的真实模型触发失败后，runner 已经构造出低敏失败报告，但命令在收到非空错误时立即退出，导致报告没有写入受控工件目录，也没有输出可供定位的报告路径。

## 根因

`cmd/obs-smoke/main.go` 的 `runLiveScenario` 把 `runner.Run` 返回的 `error` 与 `nil report` 合并成同一个提前返回条件。该判断忽略了 chat runner 的既有契约：一次 API 尝试完成后，可以同时返回一份完整失败报告和原始触发错误。

## 修复方案

仅在报告为 `nil` 时提前失败。只要报告存在，就先通过受控 writer 持久化并校验可信路径，再输出 `scenario`、`status`、`report_path` 三字段低敏摘要；runner error 仍强制最终退出码为 `1`，且原始错误不进入 stdout 或 stderr。

## 修改文件

- `cmd/obs-smoke/main.go` — 修正失败报告的持久化顺序与退出码判断。
- `cmd/obs-smoke/main_test.go` — 增加 report+error 回归用例、报告对象传递断言和双输出通道泄漏检查。

## 关联 Commit

- 未创建；本次按用户要求保留为工作区变更。

## 验证结果

- [x] 离线回归用例复现 RED，并在修复后转为 GREEN
- [x] `go build ./...`
- [x] `go vet ./...`
- [x] `go test ./... -count=1`
- [x] `go test ./... -count=1 -cover`
- [x] `go test -race ./... -count=1`
- [x] 通用、Go 与安全审查通过，均无阻塞问题
- [x] `runLiveScenario` 覆盖率为 90.6%
- [ ] `make obs-coverage`：全仓 changed-core 为 79.2%，低于 80% 门槛；这是已有 release gate 欠账，本修复未降低阈值或扩大排除范围
- [ ] 真实 live 复测：本轮修复按约束仅使用离线替身，等待下一次显式 live 授权
