#!/usr/bin/env bash
# T136：静态验证 SigNoz 备选 profile wiring；只解析受版本管理配置，绝不启动 Docker 或访问后端。
# 这是 US4 的 RED 契约测试：deploy/observability/compose.signoz.yaml 由 T141 实现前，本脚本必须失败。
# 质量门控维度：固定版本、healthcheck/resource limits、retention、loopback UI、
# Collector 边界、Langfuse 栈保留、12GiB/8vCPU/20GiB 预算。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly COMPOSE_PATH="${REPO_ROOT}/deploy/observability/compose.signoz.yaml"
readonly LANGFUSE_COMPOSE_PATH="${REPO_ROOT}/deploy/observability/compose.langfuse.yaml"
readonly VERSIONS_PATH="${REPO_ROOT}/deploy/observability/versions.env"

[[ -f "${COMPOSE_PATH}" ]] || { printf '%s: missing_signoz_compose\n' "${COMPOSE_PATH}" >&2; exit 1; }
[[ -f "${LANGFUSE_COMPOSE_PATH}" ]] || { printf '%s: missing_langfuse_compose\n' "${LANGFUSE_COMPOSE_PATH}" >&2; exit 1; }
[[ -f "${VERSIONS_PATH}" ]] || { printf '%s: missing_version_matrix\n' "${VERSIONS_PATH}" >&2; exit 1; }

exec ruby -ropen3 -ryaml -e '
repo_root, compose_path, langfuse_compose_path, versions_path = ARGV.freeze

def fail_check(message)
  warn "#{ARGV.fetch(0)}: #{message}"
  exit 1
end

def yaml(path)
  YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: true) || {}
rescue Psych::Exception
  fail_check("invalid_yaml:#{File.basename(path)}")
end

def required_hash(value, category)
  fail_check(category) unless value.is_a?(Hash)
  value
end

def scalar_strings(value)
  case value
  when Hash then value.flat_map { |key, nested| [key.to_s] + scalar_strings(nested) }
  when Array then value.flat_map { |nested| scalar_strings(nested) }
  else [value.to_s]
  end
end

def parse_versions(path)
  File.readlines(path, chomp: true).each_with_object({}) do |line, values|
    next if line.start_with?("#") || line.strip.empty?
    key, value = line.split("=", 2)
    values[key] = value if key && value
  end
end

def memory_bytes(value)
  match = /\A([1-9][0-9]*)(MiB|GiB)\z/i.match(value.to_s)
  fail_check("invalid_memory_limit") unless match
  multiplier = match[2].downcase == "gib" ? 1024 * 1024 * 1024 : 1024 * 1024
  match[1].to_i * multiplier
end

def cpu_cores(value)
  number = Float(value)
  fail_check("invalid_cpu_limit") unless number.positive?
  number
rescue ArgumentError, TypeError
  fail_check("invalid_cpu_limit")
end

def volume_source?(mount)
  mount.is_a?(Hash) && mount["type"] == "volume" && mount["source"].is_a?(String) && !mount["source"].empty?
end

def sensitive_values(value, prefix = "")
  case value
  when Hash
    value.flat_map { |key, nested| sensitive_values(nested, "#{prefix}.#{key}") }
  when Array
    value.flat_map { |nested| sensitive_values(nested, prefix) }
  else
    [[prefix, value.to_s]]
  end
end

def dependency_names(value)
  value.is_a?(Hash) ? value.keys : Array(value)
end

def has_mount?(mounts, type:, source:, target:, read_only: nil)
  mounts.any? do |mount|
    mount.is_a?(Hash) && mount["type"] == type && mount["source"] == source && mount["target"] == target && (read_only.nil? || mount["read_only"] == read_only)
  end
end

compose = required_hash(yaml(compose_path), "invalid_compose")
langfuse_compose = required_hash(yaml(langfuse_compose_path), "invalid_langfuse_compose")
versions = parse_versions(versions_path)
signoz_services = required_hash(compose["services"], "missing_compose_services")
langfuse_services = required_hash(langfuse_compose["services"], "missing_langfuse_compose_services")
services = signoz_services.merge(langfuse_services)
fail_check("duplicate_split_service") unless services.length == signoz_services.length + langfuse_services.length

