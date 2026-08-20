.PHONY: test test-race vet eval-smoke obs-smoke obs-platform-smoke verify obs-contract obs-smoke-offline obs-config-check obs-foundation-test obs-foundation-race obs-coverage obs-langfuse-bootstrap-up obs-langfuse-bootstrap-down obs-grafana-up obs-grafana-down obs-stack-health obs-infra-smoke obs-chat-smoke obs-langfuse-score-smoke obs-privacy-platform-smoke obs-direct-langfuse-smoke obs-grafana-e2e obs-signoz-up obs-signoz-down obs-signoz-infra-smoke obs-signoz-chat-smoke obs-signoz-e2e obs-exporter-failure-smoke obs-persistent-queue-smoke obs-score-worker-failure-smoke obs-resilience-e2e obs-reset

# 两条启动路径必须固定到同一个 project，才能复用首次冷启动创建的 Langfuse 数据卷。
# 本地 .env 文件可选：不存在时继续接受 shell export，存在时才交给 Compose 读取。
OBSERVABILITY_COMPOSE_PROJECT ?= longtermism-observability
OBSERVABILITY_LOCAL_ENV_FILE ?= deploy/observability/.env.local
OBSERVABILITY_LOCAL_ENV_OPTION := $(if $(wildcard $(OBSERVABILITY_LOCAL_ENV_FILE)),--env-file $(OBSERVABILITY_LOCAL_ENV_FILE),)
OBS_LANGFUSE_COMPOSE = docker compose --project-name $(OBSERVABILITY_COMPOSE_PROJECT) --env-file deploy/observability/versions.env$(if $(OBSERVABILITY_LOCAL_ENV_OPTION), $(OBSERVABILITY_LOCAL_ENV_OPTION)) -f deploy/observability/compose.langfuse.yaml
# Collector 以固定 non-root 身份接收 OTLP logs；启动不再读取宿主 JSONL 或依赖宿主 GID。
OBS_GRAFANA_COMPOSE = $(OBS_LANGFUSE_COMPOSE) -f deploy/observability/compose.grafana.yaml

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

# live smoke 是显式 opt-in 的真实验证：全部要求 `--live`，只查询已运行的服务，
# 绝不启动 Docker。预检引用名与 T181 契约逐字一致；缺失时只打印变量名。
define check-live-smoke-refs
	@missing=''; \
		for variable in $(1); do \
			if [ -z "$$(printenv "$$variable")" ]; then missing="$$missing $$variable"; fi; \
		done; \
		if [ -n "$$missing" ]; then \
			printf '%s\n' "$(2) preflight failed: missing required environment variables:$$missing" >&2; \
			printf '%s\n' "Start the local Grafana profile and application first; see deploy/observability/README.md." >&2; \
			exit 2; \
		fi
	@printf '%s\n' "$(2): querying the already-running local application and profile; it will not start services." >&2
endef

obs-chat-smoke:
	$(call check-live-smoke-refs,LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_CHAT_AUTHORIZATION LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL,obs-chat-smoke)
	go run ./cmd/obs-smoke chat --live

obs-langfuse-score-smoke:
	$(call check-live-smoke-refs,LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH,obs-langfuse-score-smoke)
	go run ./cmd/obs-smoke score --live

obs-privacy-platform-smoke:
	$(call check-live-smoke-refs,LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_CHAT_AUTHORIZATION LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION,obs-privacy-platform-smoke)
	go run ./cmd/obs-smoke privacy --live

# direct Langfuse 诊断只证明 ingestion 可达且凭据可用，不产生查询报告，也不属于
# obs-grafana-e2e 的通过依据：正式验收只认查询式 smoke 报告。
obs-direct-langfuse-smoke:
	@missing=''; \
		for variable in LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL; do \
			if [ -z "$$(printenv "$$variable")" ]; then missing="$$missing $$variable"; fi; \
		done; \
		if [ -n "$$missing" ]; then \
			printf '%s\n' "obs-direct-langfuse-smoke preflight failed: missing required environment variables:$$missing" >&2; \
			exit 2; \
		fi
	@printf '%s\n' "obs-direct-langfuse-smoke: direct ingestion/auth diagnostic; it produces no smoke report." >&2
	@health=$$(curl -fsS -o /dev/null -w '%{http_code}' "$${LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL}/api/health") || exit 1; \
		if [ "$$health" != "200" ]; then printf '%s\n' "Langfuse health diagnostic failed: http=$$health" >&2; exit 1; fi; \
		printf 'Langfuse health diagnostic: http=%s\n' "$$health" >&2; \
		auth=$$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Basic $${LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL}" "$${LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL}/api/public/observations?limit=1") || exit 1; \
		if [ "$$auth" = "401" ] || [ "$$auth" = "403" ] || [ "$$auth" = "500" ] || [ "$$auth" = "502" ] || [ "$$auth" = "503" ]; then printf '%s\n' "Langfuse credential diagnostic failed: http=$$auth" >&2; exit 1; fi; \
		printf 'Langfuse credential diagnostic: http=%s\n' "$$auth" >&2

