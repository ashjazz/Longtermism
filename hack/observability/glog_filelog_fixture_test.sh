#!/usr/bin/env bash
# T043 compatibility gate: completion logs now enter through OTLP. The historical filename is
# retained so existing verification targets keep working, but filelog/host JSONL is forbidden.
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly COLLECTOR_CONFIG_PATH="${REPO_ROOT}/deploy/observability/collector/collector-grafana.yaml"
readonly LOKI_CONFIG_PATH="${REPO_ROOT}/deploy/observability/loki/loki.yaml"

exec ruby -ryaml -e '
def fail_check(message)
  warn "#{ARGV.fetch(0)}: #{message}"
  exit 1
end

def read_yaml(path)
  YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: true) || {}
rescue Errno::ENOENT, Psych::Exception
  fail_check("invalid_yaml:#{File.basename(path)}")
end

def exact_action?(entries, action, attributes)
  entries.any? do |entry|
    entry.is_a?(Hash) && entry["action"] == action && Array(entry["attributes"]).map(&:to_s).sort == attributes.sort
  end
end

def keep_keys(processor, context)
  groups = Array(processor["log_statements"]).select { |group| group.is_a?(Hash) && group["context"] == context }
  statement = groups.flat_map { |group| Array(group["statements"]) }.map { |value| value.to_s.gsub(/\s+/, "") }.find { |value| value.start_with?("keep_keys(") }
  match = /\Akeep_keys\((?:log\.|resource\.)?attributes,\[(.*)\]\)\z/.match(statement.to_s)
  fail_check("missing_#{context}_allowlist") unless match
  match[1].scan(/["\x27]([^"\x27]+)["\x27]/).flatten
end

collector = read_yaml(ARGV.fetch(0))
loki = read_yaml(ARGV.fetch(1))
receivers = collector.fetch("receivers", {})
fail_check("legacy_filelog_receiver") if receivers.keys.any? { |name| name == "filelog" || name.start_with?("filelog/") }
logs = collector.dig("service", "pipelines", "logs")
fail_check("invalid_logs_pipeline") unless logs.is_a?(Hash) && Array(logs["receivers"]) == ["otlp"] && Array(logs["exporters"]) == ["otlphttp/loki"]
fail_check("log_pipeline_can_bypass_redaction") unless Array(logs["processors"]) == ["filter/http-completion-logs", "transform/redact-logs"]

redactor = collector.dig("processors", "transform/redact-logs")
fail_check("missing_log_redaction") unless redactor.is_a?(Hash)
fail_check("unsafe_log_redaction_error_mode") unless redactor["error_mode"] == "propagate"
expected_log = %w[request_id trace_id span_id route method status duration_ms error_class ai_trace_id smoke_run_id]
expected_resource = %w[service.name service.version deployment.environment]
fail_check("invalid_log_attribute_allowlist") unless keep_keys(redactor, "log").sort == expected_log.sort
fail_check("invalid_resource_attribute_allowlist") unless keep_keys(redactor, "resource").sort == expected_resource.sort

limits = loki.fetch("limits_config", {})
fail_check("structured_metadata_disabled") unless limits["allow_structured_metadata"] == true
otlp = limits.fetch("otlp_config", {})
resource_entries = Array(otlp.dig("resource_attributes", "attributes_config"))
log_entries = Array(otlp["log_attributes"])
index_labels = %w[service.name service.namespace deployment.environment]
structured = %w[request_id trace_id span_id route method status duration_ms error_class ai_trace_id smoke_run_id]
fail_check("invalid_loki_index_labels") unless exact_action?(resource_entries, "index_label", index_labels)
fail_check("high_cardinality_loki_label") if log_entries.any? { |entry| entry.is_a?(Hash) && entry["action"] == "index_label" }
fail_check("missing_loki_structured_metadata") unless exact_action?(log_entries, "structured_metadata", structured)

puts "otlp_log_loki_contract_test: pass"
' "${COLLECTOR_CONFIG_PATH}" "${LOKI_CONFIG_PATH}"
