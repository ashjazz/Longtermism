#!/usr/bin/env bash
# T043：只静态验证 glog JSONL -> filelog -> Loki 的配置契约；不得启动 Docker 或访问 Loki。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly GLOG_CONFIG_PATH="${REPO_ROOT}/manifest/config/glog-observability.yaml"
readonly COLLECTOR_CONFIG_PATH="${REPO_ROOT}/deploy/observability/collector/collector-grafana.yaml"
readonly LOKI_CONFIG_PATH="${REPO_ROOT}/deploy/observability/loki/loki.yaml"

[[ -f "${GLOG_CONFIG_PATH}" ]] || { printf '%s: missing_glog_config\n' "${GLOG_CONFIG_PATH}" >&2; exit 1; }
[[ -f "${COLLECTOR_CONFIG_PATH}" ]] || { printf '%s: missing_collector_config\n' "${COLLECTOR_CONFIG_PATH}" >&2; exit 1; }
[[ -f "${LOKI_CONFIG_PATH}" ]] || { printf '%s: missing_loki_config\n' "${LOKI_CONFIG_PATH}" >&2; exit 1; }

exec ruby -rjson -ryaml -e '
paths = ARGV.freeze

def fail_check(message)
  warn "#{ARGV.fetch(0)}: #{message}"
  exit 1
end

def read_yaml(path)
  YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: true) || {}
rescue Psych::Exception
  fail_check("invalid_yaml:#{File.basename(path)}")
end

def require_hash(value, category)
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

def require_exact(values, expected, category)
  fail_check(category) unless values.sort == expected.sort
end

def statements(processor, signal)
  entries = processor["#{signal}_statements"]
  return [] unless entries.is_a?(Array)

  entries.flat_map { |entry| entry.is_a?(Hash) ? Array(entry["statements"]) : [] }.map(&:to_s)
end

def delete_statement?(values, field)
  expected = /\Adelete_key\((?:log\.attributes|resource\.attributes),["'']#{Regexp.escape(field)}["'']\)\z/
  values.any? { |value| value.gsub(/\s+/, "").match?(expected) }
end

def attribute_action?(entries, action, attributes)
  entries.any? do |entry|
    entry.is_a?(Hash) && entry["action"] == action && Array(entry["attributes"]).map(&:to_s).sort == attributes.sort
  end
end

def keep_keys_statement?(values, fields)
  expected = "keep_keys(log.attributes,[\"#{fields.join(%q|","|)}\"])"
  values.any? { |value| value.gsub(/\s+/, "") == expected }
end

# 这两个 fixture 是 T041/T053 的交接契约：filelog 只能接收 JSONL 允许字段，且可用
# 原生 OTel TraceID/SpanID 回链 Tempo。malformed 行是输入故障，不得终止整个采集器。
valid_jsonl = <<~JSON
  {"timestamp":"2026-07-17T10:00:00Z","level":"info","message":"http request completed","request_id":"req-t043","trace_id":"0123456789abcdef0123456789abcdef","span_id":"0123456789abcdef","route":"/api/v1/observability/infra-smoke","method":"GET","status":200,"duration_ms":12}
JSON
fixture = JSON.parse(valid_jsonl)
required_fixture_keys = %w[timestamp level message request_id trace_id span_id route method status duration_ms]
fail_check("invalid_jsonl_fixture") unless fixture.keys.sort == required_fixture_keys.sort && fixture["trace_id"].match?(/\A[0-9a-f]{32}\z/) && fixture["span_id"].match?(/\A[0-9a-f]{16}\z/)
malformed_jsonl = "{not-json}"
begin
  JSON.parse(malformed_jsonl)
  fail_check("invalid_malformed_fixture")
rescue JSON::ParserError
  # 该 fixture 必须保持畸形，才能证明 json_parser 的 on_error 策略隔离坏行。