# 无论 health 或任一查询 smoke 失败都停止当前 Compose profile；down 不带 -v，因此
# smoke 已写出的低敏报告和 named-volume 中的故障证据保持可诊断。聚合顺序是事实依赖：
# infra 基线 -> chat（产生投影）-> score（验证异步投影）-> privacy（八 surface 负向）。
# direct 诊断不是该门禁的一部分，它只服务人工排障。
obs-grafana-e2e:
	@set -eu; \
		trap '$(OBS_GRAFANA_COMPOSE) down' EXIT; \
		$(MAKE) obs-grafana-up; \
		$(MAKE) obs-stack-health OBS_PROFILE=grafana; \
		$(MAKE) obs-infra-smoke; \
		$(MAKE) obs-chat-smoke; \
		$(MAKE) obs-langfuse-score-smoke; \
		$(MAKE) obs-privacy-platform-smoke

# ── SigNoz 备选 profile（T148）────────────────────────────────────────────
# 支持声明前置：只有在 Grafana 主线（checklists/real-backend-acceptance.md）
# 完成验收后，本节目标才构成对备选方案的支持声明；备选 profile 的通过证据
# 不改变主线优先级（checklists/signoz.md 定位声明）。
# 独立 compose project：与 Grafana 主线互斥运行，Langfuse 栈在各自 project
# 内独立实例化，观测卷互不可见（T141 契约），失败清理只触碰本 project。
OBSERVABILITY_SIGNOZ_COMPOSE_PROJECT ?= longtermism-signoz
OBS_SIGNOZ_COMPOSE = docker compose --project-name $(OBSERVABILITY_SIGNOZ_COMPOSE_PROJECT) --env-file deploy/observability/versions.env$(if $(OBSERVABILITY_LOCAL_ENV_OPTION), $(OBSERVABILITY_LOCAL_ENV_OPTION)) -f deploy/observability/compose.langfuse.yaml -f deploy/observability/compose.signoz.yaml

obs-signoz-up:
	$(OBS_SIGNOZ_COMPOSE) up -d --wait --wait-timeout 240

# down 不带 -v：低敏 smoke 报告与 named-volume 中的故障证据保持可诊断，
# 且清理严格限定本 compose project（门控：不触碰 Grafana 主线 project）。
obs-signoz-down:
	$(OBS_SIGNOZ_COMPOSE) down

# 备选 profile 的 infra smoke：与主线同一查询式验收（绝不以 compose healthy
# 代替查询），预检只引用变量名；ingestion key 可选（查询认证在未启用时省略）。
obs-signoz-infra-smoke:
	@missing=''; \
		for variable in LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL; do \
			if [ -z "$$(printenv "$$variable")" ]; then missing="$$missing $$variable"; fi; \
		done; \
		if [ -n "$$missing" ]; then \
			printf '%s\n' "obs-signoz-infra-smoke preflight failed: missing required environment variables:$$missing" >&2; \
			printf '%s\n' "Start the local SigNoz profile and application first; see deploy/observability/README.md." >&2; \
			exit 2; \
		fi
	@printf '%s\n' "obs-signoz-infra-smoke: querying the already-running SigNoz profile; it will not start services." >&2
	LONGTERMISM_SMOKE_PROFILE=signoz go run ./cmd/obs-smoke infra --profile=signoz

