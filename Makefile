.PHONY: test test-race vet eval-smoke obs-smoke obs-platform-smoke verify obs-contract obs-smoke-offline obs-config-check obs-foundation-test obs-foundation-race obs-coverage obs-langfuse-bootstrap-up obs-langfuse-bootstrap-down obs-grafana-up obs-grafana-down obs-stack-health obs-infra-smoke obs-grafana-e2e

# 两条启动路径必须固定到同一个 project，才能复用首次冷启动创建的 Langfuse 数据卷。
# 本地 .env 文件可选：不存在时继续接受 shell export，存在时才交给 Compose 读取。
OBSERVABILITY_COMPOSE_PROJECT ?= longtermism-observability
OBSERVABILITY_LOCAL_ENV_FILE ?= deploy/observability/.env.local
OBSERVABILITY_LOCAL_ENV_OPTION := $(if $(wildcard $(OBSERVABILITY_LOCAL_ENV_FILE)),--env-file $(OBSERVABILITY_LOCAL_ENV_FILE),)
OBS_LANGFUSE_COMPOSE = docker compose --project-name $(OBSERVABILITY_COMPOSE_PROJECT) --env-file deploy/observability/versions.env$(if $(OBSERVABILITY_LOCAL_ENV_OPTION), $(OBSERVABILITY_LOCAL_ENV_OPTION)) -f deploy/observability/compose.langfuse.yaml
# Collector 的 non-root 进程必须读取宿主 JSONL 主组；所有 warm 命令（包括 ps/down）
# 都需传同一 GID，否则 Compose 会在解析 collector.user 时失败。
OBS_GRAFANA_COMPOSE = OBSERVABILITY_LOG_GID="$$(id -g)" $(OBS_LANGFUSE_COMPOSE) -f deploy/observability/compose.grafana.yaml

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

# 首次冷启动不能复用完整 profile：Compose 会在启动任何服务前解析 Collector 的
# Langfuse project key。此 target 只加载 Langfuse 服务，创建项目/API key 后再走 warm start。
obs-langfuse-bootstrap-up:
	$(OBS_LANGFUSE_COMPOSE) up -d --wait --wait-timeout 180 langfuse-web langfuse-worker

obs-langfuse-bootstrap-down:
	$(OBS_LANGFUSE_COMPOSE) down

# Level 1 是明确 opt-in 的本地 Grafana profile。默认 `verify` 绝不依赖 Docker 或
# Langfuse 凭据；这些命令也不删除 named volumes，避免把诊断证据当作清理副作用丢失。
obs-grafana-up:
	@set -eu; \
		for directory in resource resource/log resource/log/observability; do \
			if [ -L "$$directory" ]; then echo "refusing symlinked local log directory: $$directory" >&2; exit 2; fi; \
		done; \
		mkdir -p resource/log/observability; \
		chgrp "$$(id -g)" resource/log/observability; \
		chmod 0750 resource/log/observability
	$(OBS_GRAFANA_COMPOSE) up -d --wait --wait-timeout 180

obs-grafana-down:
	$(OBS_GRAFANA_COMPOSE) down

# T066 当前只支持 Grafana profile。状态输出仅辅助诊断，正式通过必须依赖后续的
# 查询式 infra smoke 报告，不能把 container healthy 当成 E2E 成功。
obs-stack-health:
	@test "$(OBS_PROFILE)" = "grafana" || { echo "OBS_PROFILE must be grafana" >&2; exit 2; }
	$(OBS_GRAFANA_COMPOSE) ps

obs-infra-smoke:
	@missing=''; \
		for variable in LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL; do \
			if [ -z "$$(printenv "$$variable")" ]; then missing="$$missing $$variable"; fi; \
		done; \
		if [ -n "$$missing" ]; then \
			printf '%s\n' "obs-infra-smoke preflight failed: missing required environment variables:$$missing" >&2; \
			printf '%s\n' "Start the local Grafana profile and application first; see deploy/observability/README.md." >&2; \
			exit 2; \
		fi
	@printf '%s\n' "obs-infra-smoke: querying the already-running local application and profile; it will not start services." >&2
	go run ./cmd/obs-smoke infra

# 无论 health 或查询 smoke 失败都停止当前 Compose profile；down 不带 -v，因此
# smoke 已写出的低敏报告和 named-volume 中的故障证据保持可诊断。
obs-grafana-e2e:
	@set -eu; \
		trap '$(OBS_GRAFANA_COMPOSE) down' EXIT; \
		$(MAKE) obs-grafana-up; \
		$(MAKE) obs-stack-health OBS_PROFILE=grafana; \
		$(MAKE) obs-infra-smoke
