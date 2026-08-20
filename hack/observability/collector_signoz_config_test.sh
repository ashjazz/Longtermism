#!/usr/bin/env bash
# T137：静态解析 SigNoz 备选 profile 的 Collector 配置；不得启动 Docker 或连接任何后端。
# 这是 US4 的 RED 契约测试：deploy/observability/collector/collector-signoz.yaml 由 T142 实现前，
# 本脚本必须失败。契约核心：应用 OTLP 契约不变、infra 三信号路由到 SigNoz、
# AI marker 分支仍 OTLP/HTTP 到 Langfuse、无业务直连。
set -euo pipefail
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly CONFIG_PATH="${REPO_ROOT}/deploy/observability/collector/collector-signoz.yaml"
if [[ ! -f "${CONFIG_PATH}" ]]; then
  printf '%s: missing_collector_config\n' "${CONFIG_PATH}" >&2
  exit 1
fi
exec ruby -ryaml -e '
path = ARGV.fetch(0)
def fail_check(message)
  warn "#{ARGV.fetch(0)}: #{message}"
  exit 1
end
def section(config, name)
  value = config.fetch(name, {})
  fail_check("invalid_#{name}") unless value.is_a?(Hash)
  value
end
def require_keys(section, keys, category)
  keys.each { |key| fail_check(category) unless section.key?(key) }
end
def pipeline(config, name)
  value = config.dig("service", "pipelines", name)
  fail_check("missing_pipeline:#{name}") unless value.is_a?(Hash)
  %w[receivers exporters].each { |key| fail_check("invalid_pipeline:#{name}") unless value[key].is_a?(Array) && !value[key].empty? }
  value
end
def require_includes(values, required, category)
  required.each { |value| fail_check(category) unless values.include?(value) }
end
def require_exact(values, required, category)
  fail_check(category) unless values.sort == required.sort
end
def scalar_strings(value)
  case value
  when Hash then value.flat_map { |key, nested| [key.to_s] + scalar_strings(nested) }
  when Array then value.flat_map { |nested| scalar_strings(nested) }
  else [value.to_s]
  end
end
def delete_statement?(processor, field)
  expected = /\Adelete_key\((?:attributes|resource\.attributes),["'']#{Regexp.escape(field)}["'']\)\z/
  trace_statements = processor["trace_statements"]
  return false unless trace_statements.is_a?(Array)
  trace_statements
    .flat_map { |statement| statement.is_a?(Hash) ? Array(statement["statements"]) : [] }
    .any? { |statement| statement.to_s.gsub(/\s+/, "").match?(expected) }
end
def attribute_policy?(policy, key, value)
  return false unless policy.is_a?(Hash) && policy["type"] == "string_attribute"
  config = policy["string_attribute"]
  config.is_a?(Hash) && config["key"] == key && Array(config["values"]).map(&:to_s).include?(value)
end
def opaque_attribute_policy?(policy, key)
  return false unless policy.is_a?(Hash) && policy["type"] == "string_attribute"
  config = policy["string_attribute"]
  config.is_a?(Hash) && config["key"] == key && config["enabled_regex_matching"] == true && Array(config["values"]).include?(".+")
end
def designated_ai_policy?(policy)
  return false unless policy.is_a?(Hash) && policy["type"] == "and"
  sub_policies = policy.dig("and", "and_sub_policy")
  sub_policies.is_a?(Array) &&
    sub_policies.any? { |sub_policy| attribute_policy?(sub_policy, "longtermism.observability.plane", "ai") } &&
    sub_policies.any? { |sub_policy| attribute_policy?(sub_policy, "longtermism.ai.designated", "true") }
end
def expected_endpoint?(name, endpoint)
  expected = {
    "otlp/signoz" => "signoz-otel-collector:4317",
    "otlphttp/langfuse" => "http://langfuse-web:3000/api/public/otel"
  }
  endpoint == expected.fetch(name)
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
begin
  config = YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: true) || {}
rescue Psych::Exception
  fail_check("invalid_yaml")
end
fail_check("invalid_root") unless config.is_a?(Hash)
extensions = section(config, "extensions")
receivers = section(config, "receivers")
processors = section(config, "processors")
connectors = section(config, "connectors")
exporters = section(config, "exporters")

