#!/usr/bin/env bash
# 静态配置门禁：仅解析指定目录中的受版本管理配置，不读取 .env、不启动 Docker。
# Ruby/Psych 是 macOS 自带的 YAML 解析器；safe_load 避免把配置当作可执行对象反序列化。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly DEFAULT_REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

repo_root="${DEFAULT_REPO_ROOT}"

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --repo-root)
      [[ "$#" -ge 2 ]] || {
        printf '%s\n' 'argument_error: --repo-root requires a path' >&2
        exit 2
      }
      repo_root="$2"
      shift 2
      ;;
    *)
      printf '%s\n' 'argument_error: unsupported argument' >&2
      exit 2
      ;;
  esac
done

repo_root="$(cd "${repo_root}" && pwd)"

exec ruby -ryaml -e '
require "pathname"

root = File.realpath(File.expand_path(ARGV.fetch(0)))

def fail_check(path, category)
  puts "#{path}: #{category}"
  exit 1
end

def load_yaml(path)
  fail_check(path, "missing_config_file") unless File.file?(path) && File.readable?(path)
  YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: true) || {}
rescue Psych::Exception
  fail_check(path, "invalid_yaml")
end

def env_values(path)
  values = {}
  File.foreach(path) do |line|
    next if line.strip.empty? || line.lstrip.start_with?("#")

    name, value = line.strip.split("=", 2)
    values[name] = value if name && value && name.match?(/\A[A-Z][A-Z0-9_]*\z/)
  end
  values
rescue Errno::ENOENT
  fail_check(path, "missing_version_matrix")
end

def required_file(path, category)
  fail_check(path, category) unless File.file?(path) && File.readable?(path)
end

def resolve_image(image, values)
  image.gsub(/\$\{([A-Z][A-Z0-9_]*)\}/) { values.fetch(Regexp.last_match(1), "") }
end

def each_scalar(value, &block)
  case value
  when Hash
    value.each_value { |nested| each_scalar(nested, &block) }
  when Array
    value.each { |nested| each_scalar(nested, &block) }
  when String
    yield value
  end
end

def host_port(port)
  return port.fetch("published", "").to_s if port.is_a?(Hash)
  return "" unless port.is_a?(String)

  pieces = port.split(":")
  pieces.length >= 2 ? pieces[-2] : ""
end

def collector_mounts(service, root, declared_volumes)
  Array(service["volumes"]).each_with_object([]) do |volume, mounts|
    source, target, volume_type, read_only = if volume.is_a?(Hash)
      [volume["source"], volume["target"], volume["type"], volume["read_only"] == true]
    elsif volume.is_a?(String)
      parts = volume.split(":", 3)
      [parts[0], parts[1], nil, parts.fetch(2, "").split(",").include?("ro")] if parts.length >= 2
    end
    next unless source.is_a?(String) && target.is_a?(String) && target.start_with?("/")

    is_named_volume = volume_type == "volume" || (volume_type.nil? && declared_volumes.key?(source))
    if is_named_volume
      next unless declared_volumes.key?(source)

      mounts << { "kind" => "named", "source" => source, "target" => target, "read_only" => read_only }
      next
    end

    next if source.empty? || source.match?(%r{(^|/)\.\.(/|$)})

    source_path = File.expand_path(source, root)
    begin
      canonical_source = File.realpath(source_path)
    rescue Errno::ENOENT, Errno::EACCES
      next
    end
    next unless canonical_source.start_with?("#{root}/")

    mounts << { "kind" => "bind", "source" => canonical_source, "target" => target, "read_only" => read_only }
  end
end

def storage_mount(directory, mounts)
  return nil unless directory.is_a?(String) && directory.start_with?("/")
  return nil if directory.match?(%r{(^|/)\.\.(/|$)})

  mounts.sort_by { |item| -item.fetch("target").length }.find do |item|
    target = item.fetch("target")
    directory == target || directory.start_with?("#{target}/")
  end
end

versions_path = File.join(root, "deploy/observability/versions.env")
compose_paths = %w[compose.grafana.yaml compose.langfuse.yaml compose.signoz.yaml].map do |name|
  path = File.join(root, "deploy/observability", name)
  required_file(path, "missing_compose_config")
  path
end
app_config_path = File.join(root, "manifest/config/config.yaml")
go_mod_path = File.join(root, "go.mod")
collector_paths = %w[collector-grafana.yaml collector-signoz.yaml].map do |name|
  path = File.join(root, "deploy/observability/collector", name)
  required_file(path, "missing_collector_config")
  path
end

required_file(versions_path, "missing_version_matrix")
required_file(app_config_path, "missing_application_config")
required_file(go_mod_path, "missing_go_mod")
values = env_values(versions_path)
collector_runtime_users = {}

