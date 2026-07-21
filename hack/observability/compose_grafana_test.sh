#!/usr/bin/env bash
# T044：静态验证 Grafana profile wiring；只允许 make -n，绝不启动 Docker 或访问后端。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly COMPOSE_PATH="${REPO_ROOT}/deploy/observability/compose.grafana.yaml"
readonly LANGFUSE_COMPOSE_PATH="${REPO_ROOT}/deploy/observability/compose.langfuse.yaml"
readonly VERSIONS_PATH="${REPO_ROOT}/deploy/observability/versions.env"
readonly TEMPO_PATH="${REPO_ROOT}/deploy/observability/tempo/tempo.yaml"
readonly PROMETHEUS_PATH="${REPO_ROOT}/deploy/observability/prometheus/prometheus.yaml"
readonly DATASOURCES_PATH="${REPO_ROOT}/deploy/observability/grafana/provisioning/datasources.yaml"

[[ -f "${COMPOSE_PATH}" ]] || { printf '%s: missing_grafana_compose\n' "${COMPOSE_PATH}" >&2; exit 1; }
[[ -f "${LANGFUSE_COMPOSE_PATH}" ]] || { printf '%s: missing_langfuse_compose\n' "${LANGFUSE_COMPOSE_PATH}" >&2; exit 1; }
[[ -f "${TEMPO_PATH}" ]] || { printf '%s: missing_tempo_config\n' "${TEMPO_PATH}" >&2; exit 1; }
[[ -f "${PROMETHEUS_PATH}" ]] || { printf '%s: missing_prometheus_config\n' "${PROMETHEUS_PATH}" >&2; exit 1; }
[[ -f "${DATASOURCES_PATH}" ]] || { printf '%s: missing_grafana_datasources\n' "${DATASOURCES_PATH}" >&2; exit 1; }

exec ruby -ropen3 -ryaml -e '
repo_root, compose_path, langfuse_compose_path, versions_path, tempo_path, prometheus_path, datasources_path = ARGV.freeze

def fail_check(message)
  warn "#{ARGV.fetch(1)}: #{message}"
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
rescue ArgumentError
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