# 服务拓扑契约：SigNoz profile 用 signoz/signoz-otel-collector/signoz-clickhouse 替换
# 主线的 prometheus/loki/tempo/grafana 四个后端，但保留同一 Collector 入口（contrib 镜像、
# distroless sidecar 健康探测与 queue 初始化模式）与整个 Langfuse AI 平面。
# 精确清单匹配同时防止：主线后端残留（半迁移状态）与意外的多余服务（预算外的资源消耗）。
required_signoz_services = %w[collector collector-health-probe collector-storage-init signoz signoz-otel-collector signoz-clickhouse]
required_langfuse_services = %w[langfuse-web langfuse-worker langfuse-db langfuse-clickhouse langfuse-clickhouse-init langfuse-redis langfuse-minio langfuse-minio-init]
fail_check("invalid_signoz_profile_services") unless signoz_services.keys.sort == required_signoz_services.sort
fail_check("invalid_langfuse_profile_services") unless langfuse_services.keys.sort == required_langfuse_services.sort
retired_mainline_backends = %w[prometheus loki tempo grafana loki-health-probe tempo-health-probe]
fail_check("mainline_backend_leaked_into_signoz_profile") if (services.keys & retired_mainline_backends).any?

# 自定义 network 属于部署环境职责；profile 内服务一律使用 Compose 默认网络。
fail_check("custom_compose_network_forbidden") if [compose, langfuse_compose].any? { |document| document.key?("networks") } || services.values.any? { |service| service.key?("networks") }

