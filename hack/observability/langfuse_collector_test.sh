#!/usr/bin/env bash
# T082：离线固定 Langfuse trace downstream 的协议、凭据和隔离队列契约。
# 该测试只解析版本管理的 YAML，绝不启动 Docker 或向平台发送任何 trace。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly COLLECTOR_PATH="${REPO_ROOT}/deploy/observability/collector/collector-grafana.yaml"
readonly COMPOSE_PATH="${REPO_ROOT}/deploy/observability/compose.grafana.yaml"

for required_path in "${COLLECTOR_PATH}" "${COMPOSE_PATH}"; do
  [[ -f "${required_path}" ]] || { printf '%s: missing_langfuse_downstream_config\n' "${required_path}" >&2; exit 1; }
done

exec ruby -ryaml -e '
collector_path, compose_path = ARGV.freeze

def fail_check(category)
  warn "#{File.basename(ARGV.fetch(0))}: #{category}"
  exit 1
end

def load_yaml(path)
  YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: true) || {}
rescue Psych::Exception
  fail_check("invalid_langfuse_downstream_yaml")
end

def required_hash(value, category)
  fail_check(category) unless value.is_a?(Hash)
  value
end

def expected_env_reference?(value, variable)
  value.is_a?(String) && value == "${env:#{variable}}"
end

collector = required_hash(load_yaml(collector_path), "invalid_collector_config")
compose = required_hash(load_yaml(compose_path), "invalid_grafana_compose")
exporters = required_hash(collector["exporters"], "missing_langfuse_exporter")
exporter = required_hash(exporters["otlphttp/langfuse"], "missing_langfuse_exporter")
pipelines = required_hash(collector.dig("service", "pipelines"), "missing_langfuse_ai_pipeline")
ai_pipeline = required_hash(pipelines["traces/ai"], "missing_langfuse_ai_pipeline")
processors = required_hash(collector["processors"], "missing_langfuse_ai_filter")
ai_filter = required_hash(processors["filter/ai"], "missing_langfuse_ai_filter")
extensions = required_hash(collector["extensions"], "missing_langfuse_file_storage")
services = required_hash(compose["services"], "missing_collector_service")
collector_service = required_hash(services["collector"], "missing_collector_service")
environment = required_hash(collector_service["environment"], "missing_langfuse_secret_injection")

# Langfuse OTLP trace ingestion is HTTP/protobuf only. A gRPC exporter can look valid
# in a generic Collector pipeline but silently fails against the actual public endpoint.
fail_check("invalid_langfuse_otlp_http_protobuf") unless exporter["endpoint"] == "http://langfuse-web:3000/api/public/otel" && exporter["encoding"] == "proto"
headers = required_hash(exporter["headers"], "missing_langfuse_ingestion_headers")
fail_check("missing_langfuse_ingestion_headers") unless expected_env_reference?(headers["Authorization"], "LANGFUSE_OTLP_AUTHORIZATION") && expected_env_reference?(headers["x-langfuse-ingestion-version"], "LANGFUSE_OTEL_INGESTION_VERSION")

# Collector config can name environment references, but only Compose owns their resolution.
# This prevents checked-in credentials from entering the Collector file or test diagnostics.
{
  "LANGFUSE_OTLP_AUTHORIZATION" => "LANGFUSE_OTLP_AUTHORIZATION",
  "LANGFUSE_OTEL_INGESTION_VERSION" => "LANGFUSE_OTEL_INGESTION_VERSION"
}.each do |key, variable|
  value = environment[key]
  fail_check("missing_langfuse_secret_injection") unless value.is_a?(String) && value.match?(/\A\$\{#{Regexp.escape(variable)}:\?[^}]+\}\z/)
end

# AI downstream must reject unmarked infrastructure spans. The marker is present only on the
# root/bridge and explicit AI semantic spans; a filter must not infer it from route names.
filter_statements = Array(ai_filter.dig("traces", "span")).map { |statement| statement.to_s.gsub(/\s+/, "") }
plane_drop = /attributes\["longtermism\.observability\.plane"\]!="ai"/
fail_check("invalid_langfuse_ai_filter") unless filter_statements.any? { |statement| statement.match?(plane_drop) }
fail_check("invalid_langfuse_ai_pipeline") unless Array(ai_pipeline["exporters"]) == ["otlp/tempo", "otlphttp/langfuse"] && Array(ai_pipeline["processors"]).include?("filter/ai")

# Langfuse is an independent failure domain: it needs its own durable queue, retry budget and
# request deadline rather than borrowing Tempo or Loki storage settings.
queue = required_hash(exporter["sending_queue"], "missing_langfuse_queue")
retry_policy = required_hash(exporter["retry_on_failure"], "missing_langfuse_retry")
storage = required_hash(extensions["file_storage/langfuse"], "missing_langfuse_file_storage")
fail_check("invalid_langfuse_queue") unless queue == {"enabled" => true, "queue_size" => 10000, "storage" => "file_storage/langfuse"}
fail_check("invalid_langfuse_file_storage") unless storage["directory"] == "/var/lib/otelcol/storage/queue/langfuse" && storage["create_directory"] == true && storage.dig("compaction", "directory") == "/var/lib/otelcol/storage/queue/langfuse-compaction"
fail_check("invalid_langfuse_retry") unless retry_policy == {"enabled" => true, "initial_interval" => "1s", "max_interval" => "30s", "max_elapsed_time" => "0s"}
fail_check("invalid_langfuse_timeout") unless exporter["timeout"] == "10s"

puts "langfuse_collector_test: pass"
' "${COLLECTOR_PATH}" "${COMPOSE_PATH}"
