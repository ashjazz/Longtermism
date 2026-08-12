#!/usr/bin/env bash
# T042：静态解析 Collector 配置；不得启动 Docker 或连接任何后端。
set -euo pipefail
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly CONFIG_PATH="${REPO_ROOT}/deploy/observability/collector/collector-grafana.yaml"
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
    "otlp/tempo" => "tempo:4317",
    "otlphttp/loki" => "http://loki:3100/otlp",
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
require_keys(receivers, %w[otlp], "missing_required_receiver")
# Application logs now share the OTLP ingress and provider lifecycle with traces/metrics.
# Keeping filelog around would preserve an untested bypass and the host JSONL dependency.
fail_check("legacy_filelog_receiver") if receivers.keys.any? { |name| name == "filelog" || name.start_with?("filelog/") }
fail_check("legacy_application_log_path") if scalar_strings(receivers).any? { |value| value.include?("/var/log/longtermism") || value.include?("application.jsonl") }
require_keys(connectors, %w[forward/infra forward/ai], "missing_required_connector")
require_keys(exporters, %w[otlp/tempo otlphttp/loki otlphttp/langfuse prometheus/app], "missing_stable_component")
require_keys(extensions, %w[health_check file_storage/tempo file_storage/loki file_storage/langfuse], "missing_persistent_queue_storage")
require_keys(processors, %w[filter/ai filter/http-completion-logs transform/redact-ingress transform/redact-downstream transform/redact-logs tail_sampling/retain], "missing_required_processor")
service_extensions = config.dig("service", "extensions")
fail_check("missing_enabled_persistent_queue_storage") unless service_extensions.is_a?(Array)
require_includes(service_extensions, %w[health_check file_storage/tempo file_storage/loki file_storage/langfuse], "missing_enabled_persistent_queue_storage")
fail_check("invalid_collector_health_endpoint") unless extensions.dig("health_check", "endpoint") == "0.0.0.0:13133" && extensions.dig("health_check", "path") == "/healthz"
# Prometheus runs in a peer container, so Collector self-telemetry must not use
# the default loopback-only listener. This endpoint is separate from the
# application metric exporter on 8889.
telemetry_metrics = config.dig("service", "telemetry", "metrics")
fail_check("missing_collector_self_telemetry") unless telemetry_metrics.is_a?(Hash)
readers = telemetry_metrics["readers"]
fail_check("missing_collector_self_telemetry") unless readers.is_a?(Array)
self_telemetry_reader = readers.find do |reader|
  reader.is_a?(Hash) && reader.dig("pull", "exporter", "prometheus", "host") == "0.0.0.0" && reader.dig("pull", "exporter", "prometheus", "port") == 8888
end
fail_check("invalid_collector_self_telemetry_endpoint") unless self_telemetry_reader
{
  "file_storage/tempo" => ["/var/lib/otelcol/storage/queue/tempo", "/var/lib/otelcol/storage/queue/tempo-compaction"],
  "file_storage/loki" => ["/var/lib/otelcol/storage/queue/loki", "/var/lib/otelcol/storage/queue/loki-compaction"],
  "file_storage/langfuse" => ["/var/lib/otelcol/storage/queue/langfuse", "/var/lib/otelcol/storage/queue/langfuse-compaction"]
}.each do |name, (expected_directory, expected_compaction_directory)|
  storage = extensions.fetch(name)
  directory = storage.fetch("directory", "")
  # Queue persistence must live inside collector-data. A writable path outside
  # that mount would both lose retry evidence and require the non-root Collector
  # to create a root-owned parent directory at startup.
  fail_check("invalid_persistent_queue_storage:#{name}") unless directory == expected_directory
  fail_check("missing_persistent_queue_directory_creation:#{name}") unless storage["create_directory"] == true
  fail_check("invalid_persistent_queue_compaction_storage:#{name}") unless storage.dig("compaction", "directory") == expected_compaction_directory
end
extensions.each_key do |name|
  fail_check("unsupported_auth_extension") if name.match?(/auth/i)
end
ingress = pipeline(config, "traces/ingress")
infra = pipeline(config, "traces/infra")
ai = pipeline(config, "traces/ai")
metrics = pipeline(config, "metrics")
logs = pipeline(config, "logs")
require_exact(ingress.fetch("receivers"), ["otlp"], "invalid_ingress_receivers")
require_exact(ingress.fetch("exporters"), ["forward/infra", "forward/ai"], "invalid_ingress_fanout")
require_includes(ingress.fetch("processors", []), ["transform/redact-ingress"], "missing_ingress_redaction")
require_exact(infra.fetch("receivers"), ["forward/infra"], "invalid_infra_receivers")
require_exact(infra.fetch("exporters"), ["otlp/tempo"], "invalid_infra_exporters")
require_includes(infra.fetch("processors", []), ["transform/redact-downstream", "tail_sampling/retain"], "missing_infra_privacy_or_sampling")
require_exact(ai.fetch("receivers"), ["forward/ai"], "invalid_ai_receivers")
require_includes(ai.fetch("processors", []), ["filter/ai", "transform/redact-downstream", "tail_sampling/retain"], "missing_ai_filter_privacy_or_sampling")
require_exact(ai.fetch("exporters"), ["otlphttp/langfuse"], "invalid_ai_exporters")
require_exact(metrics.fetch("receivers"), ["otlp"], "invalid_metrics_receivers")
require_exact(metrics.fetch("exporters"), ["prometheus/app"], "invalid_metrics_exporters")
require_exact(logs.fetch("receivers"), ["otlp"], "invalid_logs_receivers")
require_exact(logs.fetch("exporters"), ["otlphttp/loki"], "invalid_logs_exporters")
require_includes(logs.fetch("processors", []), ["filter/http-completion-logs", "transform/redact-logs"], "missing_logs_identity_or_privacy_processor")
fail_check("legacy_logs_resource_inference") if logs.fetch("processors", []).include?("resource/http-completion-log-service")
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
%w[otlp/tempo otlphttp/loki otlphttp/langfuse].each do |name|
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
sensitive_values(exporters).each do |path, value|
  fail_check("unsafe_exporter_tls") if path.match?(/insecure_skip_verify/i) && value == "true"
  next unless path.match?(/authorization|api.?key|token|password|secret|header|credential|username|user|private_key/i)
  fail_check("literal_exporter_credential") unless value.match?(%r{\A(?:Bearer )?\$\{env:[A-Z][A-Z0-9_]*\}\z})
end
puts "collector_grafana_config_test: pass"
' "${CONFIG_PATH}"
