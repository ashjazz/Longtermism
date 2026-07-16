.PHONY: test test-race vet eval-smoke obs-smoke obs-platform-smoke verify obs-contract obs-smoke-offline obs-config-check obs-foundation-test obs-foundation-race obs-coverage

# Level 0 默认离线门禁：只运行本地 Go 检查，既不启动 Docker，也不要求 LLM/Langfuse 凭据。
verify: vet test

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

eval-smoke:
	go run ./cmd/eval-smoke

obs-smoke:
	go test ./internal/cmd -count=1
	go test ./internal/eval/smoke -count=1
	go test ./pkg/ai/obs ./pkg/ai/obs/testutil -count=1

# 关联、隐私与 OTel 映射的本地契约测试；真实后端查询由后续 E2E 目标负责。
obs-contract: obs-smoke

# 受控 sender 的离线 smoke，不读取环境中的真实 provider 或平台凭据。
obs-smoke-offline:
	go run ./cmd/eval-smoke --smoke observability-chain

# 只解析受版本管理的静态配置；部署资产未落地时必须 fail closed。
obs-config-check:
	bash hack/observability/config_check.sh

# Foundational observability 只运行离线单测。chat usecase 在其目录创建后自动纳入；
# 不存在时不伪造空包，避免在 T068/T070 前让 `go test` 因路径不存在失败。
obs-foundation-test:
	@packages='./internal/cmd/... ./internal/observability/... ./pkg/ai/obs/...'; \
	if [ -d internal/logic/chat ]; then packages="$$packages ./internal/logic/chat/..."; fi; \
	go test -count=1 $$packages

obs-foundation-race:
	@packages='./internal/cmd/... ./internal/observability/... ./pkg/ai/obs/...'; \
	if [ -d internal/logic/chat ]; then packages="$$packages ./internal/logic/chat/..."; fi; \
	go test -race -count=1 $$packages

# 使用同一次无缓存 atomic profile 检查 merge-base 以来的核心可执行行。checker 只
# allowlist 测试与明确 generated 文件；变更生产文件缺 profile 会失败，不能借缓存或
# 排除失败包绕过。chat 目录创建后还会按全部生产行执行独立的 90% 门禁。
obs-coverage:
	@mkdir -p build/coverage
	@packages='./internal/cmd/... ./internal/observability/... ./pkg/ai/obs/...'; \
	if [ -d internal/logic/chat ]; then packages="$$packages ./internal/logic/chat/..."; fi; \
	go test -count=1 -covermode=atomic -coverprofile=build/coverage/observability.out $$packages
	bash hack/observability/coverage_check.sh --profile build/coverage/observability.out --base origin/main --threshold 80 --scope internal/cmd --scope internal/observability --scope internal/logic/chat --scope pkg/ai/obs

# 本地平台接入契约 smoke：验证最小双平面 payload、显式启用与低敏边界。
# 它不连接真实 Collector 或后端；真实平台查询由后续 obs-*-e2e 命令承担。
obs-platform-smoke:
	go test ./internal/eval/smoke -run 'TestResolvePlatformSmokeConfig|TestPlatformSmoke' -count=1