end
sensitive_jsonl = JSON.parse(%q|{"timestamp":"2026-07-17T10:00:01Z","level":"error","message":"http request failed","request_id":"req-t043-private","trace_id":"fedcba9876543210fedcba9876543210","span_id":"fedcba987654321","route":"/api/v1/health/ping","method":"GET","status":502,"duration_ms":15,"nested":{"token":"synthetic-t043-token"},"prompt":"synthetic-t043-prompt"}|)
fail_check("invalid_sensitive_fixture") unless sensitive_jsonl.dig("nested", "token") == "synthetic-t043-token" && sensitive_jsonl["prompt"] == "synthetic-t043-prompt"

glog = require_hash(read_yaml(paths.fetch(0)), "invalid_glog_config")
collector = require_hash(read_yaml(paths.fetch(1)), "invalid_collector_config")
loki = require_hash(read_yaml(paths.fetch(2)), "invalid_loki_config")

logger = require_hash(glog["logger"], "missing_glog_logger")
logs = require_hash(glog.dig("observability", "logs"), "missing_glog_observability")
fail_check("invalid_glog_jsonl") unless logs["format"] == "jsonl"
fail_check("invalid_glog_shared_path") unless logs["shared_volume_path"] == "/var/log/longtermism" && logger["path"] == "/var/log/longtermism"
fail_check("invalid_glog_file") unless logger["file"] == "application.jsonl" && logger["stdout"] == false && logger["header"] == false
fail_check("missing_glog_rotation") unless logger["rotateSize"].is_a?(String) && logger["rotateSize"].match?(/\A[1-9][0-9]*[KMG]B?\z/i) && logger["rotateBackupLimit"].is_a?(Integer) && logger["rotateBackupLimit"].positive? && logger["rotateBackupExpire"].is_a?(String) && logger["rotateBackupExpire"].match?(/\A[1-9][0-9]*[hd]\z/)

receivers = require_hash(collector["receivers"], "invalid_collector_receivers")
filelog = require_hash(receivers["filelog/glog"], "missing_glog_filelog_receiver")
fail_check("invalid_filelog_include") unless Array(filelog["include"]) == ["/var/log/longtermism/application.jsonl"] && filelog["start_at"] == "beginning"
fail_check("missing_rotation_handling") unless filelog["on_truncate"] == "read_whole_file"
operators = Array(filelog["operators"])
require_exact(operators.map { |operator| operator["type"] if operator.is_a?(Hash) }.compact, %w[json_parser move], "unexpected_filelog_operator")
json_parser = operators.find { |operator| operator.is_a?(Hash) && operator["type"] == "json_parser" }
fail_check("missing_json_parser") unless json_parser.is_a?(Hash)
fail_check("malformed_line_not_isolated") unless json_parser["on_error"] == "drop_quiet" && json_parser["parse_from"] == "body" && json_parser["parse_to"] == "attributes"
trace = require_hash(json_parser["trace"], "missing_filelog_trace_correlation")
fail_check("invalid_filelog_trace_correlation") unless trace.dig("trace_id", "parse_from") == "attributes.trace_id" && trace.dig("span_id", "parse_from") == "attributes.span_id"
message_move = operators.find { |operator| operator.is_a?(Hash) && operator["type"] == "move" && operator["from"] == "attributes.message" && operator["to"] == "body" }
fail_check("raw_json_body_not_replaced") unless message_move
fail_check("unsafe_filelog_operator_order") unless operators == [json_parser, message_move]