def target_recipe(path, target)
  lines = File.readlines(path, chomp: true)
  start = lines.index { |line| line.match?(/^#{Regexp.escape(target)}\s*:/) }
  fail_check("missing_make_target:#{target}") unless start
  lines[(start + 1)..].take_while { |line| line.start_with?("\t") || line.empty? }.select { |line| line.start_with?("\t") }.map(&:strip)
end

def has_mount?(mounts, type:, source:, target:, read_only: nil)
  mounts.any? do |mount|
    mount.is_a?(Hash) && mount["type"] == type && mount["source"] == source && mount["target"] == target && (read_only.nil? || mount["read_only"] == read_only)
  end
end

def trusted_health_probe?(service, test)
  return false unless test.is_a?(Array) && test.length == 2 && test.first == "CMD-SHELL" && test.last.is_a?(String)
  command = test.last.strip
  allowed = {
    "collector" => ["curl --fail --silent --show-error http://127.0.0.1:13133/healthz"],
    "prometheus" => ["curl --fail --silent --show-error http://127.0.0.1:9090/-/ready"],
    "loki" => ["curl --fail --silent --show-error http://127.0.0.1:3100/ready"],
    "tempo" => ["curl --fail --silent --show-error http://127.0.0.1:3200/ready"],
    "grafana" => ["curl --fail --silent --show-error http://127.0.0.1:3000/api/health"],
    "langfuse-web" => ["curl --fail --silent --show-error http://127.0.0.1:3000/api/health"],
    "langfuse-worker" => ["node /app/worker-healthcheck.js"],
    "langfuse-db" => ["pg_isready -U langfuse -d langfuse"],
    "langfuse-clickhouse" => ["clickhouse-client --query \"SELECT 1\""],
    "langfuse-redis" => ["redis-cli ping"]
  }
  allowed.fetch(service).include?(command)
end

compose = required_hash(yaml(compose_path), "invalid_compose")
langfuse_compose = required_hash(yaml(langfuse_compose_path), "invalid_langfuse_compose")
versions = parse_versions(versions_path)
grafana_services = required_hash(compose["services"], "missing_compose_services")
langfuse_services = required_hash(langfuse_compose["services"], "missing_langfuse_compose_services")
services = grafana_services.merge(langfuse_services)
fail_check("duplicate_split_service") unless services.length == grafana_services.length + langfuse_services.length
fail_check("custom_compose_network_forbidden") if [compose, langfuse_compose].any? { |document| document.key?("networks") } || services.values.any? { |service| service.key?("networks") }
required_services = %w[collector prometheus loki tempo grafana langfuse-web langfuse-worker langfuse-db langfuse-clickhouse langfuse-redis]
fail_check("invalid_grafana_profile_services") unless services.keys.sort == required_services.sort
fail_check("invalid_bootstrap_services") unless langfuse_services.keys.sort == %w[langfuse-clickhouse langfuse-db langfuse-redis langfuse-web langfuse-worker]
fail_check("collector_leaked_into_bootstrap") if scalar_strings(langfuse_compose).any? { |value| value.include?("LANGFUSE_OTLP_AUTHORIZATION") || value.include?("LANGFUSE_OTEL_INGESTION_VERSION") }
collector_environment = required_hash(grafana_services.fetch("collector")["environment"], "missing_collector_langfuse_configuration")
{
  "LANGFUSE_OTLP_AUTHORIZATION" => "LANGFUSE_OTLP_AUTHORIZATION",
  "LANGFUSE_OTEL_INGESTION_VERSION" => "LANGFUSE_OTEL_INGESTION_VERSION"
}.each do |key, variable|
  value = collector_environment[key]
  fail_check("missing_collector_langfuse_configuration") unless value.is_a?(String) && value.match?(/\A\$\{#{Regexp.escape(variable)}:\?[^}]+\}\z/)
end
%w[langfuse-web langfuse-worker].each do |service_name|
  environment = required_hash(langfuse_services.fetch(service_name)["environment"], "missing_langfuse_encryption_key")
  value = environment["ENCRYPTION_KEY"]
  fail_check("missing_langfuse_encryption_key") unless value.is_a?(String) && value.match?(/\A\$\{LANGFUSE_ENCRYPTION_KEY:\?[^}]+\}\z/)
end
budget = required_hash(compose["x-observability-budget"], "missing_observability_budget")
fail_check("invalid_observability_budget") unless budget == {"cpus" => "8", "memory" => "12GiB", "volumes" => "20GiB"}

image_variables = {
  "collector" => "OTELCOL_CONTRIB_IMAGE", "prometheus" => "PROMETHEUS_IMAGE", "loki" => "LOKI_IMAGE", "tempo" => "TEMPO_IMAGE", "grafana" => "GRAFANA_IMAGE",
  "langfuse-web" => "LANGFUSE_IMAGE", "langfuse-worker" => "LANGFUSE_WORKER_IMAGE", "langfuse-db" => "LANGFUSE_POSTGRES_IMAGE", "langfuse-clickhouse" => "LANGFUSE_CLICKHOUSE_IMAGE", "langfuse-redis" => "LANGFUSE_REDIS_IMAGE"
}
image_variables.each do |service_name, variable|
  service = required_hash(services[service_name], "invalid_service:#{service_name}")
  fail_check("invalid_fixed_image:#{service_name}") unless service["image"] == "${#{variable}}" && versions.fetch(variable, "").include?(":") && !versions.fetch(variable, "").include?(":latest")
  limits = service.dig("deploy", "resources", "limits")
  fail_check("missing_resource_limit:#{service_name}") unless limits.is_a?(Hash) && limits.key?("cpus") && limits.key?("memory")
  healthcheck = service["healthcheck"]
  fail_check("missing_healthcheck:#{service_name}") unless healthcheck.is_a?(Hash) && healthcheck["test"].is_a?(Array) && healthcheck["test"].length > 1 && healthcheck["interval"].to_s.match?(/\A[1-9][0-9]*s\z/) && healthcheck["timeout"].to_s.match?(/\A[1-9][0-9]*s\z/) && healthcheck["retries"].is_a?(Integer) && healthcheck["retries"].positive?
  fail_check("unsafe_healthcheck:#{service_name}") if scalar_strings(healthcheck["test"]).any? { |value| value.match?(/\b(?:true|echo|exit 0)\b/i) }
end

image_variables.keys.each { |service_name| fail_check("untrusted_healthcheck:#{service_name}") unless trusted_health_probe?(service_name, services.fetch(service_name).dig("healthcheck", "test")) }

# Grafana UI 即使只绑定 loopback，也不能依赖公开已知的默认 admin/admin。
# 这两个值必须由运行环境提供，profile 中只保留变量名与 fail-fast 语义。
grafana_environment = required_hash(services.fetch("grafana")["environment"], "missing_grafana_admin_configuration")
{
  "GF_SECURITY_ADMIN_USER" => "GRAFANA_ADMIN_USER",
  "GF_SECURITY_ADMIN_PASSWORD" => "GRAFANA_ADMIN_PASSWORD"
}.each do |key, variable|
  value = grafana_environment[key]
  fail_check("missing_grafana_admin_configuration") unless value.is_a?(String) && value.match?(/\A\$\{#{Regexp.escape(variable)}:\?[^}]+\}\z/)
end

total_cpus = image_variables.keys.sum { |name| cpu_cores(services.fetch(name).dig("deploy", "resources", "limits", "cpus")) }
total_memory = image_variables.keys.sum { |name| memory_bytes(services.fetch(name).dig("deploy", "resources", "limits", "memory")) }
fail_check("resource_budget_exceeded") unless total_cpus <= 8 && total_memory <= 12 * 1024 * 1024 * 1024

allowed_ports = {
  "grafana" => [3000], "langfuse-web" => [3000], "prometheus" => [9090], "loki" => [3100],
  "tempo" => [3200], "collector" => [4317, 4318, 13133, 8888]
}
services.each do |name, service|
  fail_check("unsafe_network_mode:#{name}") if service.key?("network_mode")
  fail_check("unsafe_container_privilege:#{name}") if service["privileged"] == true || %w[host service container].any? { |value| service["pid"] == value || service["ipc"] == value } || service.key?("devices") || service.key?("volumes_from") || service.key?("cap_add") || service.key?("security_opt") || service.key?("userns_mode") || service.key?("cgroup") || service.key?("device_cgroup_rules")
  fail_check("short_volume_syntax:#{name}") if Array(service["volumes"]).any? { |mount| !mount.is_a?(Hash) }
  Array(service["ports"]).each do |port|
    is_langfuse_ui_mapping = name == "langfuse-web" && port["target"] == 3000 && port["published"].to_i == 3001
    fail_check("invalid_published_port:#{name}") unless port.is_a?(Hash) && port["host_ip"] == "127.0.0.1" && Array(allowed_ports[name]).include?(port["target"]) && (port["published"].to_i == port["target"] || is_langfuse_ui_mapping)
  end
  fail_check("unexpected_published_port:#{name}") if service.key?("ports") && !allowed_ports.key?(name)
end
allowed_ports.each { |name, ports| fail_check("missing_loopback_port:#{name}") unless Array(services.fetch(name)["ports"]).map { |port| port.is_a?(Hash) ? port["target"] : nil }.sort == ports.sort }

volumes = required_hash(compose["volumes"], "missing_observability_volumes").merge(required_hash(langfuse_compose["volumes"], "missing_langfuse_volumes"))
required_volumes = %w[collector-data tempo-data loki-data prometheus-data grafana-data langfuse-postgres-data langfuse-clickhouse-data langfuse-redis-data]
fail_check("invalid_observability_volumes") unless volumes.keys.sort == required_volumes.sort
fail_check("external_or_overridden_volume") unless volumes.values.all? { |definition| definition == {} }
fail_check("unsupported_compose_secret_or_config") if [compose, langfuse_compose].any? { |document| document.key?("secrets") || document.key?("configs") } || services.values.any? { |service| service.key?("secrets") || service.key?("configs") }

state_volume_mounts = {
  "collector" => ["collector-data", "/var/lib/otelcol/storage"], "tempo" => ["tempo-data", "/var/tempo"], "loki" => ["loki-data", "/loki"],
  "prometheus" => ["prometheus-data", "/prometheus"], "grafana" => ["grafana-data", "/var/lib/grafana"], "langfuse-db" => ["langfuse-postgres-data", "/var/lib/postgresql/data"],
  "langfuse-clickhouse" => ["langfuse-clickhouse-data", "/var/lib/clickhouse"], "langfuse-redis" => ["langfuse-redis-data", "/data"]
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

collector_mounts = Array(services.fetch("collector")["volumes"])
fail_check("missing_collector_config_mount") unless has_mount?(collector_mounts, type: "bind", source: "./collector/collector-grafana.yaml", target: "/etc/otelcol-contrib/config.yaml", read_only: true)
# 宿主机运行应用时，JSONL 落在被忽略的项目运行目录；Collector 只读同一个 bind mount，
# 因而仍由 filelog 异步送往 Loki，应用不直接连接后端。
fail_check("missing_local_application_log_mount") unless has_mount?(collector_mounts, type: "bind", source: "../../resource/log/observability", target: "/var/log/longtermism", read_only: true)
tempo_mounts = Array(services.fetch("tempo")["volumes"])
prometheus_mounts = Array(services.fetch("prometheus")["volumes"])
loki_mounts = Array(services.fetch("loki")["volumes"])
grafana_mounts = Array(services.fetch("grafana")["volumes"])
fail_check("missing_tempo_config_mount") unless has_mount?(tempo_mounts, type: "bind", source: "./tempo/tempo.yaml", target: "/etc/tempo/tempo.yaml", read_only: true)
fail_check("missing_prometheus_config_mount") unless has_mount?(prometheus_mounts, type: "bind", source: "./prometheus/prometheus.yaml", target: "/etc/prometheus/prometheus.yml", read_only: true)
fail_check("missing_loki_config_mount") unless has_mount?(loki_mounts, type: "bind", source: "./loki/loki.yaml", target: "/etc/loki/loki.yaml", read_only: true)
fail_check("missing_datasource_mount") unless has_mount?(grafana_mounts, type: "bind", source: "./grafana/provisioning/datasources.yaml", target: "/etc/grafana/provisioning/datasources/datasources.yaml", read_only: true)
fail_check("missing_dashboard_provider_mount") unless has_mount?(grafana_mounts, type: "bind", source: "./grafana/provisioning/dashboards.yaml", target: "/etc/grafana/provisioning/dashboards/dashboards.yaml", read_only: true)
fail_check("missing_dashboard_mount") unless has_mount?(grafana_mounts, type: "bind", source: "./grafana/dashboards/observability-overview.json", target: "/var/lib/grafana/dashboards/observability-overview.json", read_only: true)
fail_check("missing_alert_mount") unless has_mount?(grafana_mounts, type: "bind", source: "./grafana/alerts/observability.rules.yaml", target: "/etc/grafana/provisioning/alerting/observability.rules.yaml", read_only: true)
allowed_binds = {
  "collector" => [["./collector/collector-grafana.yaml", "/etc/otelcol-contrib/config.yaml"], ["../../resource/log/observability", "/var/log/longtermism"]], "tempo" => [["./tempo/tempo.yaml", "/etc/tempo/tempo.yaml"]],
  "prometheus" => [["./prometheus/prometheus.yaml", "/etc/prometheus/prometheus.yml"]], "loki" => [["./loki/loki.yaml", "/etc/loki/loki.yaml"]],
  "grafana" => [["./grafana/provisioning/datasources.yaml", "/etc/grafana/provisioning/datasources/datasources.yaml"], ["./grafana/provisioning/dashboards.yaml", "/etc/grafana/provisioning/dashboards/dashboards.yaml"], ["./grafana/dashboards/observability-overview.json", "/var/lib/grafana/dashboards/observability-overview.json"], ["./grafana/alerts/observability.rules.yaml", "/etc/grafana/provisioning/alerting/observability.rules.yaml"]]
}
services.each do |name, service|
  Array(service["volumes"]).select { |mount| mount.is_a?(Hash) && mount["type"] == "bind" }.each do |mount|
    fail_check("unexpected_bind_mount:#{name}") unless Array(allowed_binds[name]).include?([mount["source"], mount["target"]]) && mount["read_only"] == true
  end
end

%w[langfuse-db langfuse-clickhouse langfuse-redis].each do |dependency|
  fail_check("missing_langfuse_dependency:#{dependency}") unless dependency_names(services.fetch("langfuse-web")["depends_on"]).include?(dependency) && dependency_names(services.fetch("langfuse-worker")["depends_on"]).include?(dependency)
  fail_check("unhealthy_langfuse_dependency:#{dependency}") unless services.fetch("langfuse-web").dig("depends_on", dependency, "condition") == "service_healthy" && services.fetch("langfuse-worker").dig("depends_on", dependency, "condition") == "service_healthy"
end

tempo = required_hash(yaml(tempo_path), "invalid_tempo_config")
prometheus = required_hash(yaml(prometheus_path), "invalid_prometheus_config")
fail_check("invalid_tempo_retention") unless tempo.dig("compactor", "compaction", "block_retention") == "168h"
fail_check("invalid_prometheus_retention") unless Array(services.fetch("prometheus")["command"]).include?("--storage.tsdb.retention.time=15d") && !prometheus.key?("remote_write")
datasources = required_hash(yaml(datasources_path), "invalid_datasources_config")
expected_datasources = {"prometheus" => "http://prometheus:9090", "loki" => "http://loki:3100", "tempo" => "http://tempo:3200"}
actual_datasources = Array(datasources["datasources"]).each_with_object({}) do |entry, values|
  values[entry["uid"]] = entry["url"] if entry.is_a?(Hash)
end
fail_check("invalid_datasource_uid") unless actual_datasources == expected_datasources

makefile = File.join(repo_root, "Makefile")
makefile_text = File.read(makefile)
up_recipe = target_recipe(makefile, "obs-grafana-up").join("\n")
down_recipe = target_recipe(makefile, "obs-grafana-down").join("\n")
health_recipe = target_recipe(makefile, "obs-stack-health").join("\n")
infra_recipe = target_recipe(makefile, "obs-infra-smoke").join("\n")
e2e_recipe = target_recipe(makefile, "obs-grafana-e2e").join("\n")
bootstrap_recipe = target_recipe(makefile, "obs-langfuse-bootstrap-up").join("\n")
langfuse_compose_definition = "OBS_LANGFUSE_COMPOSE = docker compose --project-name $(OBSERVABILITY_COMPOSE_PROJECT) --env-file deploy/observability/versions.env$(if $(OBSERVABILITY_LOCAL_ENV_OPTION), $(OBSERVABILITY_LOCAL_ENV_OPTION)) -f deploy/observability/compose.langfuse.yaml"
fail_check("invalid_langfuse_compose_definition") unless makefile_text.include?(langfuse_compose_definition)
fail_check("invalid_grafana_compose_definition") unless makefile_text.include?("OBS_GRAFANA_COMPOSE = OBSERVABILITY_LOG_GID=\"$$(id -g)\" $(OBS_LANGFUSE_COMPOSE) -f deploy/observability/compose.grafana.yaml")
fail_check("invalid_grafana_up_target") unless up_recipe.include?("$(OBS_GRAFANA_COMPOSE) up -d")
fail_check("invalid_langfuse_bootstrap_target") unless bootstrap_recipe.include?("$(OBS_LANGFUSE_COMPOSE) up -d --wait --wait-timeout 180 langfuse-web langfuse-worker") && !bootstrap_recipe.include?("compose.grafana.yaml") && !bootstrap_recipe.match?(/LANGFUSE_OTLP_AUTHORIZATION|LANGFUSE_OTEL_INGESTION_VERSION/)
fail_check("missing_local_log_symlink_guard") unless up_recipe.include?("-L") && up_recipe.include?("resource/log/observability")
collector_user = services.fetch("collector")["user"]
fail_check("missing_local_log_group") unless collector_user == "10001:${OBSERVABILITY_LOG_GID:?set OBSERVABILITY_LOG_GID}"
fail_check("invalid_grafana_down_target") unless down_recipe.include?("$(OBS_GRAFANA_COMPOSE) down") && !down_recipe.match?(/(?:^|\s)-v(?:\s|$)/)
fail_check("invalid_stack_health_target") unless health_recipe.include?("$(OBS_GRAFANA_COMPOSE) ps")
fail_check("invalid_infra_smoke_target") unless infra_recipe.include?("obs-smoke") && infra_recipe.include?("infra")
fail_check("invalid_grafana_e2e_target") unless e2e_recipe.include?("obs-infra-smoke") && !e2e_recipe.match?(/\A(?:.*\bps\b.*)\z/m)

secret_markers = /authorization|api.?key|secret|password|token/i
[compose, langfuse_compose, datasources].each do |document|
  sensitive_values(document).each do |path, value|
    next unless path.match?(secret_markers)
    # Compose 的 `:?` 形式让缺少凭据在容器创建前失败；仍只允许提交变量名，
    # 不能把 credential value 或带凭据的 URI 写进 profile。
    fail_check("literal_credential_in_profile") unless value.match?(/\A\$\{[A-Z][A-Z0-9_]*(?::\?[^}]*)?\}\z/)
  end
end
fail_check("unsafe_environment_syntax") if services.values.any? { |service| service.key?("environment") && !service["environment"].is_a?(Hash) }
fail_check("credential_bearing_uri_in_profile") if scalar_strings([compose, langfuse_compose, datasources]).any? { |value| value.match?(%r{[a-z]+://[^/\s:@]+:[^/\s@]+@}i) }
fail_check("credential_in_healthcheck") if services.values.any? { |service| scalar_strings(service["healthcheck"]).any? { |value| value.match?(secret_markers) } }
puts "compose_grafana_test: pass"
' "${REPO_ROOT}" "${COMPOSE_PATH}" "${LANGFUSE_COMPOSE_PATH}" "${VERSIONS_PATH}" "${TEMPO_PATH}" "${PROMETHEUS_PATH}" "${DATASOURCES_PATH}"