# ── 应用 OTLP 契约不变 ─────────────────────────────────────────────────────
# 应用只认识这一个入口：OTLP gRPC/HTTP 的 endpoint 与主线完全一致，
# profile 切换对应用进程零感知（research.md 决策 1/10）。
require_keys(receivers, %w[otlp], "missing_required_receiver")
fail_check("invalid_application_otlp_grpc_endpoint") unless receivers.dig("otlp", "protocols", "grpc", "endpoint") == "0.0.0.0:4317"
fail_check("invalid_application_otlp_http_endpoint") unless receivers.dig("otlp", "protocols", "http", "endpoint") == "0.0.0.0:4318"
# filelog receiver 会绕过 OTLP ingress 的统一脱敏并重新引入宿主 JSONL 依赖（主线既有约束）。
fail_check("legacy_filelog_receiver") if receivers.keys.any? { |name| name == "filelog" || name.start_with?("filelog/") }
fail_check("legacy_application_log_path") if scalar_strings(receivers).any? { |value| value.include?("/var/log/longtermism") || value.include?("application.jsonl") }

# ── 组件清单：SigNoz 路由 + Langfuse AI 分支 ─────────────────────────────
# otlp/signoz 服务 infra 三信号（traces/metrics/logs 共用一个 exporter 与持久队列命名空间，
# 与 T136 storage-init 的 queue/signoz 目录契约一一对应）；prometheus/app pull exporter
# 在无 Prometheus 的 profile 中是无消费者的死端，必须删除，metrics 改为 OTLP push。
require_keys(connectors, %w[forward/infra forward/ai], "missing_required_connector")
require_keys(exporters, %w[otlp/signoz otlphttp/langfuse], "missing_stable_component")
require_keys(extensions, %w[health_check file_storage/signoz file_storage/langfuse], "missing_persistent_queue_storage")
require_keys(processors, %w[filter/ai filter/http-completion-logs transform/redact-ingress transform/redact-downstream transform/redact-span-events transform/redact-logs tail_sampling/retain], "missing_required_processor")
fail_check("mainline_backend_exporter_residual") if exporters.keys.any? { |name| name == "otlp/tempo" || name == "otlphttp/loki" || name == "prometheus/app" }
# 半迁移防护：主线后端 endpoint 不得残留在任何配置值里。
fail_check("mainline_backend_endpoint_residual") if scalar_strings(config).any? { |value| value.match?(%r{\b(?:tempo|loki|prometheus)(?::\d+|/otlp\b)}i) && !value.include?("signoz") }

service_extensions = config.dig("service", "extensions")
fail_check("missing_enabled_persistent_queue_storage") unless service_extensions.is_a?(Array)
require_includes(service_extensions, %w[health_check file_storage/signoz file_storage/langfuse], "missing_enabled_persistent_queue_storage")
# distroless sidecar 健康探测依赖显式绑定（T136 的 collector-health-probe 契约）。
fail_check("invalid_collector_health_endpoint") unless extensions.dig("health_check", "endpoint") == "0.0.0.0:13133" && extensions.dig("health_check", "path") == "/healthz"
# Collector 自身指标端点保持不变：诊断面不因 profile 切换漂移。
telemetry_metrics = config.dig("service", "telemetry", "metrics")
fail_check("missing_collector_self_telemetry") unless telemetry_metrics.is_a?(Hash)
readers = telemetry_metrics["readers"]
fail_check("missing_collector_self_telemetry") unless readers.is_a?(Array)
self_telemetry_reader = readers.find do |reader|
  reader.is_a?(Hash) && reader.dig("pull", "exporter", "prometheus", "host") == "0.0.0.0" && reader.dig("pull", "exporter", "prometheus", "port") == 8888
end
fail_check("invalid_collector_self_telemetry_endpoint") unless self_telemetry_reader
{
  "file_storage/signoz" => ["/var/lib/otelcol/storage/queue/signoz", "/var/lib/otelcol/storage/queue/signoz-compaction"],
  "file_storage/langfuse" => ["/var/lib/otelcol/storage/queue/langfuse", "/var/lib/otelcol/storage/queue/langfuse-compaction"]
}.each do |name, (expected_directory, expected_compaction_directory)|
  storage = extensions.fetch(name)
  directory = storage.fetch("directory", "")
  # 队列持久化必须落在 collector-data 卷内（T136 storage-init 已预创建并 chown 10001），
  # 否则非 root Collector 会在只读根文件系统上创建目录失败。
  fail_check("invalid_persistent_queue_storage:#{name}") unless directory == expected_directory
  fail_check("missing_persistent_queue_directory_creation:#{name}") unless storage["create_directory"] == true
  fail_check("invalid_persistent_queue_compaction_storage:#{name}") unless storage.dig("compaction", "directory") == expected_compaction_directory
end
extensions.each_key do |name|
  fail_check("unsupported_auth_extension") if name.match?(/auth/i)
end