obs-signoz-chat-smoke:
	@missing=''; \
		for variable in LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_CHAT_AUTHORIZATION LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL; do \
			if [ -z "$$(printenv "$$variable")" ]; then missing="$$missing $$variable"; fi; \
		done; \
		if [ -n "$$missing" ]; then \
			printf '%s\n' "obs-signoz-chat-smoke preflight failed: missing required environment variables:$$missing" >&2; \
			printf '%s\n' "Start the local SigNoz profile and application first; see deploy/observability/README.md." >&2; \
			exit 2; \
		fi
	@printf '%s\n' "obs-signoz-chat-smoke: querying the already-running SigNoz profile; it will not start services." >&2
	LONGTERMISM_SMOKE_PROFILE=signoz go run ./cmd/obs-smoke chat --live --profile=signoz

# 备选 E2E 聚合顺序与主线同构：compose up -> infra（三信号+AI 负向）-> chat
# （三信号+Langfuse trace/score）。score/privacy 的证据面绑定主线后端，不在
# 备选 E2E 内伪装。失败时 trap 清理只针对本 project 的 compose down。
obs-signoz-e2e:
	@set -eu; \
		trap '$(OBS_SIGNOZ_COMPOSE) down' EXIT; \
		$(MAKE) obs-signoz-up; \
		$(MAKE) obs-signoz-infra-smoke; \
		$(MAKE) obs-signoz-chat-smoke

# T131：US3 破坏性 live smoke 目标。全部显式 `--live` opt-in，只操作已运行
# 服务、绝不启动 Docker；预检引用名与 T130 钉死的 CLI 契约逐字一致。
# live composition 已收敛：exporter-failure / persistent-queue / score-worker
# 的 langfuse-api case 为真实装配；仅 score-worker 的 queue-full 与 shutdown
# 两个 case 仍走 CLI 能力哨兵（组合报告里呈现为 preflight 失败行）。

obs-exporter-failure-smoke:
	$(call check-live-smoke-refs,LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT,obs-exporter-failure-smoke)
	go run ./cmd/obs-smoke exporter-failure --target tempo --live

obs-persistent-queue-smoke:
	$(call check-live-smoke-refs,LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT,obs-persistent-queue-smoke)
	go run ./cmd/obs-smoke persistent-queue --live

obs-score-worker-failure-smoke:
	$(call check-live-smoke-refs,LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_CHAT_AUTHORIZATION LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT,obs-score-worker-failure-smoke)
	go run ./cmd/obs-smoke score-worker-failure --case langfuse-api --live

# full aggregate 由 CLI 串行编排 7 个子场景（3 exporter target + persistent-queue
# + 3 score-worker case），子报告先于聚合报告落盘。trap 先 unpause 项目内全部
# 服务再 down：中断（INT/TERM）后 paused services 必须被恢复，不能靠 down 兜底
# 掩盖"服务仍处于 paused"的运营事实。
obs-resilience-e2e:
	@set -eu; \
		trap '$(OBS_GRAFANA_COMPOSE) unpause 2>/dev/null || true; $(OBS_GRAFANA_COMPOSE) down' EXIT INT TERM; \
		$(MAKE) obs-grafana-up; \
		$(MAKE) obs-stack-health OBS_PROFILE=grafana; \
		go run ./cmd/obs-smoke resilience --live

# 破坏性重置：无 CONFIRM_RESET=1 直接拒绝（exit 2），不进入 reset 脚本。
# reset 本身 label-scoped：只删当前 project + longtermism.observability=true
# 的卷，绝不 prune，不触碰应用数据。OBS_SMOKE_RUN_ROOT 可选，指向 smoke 报告
# 根目录时一并清理被中断运行的 run-* 残留。
OBS_SMOKE_RUN_ROOT ?=
obs-reset:
	@test "$(CONFIRM_RESET)" = "1" || { printf '%s\n' 'obs-reset is destructive: rerun with CONFIRM_RESET=1 to delete observability volumes and run residue' >&2; exit 2; }
	@if [ -n "$(OBS_SMOKE_RUN_ROOT)" ]; then \
		bash hack/observability/reset.sh --project-name "$(OBSERVABILITY_COMPOSE_PROJECT)" --run-root "$(OBS_SMOKE_RUN_ROOT)" --confirm; \
	else \
		bash hack/observability/reset.sh --project-name "$(OBSERVABILITY_COMPOSE_PROJECT)" --confirm; \
	fi