# Collector 是唯一的平台 fan-out 边界：AI 分支凭据只允许出现在 signoz profile 的
# collector 服务（${VAR:?} fail-fast 形式），bootstrap 层（langfuse compose）不得泄漏。
collector_environment = required_hash(signoz_services.fetch("collector")["environment"], "missing_collector_langfuse_configuration")
{
  "LANGFUSE_OTLP_AUTHORIZATION" => "LANGFUSE_OTLP_AUTHORIZATION",
  "LANGFUSE_OTEL_INGESTION_VERSION" => "LANGFUSE_OTEL_INGESTION_VERSION"
}.each do |key, variable|
  value = collector_environment[key]
  fail_check("missing_collector_langfuse_configuration") unless value.is_a?(String) && value.match?(/\A\$\{#{Regexp.escape(variable)}:\?[^}]+\}\z/)
end
fail_check("collector_leaked_into_bootstrap") if scalar_strings(langfuse_compose).any? { |value| value.include?("LANGFUSE_OTLP_AUTHORIZATION") || value.include?("LANGFUSE_OTEL_INGESTION_VERSION") }

# Langfuse AI 平面在备选 profile 中必须原样保留（research.md 决策 10）：
# 应用配置与埋点不因 profile 改变，Langfuse 继续接收 AI trace/score。
%w[langfuse-web langfuse-worker].each do |service_name|
  environment = required_hash(langfuse_services.fetch(service_name)["environment"], "missing_langfuse_encryption_key")
  value = environment["ENCRYPTION_KEY"]
  fail_check("missing_langfuse_encryption_key") unless value.is_a?(String) && value.match?(/\A\$\{LANGFUSE_ENCRYPTION_KEY:\?[^}]+\}\z/)
  {
    "CLICKHOUSE_USER" => "LANGFUSE_CLICKHOUSE_USER",
    "CLICKHOUSE_PASSWORD" => "LANGFUSE_CLICKHOUSE_PASSWORD",
    "LANGFUSE_S3_EVENT_UPLOAD_BUCKET" => "LANGFUSE_S3_EVENT_UPLOAD_BUCKET",
    "LANGFUSE_S3_EVENT_UPLOAD_ACCESS_KEY_ID" => "LANGFUSE_MINIO_ACCESS_KEY_ID",
    "LANGFUSE_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY" => "LANGFUSE_MINIO_SECRET_ACCESS_KEY"
  }.each do |key, variable|
    value = environment[key]
    fail_check("missing_langfuse_storage_configuration:#{service_name}:#{key}") unless value.is_a?(String) && value.match?(/\A\$\{#{Regexp.escape(variable)}:\?[^}]+\}\z/)
  end
  fail_check("invalid_langfuse_clickhouse_cluster_mode:#{service_name}") unless environment["CLICKHOUSE_CLUSTER_ENABLED"] == "false"
  fail_check("invalid_langfuse_s3_endpoint:#{service_name}") unless environment["LANGFUSE_S3_EVENT_UPLOAD_ENDPOINT"] == "http://langfuse-minio:9000"
  fail_check("invalid_langfuse_s3_path_style:#{service_name}") unless environment["LANGFUSE_S3_EVENT_UPLOAD_FORCE_PATH_STYLE"] == "true"
end
langfuse_web = services.fetch("langfuse-web")
fail_check("missing_langfuse_web_heap_limit") unless langfuse_web.dig("environment", "NODE_OPTIONS") == "--max-old-space-size=1536"
fail_check("invalid_langfuse_web_memory_limit") unless langfuse_web.dig("deploy", "resources", "limits", "memory") == "2GiB"

# 版本固定契约：所有镜像经 versions.env 变量解析，禁止 latest（T003 已 pin SigNoz 三镜像）。
image_variables = {
  "collector" => "OTELCOL_CONTRIB_IMAGE", "collector-health-probe" => "COLLECTOR_STORAGE_INIT_IMAGE", "signoz" => "SIGNOZ_IMAGE",
  "signoz-otel-collector" => "SIGNOZ_OTELCOL_IMAGE", "signoz-clickhouse" => "SIGNOZ_CLICKHOUSE_IMAGE",
  "langfuse-web" => "LANGFUSE_IMAGE", "langfuse-worker" => "LANGFUSE_WORKER_IMAGE", "langfuse-db" => "LANGFUSE_POSTGRES_IMAGE",
  "langfuse-clickhouse" => "LANGFUSE_CLICKHOUSE_IMAGE", "langfuse-redis" => "LANGFUSE_REDIS_IMAGE", "langfuse-minio" => "LANGFUSE_MINIO_IMAGE"
}
image_variables.each do |service_name, variable|
  service = required_hash(services[service_name], "invalid_service:#{service_name}")
  fail_check("invalid_fixed_image:#{service_name}") unless service["image"] == "${#{variable}}" && versions.fetch(variable, "").include?(":") && !versions.fetch(variable, "").include?(":latest")
  limits = service.dig("deploy", "resources", "limits")
  fail_check("missing_resource_limit:#{service_name}") unless limits.is_a?(Hash) && limits.key?("cpus") && limits.key?("memory")
end
# 死变量防护：versions.env 中已 pin 的 SigNoz 镜像必须被 profile 引用，
# 避免“看起来支持备选方案、实际从未部署”的假兼容声明。
%w[SIGNOZ_IMAGE SIGNOZ_OTELCOL_IMAGE SIGNOZ_CLICKHOUSE_IMAGE].each do |variable|
  fail_check("pinned_signoz_image_unused:#{variable}") unless image_variables.value?(variable)
end

# healthcheck 分层契约：
# - signoz/signoz-clickhouse 是查询与数据面，必须自带结构合法的健康探测；
# - collector 与 signoz-otel-collector 是 distroless Collector 镜像，无 shell 可用，
#   collector 用 sidecar probe（见下），signoz-otel-collector 的健康性由 E2E 查询闭环
#   （T139/T143）证明，禁止伪造 shell 探测；
# - langfuse-worker 无健康端点（主线先例），initializer 是 one-shot 任务不应有 healthcheck。
healthcheck_required = %w[signoz signoz-clickhouse langfuse-web langfuse-db langfuse-clickhouse langfuse-redis langfuse-minio]
healthcheck_required.each do |service_name|
  healthcheck = services.fetch(service_name)["healthcheck"]
  fail_check("missing_healthcheck:#{service_name}") unless healthcheck.is_a?(Hash) && healthcheck["test"].is_a?(Array) && healthcheck["test"].length > 1 && healthcheck["interval"].to_s.match?(/\A[1-9][0-9]*s\z/) && healthcheck["timeout"].to_s.match?(/\A[1-9][0-9]*s\z/) && healthcheck["retries"].is_a?(Integer) && healthcheck["retries"].positive?
  fail_check("unsafe_healthcheck:#{service_name}") if healthcheck && scalar_strings(healthcheck["test"]).any? { |value| value.match?(/\b(?:true|echo|exit 0)\b/i) }
end
%w[collector signoz-otel-collector langfuse-worker].each { |service_name| fail_check("unexpected_distroless_healthcheck:#{service_name}") if services.fetch(service_name).key?("healthcheck") }

# distroless collector 的健康探测 sidecar：非特权、只读根文件系统、最小能力集。
probe = services.fetch("collector-health-probe")
fail_check("missing_health_probe_dependency") unless probe.dig("depends_on", "collector", "condition") == "service_started"
fail_check("invalid_health_probe_user") unless probe["user"] == "65534:65534"
fail_check("health_probe_root_filesystem_writable") unless probe["read_only"] == true
fail_check("health_probe_capabilities_not_minimized") unless Array(probe["cap_drop"]) == ["ALL"] && !probe.key?("cap_add")

# persistent queue 目录初始化（one-shot）：queue 子目录按 exporter 目标命名，
# 对应 T137/T142 契约——infra 三信号经 otlp/signoz exporter，AI 分支经 otlphttp/langfuse，
# 各自独立的 queue 命名空间让分出口的积压归因成为可能。
{
  "collector-storage-init" => "COLLECTOR_STORAGE_INIT_IMAGE", "langfuse-minio-init" => "LANGFUSE_MINIO_MC_IMAGE", "langfuse-clickhouse-init" => "LANGFUSE_CLICKHOUSE_IMAGE"
}.each do |service_name, variable|
  initializer = required_hash(services.fetch(service_name), "invalid_service:#{service_name}")
  fail_check("invalid_initializer_image:#{service_name}") unless initializer["image"] == "${#{variable}}" && versions.fetch(variable, "").include?(":") && !versions.fetch(variable, "").include?(":latest")
  limits = initializer.dig("deploy", "resources", "limits")
  fail_check("missing_initializer_resource_limit:#{service_name}") unless limits.is_a?(Hash) && limits.key?("cpus") && limits.key?("memory")
  fail_check("invalid_initializer_restart:#{service_name}") unless initializer["restart"] == "no"
  fail_check("unexpected_initializer_healthcheck:#{service_name}") if initializer.key?("healthcheck")
end
collector_storage_init = services.fetch("collector-storage-init")
fail_check("invalid_collector_storage_initializer_user") unless collector_storage_init["user"] == "0:0"
collector_storage_init_command = Array(collector_storage_init["entrypoint"]).last
%w[mkdir\ -p /var/lib/otelcol/storage/queue/signoz /var/lib/otelcol/storage/queue/langfuse /var/lib/otelcol/storage/queue/signoz-compaction /var/lib/otelcol/storage/queue/langfuse-compaction chown\ 10001: chmod\ 0750].each do |required_fragment|
  fail_check("invalid_collector_storage_initializer_command") unless collector_storage_init_command.is_a?(String) && collector_storage_init_command.include?(required_fragment)
end
fail_check("collector_storage_initializer_network_exposed") unless collector_storage_init["network_mode"] == "none"
fail_check("collector_storage_initializer_root_filesystem_writable") unless collector_storage_init["read_only"] == true
fail_check("collector_storage_initializer_capabilities_not_minimized") unless Array(collector_storage_init["cap_drop"]) == ["ALL"] && Array(collector_storage_init["cap_add"]).sort == %w[CHOWN DAC_OVERRIDE FOWNER]

# retention 契约（runtime-configuration.md §9 基线在 profile 中的声明锚点）：
# SigNoz 替换的三信号沿用被替换后端的保留基线——metrics 15d（Prometheus 基线）、
# logs/traces 7d（Loki/Tempo 基线）；Langfuse 14d 保持 AI 平面自身基线。
# 声明是静态契约锚点，容器内实际 TTL 由 T141 落到 SigNoz/ClickHouse 配置并被 E2E 证明。
budget = required_hash(compose["x-observability-budget"], "missing_observability_budget")
fail_check("invalid_observability_budget") unless budget == {"cpus" => "8", "memory" => "12GiB", "volumes" => "20GiB"}
retention = required_hash(compose["x-observability-retention"], "missing_observability_retention")
fail_check("invalid_observability_retention") unless retention == {"metrics" => "15d", "logs" => "7d", "traces" => "7d", "langfuse" => "14d"}

# 资源预算：逐服务 limits 求和不得超过本地总预算（plan.md 风险缓解），
# 防止“每服务都合法、加起来压垮开发机”的渐进式超支。
total_cpus = image_variables.keys.sum { |name| cpu_cores(services.fetch(name).dig("deploy", "resources", "limits", "cpus")) }
total_memory = image_variables.keys.sum { |name| memory_bytes(services.fetch(name).dig("deploy", "resources", "limits", "memory")) }
fail_check("resource_budget_exceeded") unless total_cpus <= 8 && total_memory <= 12 * 1024 * 1024 * 1024

# loopback 端口契约（runtime-configuration.md §7）：只有面向开发者的诊断面允许发布，
# 且必须绑定 127.0.0.1；SigNoz 内部 OTLP 接收与 ClickHouse 端口留在 Compose 网络内，
# 避免把未认证的摄取/存储面暴露到宿主网络。
allowed_ports = {
  "signoz" => [3301], "collector" => [4317, 4318, 13133, 8888], "langfuse-web" => [3000]
}
services.each do |name, service|
  is_collector_storage_initializer = name == "collector-storage-init"
  fail_check("unsafe_network_mode:#{name}") if service.key?("network_mode") && !(is_collector_storage_initializer && service["network_mode"] == "none")
  allows_only_initializer_capabilities = is_collector_storage_initializer && Array(service["cap_drop"]) == ["ALL"] && Array(service["cap_add"]).sort == %w[CHOWN DAC_OVERRIDE FOWNER]
  fail_check("unsafe_container_privilege:#{name}") if service["privileged"] == true || %w[host service container].any? { |value| service["pid"] == value || service["ipc"] == value } || service.key?("devices") || service.key?("volumes_from") || (service.key?("cap_add") && !allows_only_initializer_capabilities) || service.key?("security_opt") || service.key?("userns_mode") || service.key?("cgroup") || service.key?("device_cgroup_rules")
  fail_check("short_volume_syntax:#{name}") if Array(service["volumes"]).any? { |mount| !mount.is_a?(Hash) }
  Array(service["ports"]).each do |port|
    is_langfuse_ui_mapping = name == "langfuse-web" && port["target"] == 3000 && port["published"].to_i == 3001
    fail_check("invalid_published_port:#{name}") unless port.is_a?(Hash) && port["host_ip"] == "127.0.0.1" && Array(allowed_ports[name]).include?(port["target"]) && (port["published"].to_i == port["target"] || is_langfuse_ui_mapping)
  end
  fail_check("unexpected_published_port:#{name}") if service.key?("ports") && !allowed_ports.key?(name)
end
allowed_ports.each { |name, ports| fail_check("missing_loopback_port:#{name}") unless Array(services.fetch(name)["ports"]).map { |port| port.is_a?(Hash) ? port["target"] : nil }.sort == ports.sort }

# 卷契约：SigNoz profile 只声明自己需要的状态卷。signoz 本体（UI+query）无持久状态，
# 数据全部落在 signoz-clickhouse；与 Langfuse 的卷严格隔离，两个 profile 的数据
# 互不可见，安全 reset 可以按 compose project 独立清理。
volumes = required_hash(compose["volumes"], "missing_observability_volumes").merge(required_hash(langfuse_compose["volumes"], "missing_langfuse_volumes"))
required_volumes = %w[collector-data signoz-clickhouse-data langfuse-postgres-data langfuse-clickhouse-data langfuse-redis-data langfuse-minio-data]
fail_check("invalid_observability_volumes") unless volumes.keys.sort == required_volumes.sort
fail_check("external_or_overridden_volume") unless volumes.values.all? { |definition| definition == {} }
fail_check("unsupported_compose_secret_or_config") if [compose, langfuse_compose].any? { |document| document.key?("secrets") || document.key?("configs") } || services.values.any? { |service| service.key?("secrets") || service.key?("configs") }

state_volume_mounts = {
  "collector" => ["collector-data", "/var/lib/otelcol/storage"], "collector-storage-init" => ["collector-data", "/var/lib/otelcol/storage"],
  "signoz-clickhouse" => ["signoz-clickhouse-data", "/var/lib/clickhouse"], "langfuse-db" => ["langfuse-postgres-data", "/var/lib/postgresql/data"],
  "langfuse-clickhouse" => ["langfuse-clickhouse-data", "/var/lib/clickhouse"], "langfuse-redis" => ["langfuse-redis-data", "/data"],
  "langfuse-minio" => ["langfuse-minio-data", "/data"]
}
state_volume_mounts.each do |service_name, (source, target)|
  mounts = Array(services.fetch(service_name)["volumes"])
  fail_check("missing_dedicated_volume:#{service_name}") unless has_mount?(mounts, type: "volume", source: source, target: target)
  fail_check("shared_or_unexpected_data_volume:#{service_name}") if mounts.any? { |mount| volume_source?(mount) && (mount["source"] != source || mount["target"] != target) }
end
services.each do |service_name, service|
  expected = state_volume_mounts[service_name]
  actual = Array(service["volumes"]).select { |mount| volume_source?(mount) }.map { |mount| [mount["source"], mount["target"]] }
  expected_mounts = expected ? [expected] : []
  fail_check("unexpected_data_volume_mount:#{service_name}") unless actual == expected_mounts
end

# Collector 边界：应用唯一入口挂载 signoz 专用配置（T137/T142 契约载体），
# 只读 bind，且必须等待 queue 目录初始化完成后才启动。
collector_mounts = Array(services.fetch("collector")["volumes"])
fail_check("missing_collector_config_mount") unless has_mount?(collector_mounts, type: "bind", source: "./collector/collector-signoz.yaml", target: "/etc/otelcol-contrib/config.yaml", read_only: true)
fail_check("missing_collector_storage_initialization_dependency") unless services.fetch("collector").dig("depends_on", "collector-storage-init", "condition") == "service_completed_successfully"
allowed_binds = {
  "collector" => [["./collector/collector-signoz.yaml", "/etc/otelcol-contrib/config.yaml"]]
}
services.each do |name, service|
  Array(service["volumes"]).select { |mount| mount.is_a?(Hash) && mount["type"] == "bind" }.each do |mount|
    fail_check("unexpected_bind_mount:#{name}") unless Array(allowed_binds[name]).include?([mount["source"], mount["target"]]) && mount["read_only"] == true
  end
end

# SigNoz 查询面与摄取面依赖 ClickHouse 健康：冷启动顺序是数据面先行的硬约束。
%w[signoz signoz-otel-collector].each do |service_name|
  fail_check("missing_signoz_clickhouse_dependency:#{service_name}") unless dependency_names(services.fetch(service_name)["depends_on"]).include?("signoz-clickhouse")
  fail_check("unhealthy_signoz_clickhouse_dependency:#{service_name}") unless services.fetch(service_name).dig("depends_on", "signoz-clickhouse", "condition") == "service_healthy"
end
%w[langfuse-db langfuse-clickhouse langfuse-redis].each do |dependency|
  fail_check("missing_langfuse_dependency:#{dependency}") unless dependency_names(services.fetch("langfuse-web")["depends_on"]).include?(dependency) && dependency_names(services.fetch("langfuse-worker")["depends_on"]).include?(dependency)
  fail_check("unhealthy_langfuse_dependency:#{dependency}") unless services.fetch("langfuse-web").dig("depends_on", dependency, "condition") == "service_healthy" && services.fetch("langfuse-worker").dig("depends_on", dependency, "condition") == "service_healthy"
end
%w[langfuse-web langfuse-worker].each do |service_name|
  fail_check("missing_minio_bucket_initialization:#{service_name}") unless services.fetch(service_name).dig("depends_on", "langfuse-minio-init", "condition") == "service_completed_successfully"
  fail_check("missing_clickhouse_identity_initialization:#{service_name}") unless services.fetch(service_name).dig("depends_on", "langfuse-clickhouse-init", "condition") == "service_completed_successfully"
end

# 无业务直连：观测栈不得反向依赖应用进程（宿主 8000），
# 否则应用重启会被误判为观测故障，违反“观测故障与业务故障分别可诊断”（FR-007）。
fail_check("application_endpoint_in_profile") if scalar_strings([compose, langfuse_compose]).any? { |value| value.match?(/host\.docker\.internal|localhost:8000|\bapp:8000\b/) }

# 凭据卫生：profile 中只允许提交变量名（${VAR:?} fail-fast），
# 不得出现字面凭据、带凭据 URI 或 healthcheck 中的 secret。
secret_markers = /authorization|api.?key|secret|password|token/i
[compose, langfuse_compose].each do |document|
  sensitive_values(document).each do |path, value|
    next unless path.match?(secret_markers)
    fail_check("literal_credential_in_profile") unless value.match?(/\A\$\{[A-Z][A-Z0-9_]*(?::\?[^}]*)?\}\z/)
  end
end
fail_check("unsafe_environment_syntax") if services.values.any? { |service| service.key?("environment") && !service["environment"].is_a?(Hash) }
fail_check("credential_bearing_uri_in_profile") if scalar_strings([compose, langfuse_compose]).any? { |value| value.match?(%r{[a-z]+://[^/\s:@]+:[^/\s@]+@}i) }
fail_check("credential_in_healthcheck") if services.values.any? { |service| scalar_strings(service["healthcheck"]).any? { |value| value.match?(secret_markers) } }
fail_check("legacy_observability_log_gid_dependency") if scalar_strings([compose, langfuse_compose]).any? { |value| value.include?("OBSERVABILITY_LOG_GID") }
puts "compose_signoz_test: pass"
' "${REPO_ROOT}" "${COMPOSE_PATH}" "${LANGFUSE_COMPOSE_PATH}" "${VERSIONS_PATH}"