# ── Pipeline 拓扑：三信号进 SigNoz、AI marker 分支进 Langfuse ─────────────
ingress = pipeline(config, "traces/ingress")
infra = pipeline(config, "traces/infra")
ai = pipeline(config, "traces/ai")
metrics = pipeline(config, "metrics")
logs = pipeline(config, "logs")
require_exact(ingress.fetch("receivers"), ["otlp"], "invalid_ingress_receivers")
require_exact(ingress.fetch("exporters"), ["forward/infra", "forward/ai"], "invalid_ingress_fanout")
require_includes(ingress.fetch("processors", []), ["transform/redact-ingress"], "missing_ingress_redaction")
require_exact(infra.fetch("receivers"), ["forward/infra"], "invalid_infra_receivers")
require_exact(infra.fetch("exporters"), ["otlp/signoz"], "invalid_infra_exporters")
require_includes(infra.fetch("processors", []), ["transform/redact-downstream", "tail_sampling/retain"], "missing_infra_privacy_or_sampling")
require_exact(ai.fetch("receivers"), ["forward/ai"], "invalid_ai_receivers")
require_includes(ai.fetch("processors", []), ["filter/ai", "transform/redact-downstream", "tail_sampling/retain"], "missing_ai_filter_privacy_or_sampling")
require_exact(ai.fetch("exporters"), ["otlphttp/langfuse"], "invalid_ai_exporters")
# metrics 从主线 prometheus/app（pull，依赖 Prometheus 抓取）改为 OTLP push 到 SigNoz：
# 这是后端边界变化，应用仍向同一 receiver push，应用契约不变。
require_exact(metrics.fetch("receivers"), ["otlp"], "invalid_metrics_receivers")
require_exact(metrics.fetch("exporters"), ["otlp/signoz"], "invalid_metrics_exporters")
require_exact(logs.fetch("receivers"), ["otlp"], "invalid_logs_receivers")
require_exact(logs.fetch("exporters"), ["otlp/signoz"], "invalid_logs_exporters")
require_includes(logs.fetch("processors", []), ["filter/http-completion-logs", "transform/redact-logs"], "missing_logs_identity_or_privacy_processor")
fail_check("legacy_logs_resource_inference") if logs.fetch("processors", []).include?("resource/http-completion-log-service")

# ── 隐私与采样契约：与主线逐字一致（应用/事实边界不因 profile 改变）───────
log_statements = Array(processors.dig("transform/redact-logs", "log_statements")).flat_map do |group|
  group.is_a?(Hash) ? Array(group["statements"]).map { |statement| statement.to_s.gsub(/\s+/, "") } : []
