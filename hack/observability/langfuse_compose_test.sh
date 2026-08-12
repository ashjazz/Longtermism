#!/usr/bin/env bash
# T083：离线校验 self-hosted Langfuse Compose 的可运行、安全和数据留存边界。
# 只读取受版本管理的 YAML/env；不启动 Docker，也不解析任何本地 secret 文件。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly COMPOSE_PATH="${REPO_ROOT}/deploy/observability/compose.langfuse.yaml"
readonly VERSIONS_PATH="${REPO_ROOT}/deploy/observability/versions.env"

for required_path in "${COMPOSE_PATH}" "${VERSIONS_PATH}"; do
  [[ -f "${required_path}" ]] || { printf '%s: missing_langfuse_compose_contract\n' "${required_path}" >&2; exit 1; }
done

exec ruby -ryaml -e '
compose_path, versions_path = ARGV.freeze

def fail_check(category)
  warn "#{File.basename(ARGV.fetch(0))}: #{category}"
  exit 1
end

def load_yaml(path)
  YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: true) || {}
rescue Psych::Exception
  fail_check("invalid_langfuse_compose")
end

def required_hash(value, category)
  fail_check(category) unless value.is_a?(Hash)
  value
end

def required_env_reference?(value, variable)
  value.is_a?(String) && value.match?(/\A\$\{#{Regexp.escape(variable)}:\?[^}]+\}\z/)
end

def healthcheck?(service)
  healthcheck = service["healthcheck"]
  healthcheck.is_a?(Hash) && healthcheck["test"].is_a?(Array) && healthcheck["test"].length == 2 && healthcheck["interval"].to_s.match?(/\A[1-9][0-9]*s\z/) && healthcheck["timeout"].to_s.match?(/\A[1-9][0-9]*s\z/) && healthcheck["retries"].is_a?(Integer) && healthcheck["retries"].positive?
end

def dependency_names(value)
  value.is_a?(Hash) ? value.keys : Array(value)
end

def version_values(path)
  File.readlines(path, chomp: true).each_with_object({}) do |line, values|
    next if line.empty? || line.start_with?("#")
    key, value = line.split("=", 2)
    values[key] = value if key && value
  end
end

def fixed_image_tag?(value)
  return true if value.match?(%r{\A[^@\s]+@sha256:[0-9a-f]{64}\z})

  image, tag = value.to_s.rpartition(":").values_at(0, 2)
  return false if image.empty? || tag.empty?

  tag.match?(%r{\A(?:v?\d+\.\d+(?:\.\d+){0,2}(?:[-+][0-9A-Za-z.-]+)?|RELEASE\.\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}Z)\z})
end

def require_environment_references(services, requirements)
  requirements.each do |service_name, variables|
    environment = required_hash(services.fetch(service_name)["environment"], "missing_credential_environment:#{service_name}")
    variables.each do |key, variable|
      fail_check("unsafe_or_default_credential:#{service_name}:#{key}") unless required_env_reference?(environment[key], variable)
    end
  end
end

compose = required_hash(load_yaml(compose_path), "invalid_langfuse_compose")
services = required_hash(compose["services"], "missing_langfuse_services")
versions = version_values(versions_path)
required_services = %w[langfuse-db langfuse-clickhouse langfuse-clickhouse-init langfuse-redis langfuse-minio langfuse-minio-init langfuse-web langfuse-worker]
fail_check("invalid_langfuse_services") unless services.keys.sort == required_services.sort

image_variables = {
  "langfuse-web" => "LANGFUSE_IMAGE",
  "langfuse-worker" => "LANGFUSE_WORKER_IMAGE",
  "langfuse-db" => "LANGFUSE_POSTGRES_IMAGE",
  "langfuse-clickhouse" => "LANGFUSE_CLICKHOUSE_IMAGE",
  "langfuse-redis" => "LANGFUSE_REDIS_IMAGE",
  "langfuse-minio" => "LANGFUSE_MINIO_IMAGE"
}
image_variables.each do |service_name, variable|
  image = services.fetch(service_name)["image"]
  version = versions.fetch(variable, "")
  fail_check("unfixed_langfuse_image:#{service_name}") unless image == "${#{variable}}" && fixed_image_tag?(version)
end

# All credential-bearing values must stay as fail-fast references. This is deliberately
# exhaustive so a future convenience default cannot silently become a real deployment secret.
require_environment_references(services, {
  "langfuse-db" => {"POSTGRES_PASSWORD" => "LANGFUSE_POSTGRES_PASSWORD"},
  "langfuse-clickhouse" => {"CLICKHOUSE_PASSWORD" => "LANGFUSE_CLICKHOUSE_ADMIN_PASSWORD"},
  "langfuse-clickhouse-init" => {"LANGFUSE_CLICKHOUSE_PASSWORD" => "LANGFUSE_CLICKHOUSE_PASSWORD", "LANGFUSE_CLICKHOUSE_ADMIN_PASSWORD" => "LANGFUSE_CLICKHOUSE_ADMIN_PASSWORD"},
  "langfuse-minio" => {"MINIO_ROOT_USER" => "LANGFUSE_MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD" => "LANGFUSE_MINIO_ROOT_PASSWORD"},
  "langfuse-minio-init" => {"MINIO_ROOT_USER" => "LANGFUSE_MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD" => "LANGFUSE_MINIO_ROOT_PASSWORD", "LANGFUSE_S3_EVENT_UPLOAD_BUCKET" => "LANGFUSE_S3_EVENT_UPLOAD_BUCKET", "LANGFUSE_MINIO_ACCESS_KEY_ID" => "LANGFUSE_MINIO_ACCESS_KEY_ID", "LANGFUSE_MINIO_SECRET_ACCESS_KEY" => "LANGFUSE_MINIO_SECRET_ACCESS_KEY"},
  "langfuse-web" => {"DATABASE_URL" => "LANGFUSE_DATABASE_URL", "DIRECT_URL" => "LANGFUSE_DIRECT_URL", "SALT" => "LANGFUSE_SALT", "ENCRYPTION_KEY" => "LANGFUSE_ENCRYPTION_KEY", "NEXTAUTH_SECRET" => "LANGFUSE_NEXTAUTH_SECRET", "NEXTAUTH_URL" => "LANGFUSE_NEXTAUTH_URL", "REDIS_CONNECTION_STRING" => "LANGFUSE_REDIS_CONNECTION_STRING", "CLICKHOUSE_URL" => "LANGFUSE_CLICKHOUSE_URL", "CLICKHOUSE_MIGRATION_URL" => "LANGFUSE_CLICKHOUSE_MIGRATION_URL", "CLICKHOUSE_USER" => "LANGFUSE_CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD" => "LANGFUSE_CLICKHOUSE_PASSWORD", "LANGFUSE_S3_EVENT_UPLOAD_BUCKET" => "LANGFUSE_S3_EVENT_UPLOAD_BUCKET", "LANGFUSE_S3_EVENT_UPLOAD_ACCESS_KEY_ID" => "LANGFUSE_MINIO_ACCESS_KEY_ID", "LANGFUSE_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY" => "LANGFUSE_MINIO_SECRET_ACCESS_KEY"},
  "langfuse-worker" => {"DATABASE_URL" => "LANGFUSE_DATABASE_URL", "DIRECT_URL" => "LANGFUSE_DIRECT_URL", "SALT" => "LANGFUSE_SALT", "ENCRYPTION_KEY" => "LANGFUSE_ENCRYPTION_KEY", "NEXTAUTH_SECRET" => "LANGFUSE_NEXTAUTH_SECRET", "REDIS_CONNECTION_STRING" => "LANGFUSE_REDIS_CONNECTION_STRING", "CLICKHOUSE_URL" => "LANGFUSE_CLICKHOUSE_URL", "CLICKHOUSE_MIGRATION_URL" => "LANGFUSE_CLICKHOUSE_MIGRATION_URL", "CLICKHOUSE_USER" => "LANGFUSE_CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD" => "LANGFUSE_CLICKHOUSE_PASSWORD", "LANGFUSE_S3_EVENT_UPLOAD_BUCKET" => "LANGFUSE_S3_EVENT_UPLOAD_BUCKET", "LANGFUSE_S3_EVENT_UPLOAD_ACCESS_KEY_ID" => "LANGFUSE_MINIO_ACCESS_KEY_ID", "LANGFUSE_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY" => "LANGFUSE_MINIO_SECRET_ACCESS_KEY"}
})

%w[langfuse-db langfuse-clickhouse langfuse-redis langfuse-minio].each do |service_name|
  fail_check("missing_langfuse_healthcheck:#{service_name}") unless healthcheck?(services.fetch(service_name))
end
web = required_hash(services["langfuse-web"], "missing_langfuse_web")
worker = required_hash(services["langfuse-worker"], "missing_langfuse_worker")
fail_check("missing_langfuse_healthcheck:web") unless healthcheck?(web)
fail_check("unexpected_langfuse_worker_healthcheck") if worker.key?("healthcheck")
%w[langfuse-db langfuse-clickhouse langfuse-redis].each do |dependency|
  [web, worker].each do |service|
    fail_check("missing_langfuse_dependency:#{dependency}") unless dependency_names(service["depends_on"]).include?(dependency) && service.dig("depends_on", dependency, "condition") == "service_healthy"
  end
end
%w[langfuse-clickhouse-init langfuse-minio-init].each do |initializer|
  [web, worker].each { |service| fail_check("missing_langfuse_initializer:#{initializer}") unless service.dig("depends_on", initializer, "condition") == "service_completed_successfully" }
end

# Project retention removes event blobs as well as database rows. The scoped MinIO
# identity needs delete permission on events only; root credentials must not reach web/worker.
minio_init = required_hash(services["langfuse-minio-init"], "missing_langfuse_minio_initializer")
minio_policy = Array(minio_init["entrypoint"]).join(" ")
scoped_delete = /"Action":\["s3:GetObject","s3:PutObject","s3:DeleteObject"\],"Resource":\["arn:aws:s3:::.*LANGFUSE_S3_EVENT_UPLOAD_BUCKET.*\/events\/\*"\]/
fail_check("missing_langfuse_retention_delete_permission") unless minio_policy.match?(scoped_delete)
fail_check("unsafe_langfuse_bucket_permission") if minio_policy.match?(/"Action":\["s3:\*"\]/)
%w[langfuse-web langfuse-worker].each do |service_name|
  environment = required_hash(services.fetch(service_name)["environment"], "missing_credential_environment:#{service_name}")
  fail_check("unsafe_langfuse_root_credential:#{service_name}") if environment.key?("MINIO_ROOT_USER") || environment.key?("MINIO_ROOT_PASSWORD")
end

# Langfuse retention is a project-level policy. Pin it at 14 days during headless
# initialization and require the EE entitlement rather than silently retaining forever.
web_environment = required_hash(web["environment"], "missing_langfuse_web_environment")
worker_environment = required_hash(worker["environment"], "missing_langfuse_worker_environment")
fail_check("missing_langfuse_retention") unless web_environment["LANGFUSE_INIT_PROJECT_RETENTION"] == 14
fail_check("missing_langfuse_retention_license") unless required_env_reference?(web_environment["LANGFUSE_EE_LICENSE_KEY"], "LANGFUSE_EE_LICENSE_KEY") && required_env_reference?(worker_environment["LANGFUSE_EE_LICENSE_KEY"], "LANGFUSE_EE_LICENSE_KEY")
%w[LANGFUSE_INIT_ORG_ID LANGFUSE_INIT_PROJECT_ID LANGFUSE_INIT_PROJECT_PUBLIC_KEY LANGFUSE_INIT_PROJECT_SECRET_KEY LANGFUSE_INIT_USER_EMAIL LANGFUSE_INIT_USER_PASSWORD].each do |variable|
  fail_check("missing_langfuse_retention_initialization") unless required_env_reference?(web_environment[variable], variable)
end

# UI access remains local-only. The test deliberately rejects a default account by requiring
# an external password reference; no secret value is read or rendered by this script.
ports = Array(web["ports"])
fail_check("invalid_langfuse_loopback_ui") unless ports == [{"target" => 3000, "published" => 3001, "host_ip" => "127.0.0.1"}]

volumes = required_hash(compose["volumes"], "missing_langfuse_volumes")
expected_volumes = %w[langfuse-clickhouse-data langfuse-minio-data langfuse-postgres-data langfuse-redis-data]
fail_check("invalid_langfuse_volumes") unless volumes.keys.sort == expected_volumes && volumes.values.all? { |definition| definition == {} }
raw_markers = /raw|debug|payload/i
services.each_value do |service|
  Array(service["volumes"]).each do |mount|
    fail_check("unsafe_langfuse_raw_volume") unless mount.is_a?(Hash)
    fail_check("unsafe_langfuse_raw_volume") if mount["source"].to_s.match?(raw_markers) || mount["target"].to_s.match?(raw_markers)
  end
end

puts "langfuse_compose_test: pass"
' "${COMPOSE_PATH}" "${VERSIONS_PATH}"