processors = require_hash(collector["processors"], "invalid_collector_processors")
redactor = require_hash(processors["transform/redact-logs"], "missing_log_redaction")
log_statements = statements(redactor, "log")
fail_check("redactor_can_rewrite_body") if log_statements.any? { |statement| statement.gsub(/\s+/, "").match?(/\A(?:set|replace_pattern|replace_all_patterns)\(body,/) }
%w[authorization cookie api_key token password prompt query output tool_arguments provider_error_body raw_query].each do |field|
  fail_check("missing_log_field_removal:#{field}") unless delete_statement?(log_statements, field)
end
allowed_log_fields = %w[timestamp level message request_id trace_id span_id route method status duration_ms error_class smoke_run_id]
fail_check("missing_log_attribute_allowlist") unless keep_keys_statement?(log_statements, allowed_log_fields)
pipelines = require_hash(collector.dig("service", "pipelines"), "invalid_collector_pipelines")
logs_pipelines = pipelines.select { |name, _| name.start_with?("logs") }
fail_check("unexpected_logs_pipeline") unless logs_pipelines.keys == ["logs"]
logs_pipeline = require_hash(logs_pipelines.fetch("logs"), "invalid_logs_pipeline")
require_exact(Array(logs_pipeline["receivers"]), ["filelog/glog"], "invalid_logs_receivers")
require_exact(Array(logs_pipeline["exporters"]), ["otlphttp/loki"], "invalid_logs_exporters")
fail_check("log_pipeline_can_bypass_redaction") unless Array(logs_pipeline["processors"]).include?("transform/redact-logs")
fail_check("unexpected_log_transform") if Array(logs_pipeline["processors"]).any? { |name| name.start_with?("transform/") && name != "transform/redact-logs" }
fail_check("invalid_loki_native_otlp_exporter") unless collector.dig("exporters", "otlphttp/loki", "endpoint") == "http://loki:3100/otlp" && !collector.fetch("exporters", {}).keys.any? { |name| name.start_with?("loki") }

limits = require_hash(loki["limits_config"], "missing_loki_limits")
fail_check("structured_metadata_disabled") unless limits["allow_structured_metadata"] == true
fail_check("missing_loki_delete_request_store") unless loki.dig("compactor", "delete_request_store") == "filesystem"
otlp = require_hash(limits["otlp_config"], "missing_loki_native_otlp")
resource_attributes = require_hash(otlp["resource_attributes"], "missing_loki_resource_attributes")
log_attributes = otlp["log_attributes"]
fail_check("missing_loki_log_attributes") unless log_attributes.is_a?(Array)
resource_entries = Array(resource_attributes["attributes_config"])
log_entries = log_attributes
low_cardinality_labels = %w[service.name service.namespace deployment.environment]
fail_check("invalid_loki_index_labels") unless attribute_action?(resource_entries, "index_label", low_cardinality_labels)
index_entries = resource_entries.select { |entry| entry.is_a?(Hash) && entry["action"] == "index_label" }
fail_check("invalid_loki_index_labels") unless index_entries.length == 1 && log_entries.none? { |entry| entry.is_a?(Hash) && entry["action"] == "index_label" }
high_cardinality = %w[request_id trace_id span_id ai_trace_id smoke_run_id]
fail_check("high_cardinality_loki_label") if index_entries.any? { |entry| scalar_strings(entry).any? { |value| high_cardinality.include?(value) } }
fail_check("missing_loki_structured_metadata") unless attribute_action?(log_entries, "structured_metadata", %w[request_id trace_id span_id route method status duration_ms error_class smoke_run_id])
%w[authorization cookie api_key token password prompt query output tool_arguments provider_error_body raw_query].each do |field|
  fail_check("loki_raw_payload_not_dropped:#{field}") unless log_entries.any? { |entry| entry.is_a?(Hash) && entry["action"] == "drop" && Array(entry["attributes"]).include?(field) }
end
forbidden = %w[authorization api_key prompt query output tool_arguments provider_error_body raw_query]
rendered = scalar_strings([glog, collector, loki]).join("\n").downcase
fail_check("literal_sensitive_payload_in_config") if forbidden.any? { |field| rendered.include?("synthetic-t043-#{field}") }

puts "glog_filelog_fixture_test: pass"
' "${GLOG_CONFIG_PATH}" "${COLLECTOR_CONFIG_PATH}" "${LOKI_CONFIG_PATH}"