end
fail_check("unsafe_logs_redaction_error_mode") unless processors.dig("transform/redact-logs", "error_mode") == "propagate"
expected_log_attributes = %w[request_id trace_id span_id route method status duration_ms error_class ai_trace_id smoke_run_id]
keep_statement = log_statements.find { |statement| statement.start_with?("keep_keys(") }
keep_match = /\Akeep_keys\((?:log\.)?attributes,\[(.*)\]\)\z/.match(keep_statement.to_s)
fail_check("missing_logs_attribute_allowlist") unless keep_match
kept_log_attributes = keep_match[1].scan(/["'']([^"'']+)["'']/).flatten
fail_check("invalid_logs_attribute_allowlist") unless kept_log_attributes.sort == expected_log_attributes.sort
resource_statements = Array(processors.dig("transform/redact-logs", "log_statements")).flat_map do |group|
  group.is_a?(Hash) && group["context"] == "resource" ? Array(group["statements"]).map { |statement| statement.to_s.gsub(/\s+/, "") } : []
end
resource_keep = resource_statements.find { |statement| statement.start_with?("keep_keys(") }
resource_match = /\Akeep_keys\((?:resource\.)?attributes,\[(.*)\]\)\z/.match(resource_keep.to_s)
fail_check("missing_logs_resource_allowlist") unless resource_match
expected_log_resource_attributes = %w[service.name service.version deployment.environment]
kept_log_resource_attributes = resource_match[1].scan(/["'']([^"'']+)["'']/).flatten
fail_check("invalid_logs_resource_allowlist") unless kept_log_resource_attributes.sort == expected_log_resource_attributes.sort
%w[authorization cookie api_key token password prompt query output tool_arguments provider_error_body].each do |forbidden|
  %w[transform/redact-ingress transform/redact-downstream].each do |processor|
    fail_check("missing_second_field_removal:#{processor}:#{forbidden}") unless delete_statement?(processors.fetch(processor), forbidden)
  end
end
ai_filter = processors.fetch("filter/ai").dig("traces", "span")
fail_check("invalid_ai_filter") unless ai_filter.is_a?(Array)
ai_filter = ai_filter.map { |statement| statement.to_s.gsub(/\s+/, "") }
expected_ai_drop = /attributes\[["'']longtermism\.observability\.plane["'']\]!=["'']ai["'']/
fail_check("invalid_ai_filter") unless ai_filter.any? { |statement| statement.match?(expected_ai_drop) }
sampling = processors.fetch("tail_sampling/retain")
policies = sampling["policies"]
fail_check("invalid_tail_sampling") unless sampling["decision_wait"].is_a?(String) && sampling["decision_wait"].match?(/\A[1-9][0-9]{0,2}s\z/) && policies.is_a?(Array) && !policies.empty?
fail_check("missing_tail_sampling_retention:smoke") unless policies.any? { |policy| opaque_attribute_policy?(policy, "longtermism.smoke.run_id") }
fail_check("missing_tail_sampling_retention:error") unless policies.any? { |policy| policy.is_a?(Hash) && policy["type"] == "status_code" && Array(policy.dig("status_code", "status_codes")).include?("ERROR") }
fail_check("missing_tail_sampling_retention:degradation") unless policies.any? { |policy| attribute_policy?(policy, "longtermism.degraded", "true") }
fail_check("missing_tail_sampling_retention:regression") unless policies.any? { |policy| attribute_policy?(policy, "longtermism.eval.regression", "true") }
fail_check("missing_tail_sampling_retention:designated") unless policies.any? { |policy| designated_ai_policy?(policy) }
fail_check("missing_tail_sampling_ordinary_ratio") unless policies.any? { |policy| policy.is_a?(Hash) && policy["type"] == "probabilistic" }

# ── Exporter 韧性与凭据卫生 ────────────────────────────────────────────────
%w[otlp/signoz otlphttp/langfuse].each do |name|
  exporter = exporters.fetch(name)
  queue = exporter.fetch("sending_queue", {})
  storage = "file_storage/#{name.split("/").last}"
  fail_check("missing_persistent_queue:#{name}") unless queue["enabled"] == true && queue["storage"] == storage
  fail_check("unbounded_persistent_queue:#{name}") unless queue["queue_size"].is_a?(Integer) && queue["queue_size"].positive? && queue["queue_size"] <= 100_000
  fail_check("missing_retry:#{name}") unless exporter.dig("retry_on_failure", "enabled") == true
  fail_check("missing_timeout:#{name}") unless exporter["timeout"].is_a?(String) && exporter["timeout"].match?(/\A(?:[1-9][0-9]{0,2}ms|[1-9][0-9]{0,2}s|[1-5][0-9]m)\z/)
  fail_check("untrusted_exporter_endpoint:#{name}") unless exporter["endpoint"].is_a?(String) && expected_endpoint?(name, exporter["endpoint"])
  fail_check("unsupported_exporter_auth:#{name}") if exporter.key?("auth")
  fail_check("unsupported_exporter_proxy:#{name}") if exporter.key?("proxy_url")
end
# Langfuse OTLP 入口只接受 HTTP 传输；显式固定 protobuf 编码，防止 Collector
# 版本升级时默认编码变化造成静默投递失败（research.md Langfuse 协议误配风险）。
fail_check("invalid_langfuse_encoding") unless exporters.dig("otlphttp/langfuse", "encoding") == "proto"
langfuse_headers = exporters.dig("otlphttp/langfuse", "headers")
fail_check("missing_langfuse_ingestion_headers") unless langfuse_headers.is_a?(Hash) &&
  langfuse_headers["Authorization"] == "${env:LANGFUSE_OTLP_AUTHORIZATION}" &&
  langfuse_headers["x-langfuse-ingestion-version"] == "${env:LANGFUSE_OTEL_INGESTION_VERSION}"

# 无业务直连：观测边界不得反向依赖应用进程，否则应用重启会被误判为观测故障（FR-007）。
fail_check("application_endpoint_in_collector") if scalar_strings(config).any? { |value| value.match?(/host\.docker\.internal|localhost:8000|\bapp:8000\b/) }
sensitive_values(exporters).each do |key, value|
  fail_check("unsafe_exporter_tls") if key.match?(/insecure_skip_verify/i) && value == "true"
  next unless key.match?(/authorization|api.?key|token|password|secret|header|credential|username|user|private_key/i)
  fail_check("literal_exporter_credential") unless value.match?(%r{\A(?:Bearer )?\$\{env:[A-Z][A-Z0-9_]*\}\z})
end
puts "collector_signoz_config_test: pass"
' "${CONFIG_PATH}"