compose_paths.sort.each do |compose_path|
  compose = load_yaml(compose_path)
  services = compose.fetch("services", {})
  declared_volumes = compose.fetch("volumes", {})
  unless services.is_a?(Hash) && !services.empty?
    fail_check(compose_path, "invalid_compose_services")
  end
  fail_check(compose_path, "invalid_compose_volumes") unless declared_volumes.is_a?(Hash)

  published_ports = {}
  profile = File.basename(compose_path).sub("compose.", "").sub(".yaml", "")
  services.each do |service_name, service|
    unless service.is_a?(Hash)
      fail_check(compose_path, "invalid_compose_service")
    end

    image = resolve_image(service.fetch("image", "").to_s, values)
    fail_check(versions_path, "latest_image") if image.match?(/(^|:)latest(?:$|@)/)
    fail_check(compose_path, "unresolved_image_tag") if image.empty? || image.include?("${")
    # Bucket initialization is deliberately a one-shot dependency: treating it
    # as a long-running healthy service would conceal a failed cold bootstrap.
    is_one_shot_initializer = %w[langfuse-minio-init langfuse-clickhouse-init].include?(service_name)
    if is_one_shot_initializer
      fail_check(compose_path, "invalid_one_shot_initializer") unless service["restart"] == "no" && !service.key?("healthcheck")
    else
      fail_check(compose_path, "missing_healthcheck") unless service["healthcheck"].is_a?(Hash)
    end

    limits = service.dig("deploy", "resources", "limits")
    unless limits.is_a?(Hash) && limits["cpus"] && limits["memory"]
      fail_check(compose_path, "missing_resource_limit")
    end

    Array(service["ports"]).each do |port|
      published = host_port(port)
      next if published.empty?

      fail_check(compose_path, "host_port_conflict") if published_ports[published]
      published_ports[published] = true
    end

    next unless service_name == "collector"

    runtime_user = service.fetch("user", "").to_s.split(":", 2).first
    unless runtime_user.match?(/\A\d+\z/)
      fail_check(compose_path, "storage_path_unavailable")
    end
    collector_runtime_users[profile] = {
      "uid" => runtime_user.to_i,
      "mounts" => collector_mounts(service, root, declared_volumes)
    }
  end
end

app_config = load_yaml(app_config_path)
each_scalar(app_config) do |value|
  if value.match?(/\b(?:tempo|loki|prometheus|grafana|signoz|langfuse)(?::|\/|\b)/i)
    fail_check(app_config_path, "application_backend_endpoint")
  end
end

go_mod = File.read(go_mod_path)
if go_mod.match?(%r{github\.com/opentracing/opentracing-go|github\.com/(?:uber|jaegertracing)/jaeger-client-go})
  fail_check(go_mod_path, "legacy_tracing_dependency")
end

collector_paths.sort.each do |collector_path|
  collector = load_yaml(collector_path)
  extensions = collector.fetch("extensions", {})
  receivers = collector.fetch("receivers", {})
  processors = collector.fetch("processors", {})
  connectors = collector.fetch("connectors", {})
  exporters = collector.fetch("exporters", {})
  pipelines = collector.dig("service", "pipelines") || {}

  unless extensions.is_a?(Hash) && receivers.is_a?(Hash) && processors.is_a?(Hash) && connectors.is_a?(Hash) && exporters.is_a?(Hash) && pipelines.is_a?(Hash)
    fail_check(collector_path, "invalid_collector_pipeline")
  end

  pipelines.each_value do |pipeline|
    unless pipeline.is_a?(Hash)
      fail_check(collector_path, "invalid_collector_pipeline")
    end

    pipeline_receivers = pipeline["receivers"]
    pipeline_exporters = pipeline["exporters"]
    unless pipeline_receivers.is_a?(Array) && !pipeline_receivers.empty? && pipeline_exporters.is_a?(Array) && !pipeline_exporters.empty?
      fail_check(collector_path, "invalid_collector_pipeline")
    end

    pipeline_receivers.each do |name|
      fail_check(collector_path, "invalid_collector_pipeline") unless receivers.key?(name) || connectors.key?(name)
    end
    Array(pipeline["processors"]).each do |name|
      fail_check(collector_path, "invalid_collector_pipeline") unless processors.key?(name)
    end
    pipeline_exporters.each do |name|
      fail_check(collector_path, "invalid_collector_pipeline") unless exporters.key?(name) || connectors.key?(name)
    end
  end

  profile = File.basename(collector_path).sub("collector-", "").sub(".yaml", "")
  runtime = collector_runtime_users[profile]
  fail_check(collector_path, "storage_path_unavailable") unless runtime

  exporters.each_value do |exporter|
    storage_name = exporter.dig("sending_queue", "storage") if exporter.is_a?(Hash)
    next unless storage_name

    storage = extensions[storage_name]
    directory = storage["directory"] if storage.is_a?(Hash)
    mount = storage_mount(directory, runtime.fetch("mounts"))
    fail_check(collector_path, "storage_path_unavailable") if mount && mount.fetch("read_only")
    next if mount && mount.fetch("kind") == "named"

    suffix = mount && directory.delete_prefix(mount.fetch("target")).sub(%r{\A/}, "")
    resolved_directory = suffix && File.expand_path(suffix, mount.fetch("source"))
    begin
      metadata = resolved_directory && File.lstat(resolved_directory)
      source_root = mount && mount.fetch("source")
      resolved_realpath = metadata && File.realpath(resolved_directory)
    rescue Errno::ENOENT, Errno::EACCES
      metadata = nil
      source_root = nil
      resolved_realpath = nil
    end
    owner_writable = metadata && metadata.uid == runtime.fetch("uid") && (metadata.mode & 0o200).positive?
    inside_mount = source_root && resolved_realpath && (resolved_realpath == source_root || resolved_realpath.start_with?("#{source_root}/"))
    unless metadata && !metadata.symlink? && metadata.directory? && owner_writable && inside_mount
      fail_check(collector_path, "storage_path_unavailable")
    end
  end
end
' "${repo_root}"
