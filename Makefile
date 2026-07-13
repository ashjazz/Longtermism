.PHONY: test test-race vet eval-smoke obs-smoke obs-platform-smoke verify obs-contract obs-smoke-offline obs-config-check

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

# 本地平台接入契约 smoke：验证最小双平面 payload、显式启用与低敏边界。
# 它不连接真实 Collector 或后端；真实平台查询由后续 obs-*-e2e 命令承担。
obs-platform-smoke:
	go test ./internal/eval/smoke -run 'TestResolvePlatformSmokeConfig|TestPlatformSmoke' -count=1
