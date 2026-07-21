#!/usr/bin/env bash
# 此脚本是 T001 的 RED 契约：它只构造临时配置树，不读取仓库或用户的 .env。
# 后续 T002 的检查器必须在离线状态下拒绝每一种不安全配置，并保持错误类别稳定。
set -u -o pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly CHECKER_PATH="${SCRIPT_DIR}/config_check.sh"
readonly SYNTHETIC_SECRET="synthetic-secret-do-not-print"

temp_root="$(mktemp -d)"
trap 'rm -rf "${temp_root}"' EXIT

failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

write_base_fixture() {
  local fixture_root="$1"

  mkdir -p \
    "${fixture_root}/deploy/observability/collector" \
    "${fixture_root}/manifest/config" \
    "${fixture_root}/storage/tempo"

  cat >"${fixture_root}/deploy/observability/versions.env" <<'EOF'
OTELCOL_CONTRIB_IMAGE=otel/opentelemetry-collector-contrib:0.156.0
EOF

  cat >"${fixture_root}/deploy/observability/compose.grafana.yaml" <<'EOF'
services:
  collector:
    image: ${OTELCOL_CONTRIB_IMAGE}
    ports:
      - "127.0.0.1:4317:4317"
    healthcheck:
      test: ["CMD", "true"]
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 512M
    user: "__CURRENT_UID__"
    volumes:
      - type: bind
        source: __STORAGE_SOURCE__
        target: /var/lib/otelcol/storage
EOF
  sed -i.bak "s/__CURRENT_UID__/$(id -u)/" "${fixture_root}/deploy/observability/compose.grafana.yaml"
  sed -i.bak "s|__STORAGE_SOURCE__|${fixture_root}/storage|" "${fixture_root}/deploy/observability/compose.grafana.yaml"
  rm -f "${fixture_root}/deploy/observability/compose.grafana.yaml.bak"
  cp "${fixture_root}/deploy/observability/compose.grafana.yaml" "${fixture_root}/deploy/observability/compose.signoz.yaml"
  cat >"${fixture_root}/deploy/observability/compose.langfuse.yaml" <<'EOF'
services:
  langfuse-web:
    image: ${OTELCOL_CONTRIB_IMAGE}
    healthcheck:
      test: ["CMD", "true"]
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 512M
EOF

  cat >"${fixture_root}/manifest/config/config.yaml" <<'EOF'
observability:
  enabled: false
  mode: collector
  collector:
    endpoint: otel-collector:4317
EOF

  cat >"${fixture_root}/deploy/observability/collector/collector-grafana.yaml" <<EOF
extensions:
  file_storage/tempo:
    # The fixture uses an absolute path so static validation has no ambiguous CWD.
    directory: /var/lib/otelcol/storage/tempo
receivers:
  otlp:
    protocols:
      grpc: {}
connectors:
  forward/infra: {}
exporters:
  otlp/tempo:
    endpoint: tempo:4317
    sending_queue:
      storage: file_storage/tempo
service:
  extensions: [file_storage/tempo]
  pipelines:
    traces/ingress:
      receivers: [otlp]
      exporters: [forward/infra]
    traces/infra:
      receivers: [forward/infra]
      exporters: [otlp/tempo]
EOF
  cp "${fixture_root}/deploy/observability/collector/collector-grafana.yaml" "${fixture_root}/deploy/observability/collector/collector-signoz.yaml"

  cat >"${fixture_root}/go.mod" <<'EOF'
module example.com/config-check-fixture

go 1.25.0

require go.opentelemetry.io/otel v1.44.0
EOF

  # The checker must ignore this fixture-local value and never load a real .env.
  printf 'OTEL_EXPORTER_OTLP_HEADERS=Authorization=%s\n' "${SYNTHETIC_SECRET}" >"${fixture_root}/.env"
}

apply_fixture_mutation() {
  local name="$1"
  local fixture_root="$2"

  case "${name}" in
    valid_minimum)
      ;;
    latest_image)
      printf 'OTELCOL_CONTRIB_IMAGE=otel/opentelemetry-collector-contrib:latest\n' >"${fixture_root}/deploy/observability/versions.env"
      ;;
    missing_healthcheck)
      cat >"${fixture_root}/deploy/observability/compose.grafana.yaml" <<'EOF'
services:
  collector:
    image: ${OTELCOL_CONTRIB_IMAGE}
    ports:
      - "127.0.0.1:4317:4317"
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 512M
EOF
      ;;
    missing_resource_limit)
      sed -i.bak '/deploy:/,$d' "${fixture_root}/deploy/observability/compose.grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/compose.grafana.yaml.bak"
      ;;
    host_port_conflict)
      cat >>"${fixture_root}/deploy/observability/compose.grafana.yaml" <<'EOF'
  second-collector:
    image: ${OTELCOL_CONTRIB_IMAGE}
    ports:
      - "127.0.0.1:4317:4317"
    healthcheck:
      test: ["CMD", "true"]
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 512M
EOF
      ;;
    application_backend_endpoint)
      printf '\n  tempo_endpoint: tempo:3200\n' >>"${fixture_root}/manifest/config/config.yaml"
      ;;
    legacy_tracing_dependency)
      printf '\nrequire github.com/opentracing/opentracing-go v1.2.0\n' >>"${fixture_root}/go.mod"
      ;;
    legacy_jaeger_dependency)
      printf '\nrequire github.com/uber/jaeger-client-go v2.30.0+incompatible\n' >>"${fixture_root}/go.mod"
      ;;
    invalid_collector_pipeline)
      sed -i.bak 's/exporters: \[otlp\/tempo\]/exporters: [otlp\/missing]/' "${fixture_root}/deploy/observability/collector/collector-grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/collector/collector-grafana.yaml.bak"
      ;;
    storage_path_unavailable)
      sed -i.bak 's|storage/tempo|storage/missing|' "${fixture_root}/deploy/observability/collector/collector-grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/collector/collector-grafana.yaml.bak"
      ;;
    missing_compose_config)
      rm "${fixture_root}/deploy/observability/compose.signoz.yaml"
      ;;
    missing_collector_config)
      rm "${fixture_root}/deploy/observability/collector/collector-signoz.yaml"
      ;;
    storage_path_symlink)
      ln -s "${fixture_root}/storage/tempo" "${fixture_root}/storage/link"
      sed -i.bak 's|storage/tempo|storage/link|' "${fixture_root}/deploy/observability/collector/collector-grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/collector/collector-grafana.yaml.bak"
      ;;
    storage_runtime_user_unwritable)
      sed -i.bak 's/user: "[0-9][0-9]*"/user: "99999"/' "${fixture_root}/deploy/observability/compose.grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/compose.grafana.yaml.bak"
      ;;
    empty_collector_pipeline)
      sed -i.bak 's/receivers: \[otlp\]/receivers: []/' "${fixture_root}/deploy/observability/collector/collector-grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/collector/collector-grafana.yaml.bak"
      ;;
    named_storage_volume)
      sed -i.bak 's|source: .*storage|source: collector-storage|' "${fixture_root}/deploy/observability/compose.grafana.yaml"
      sed -i.bak 's/type: bind/type: volume/' "${fixture_root}/deploy/observability/compose.grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/compose.grafana.yaml.bak"
      cat >>"${fixture_root}/deploy/observability/compose.grafana.yaml" <<'EOF'
volumes:
  collector-storage: {}
EOF
      ;;
    named_storage_volume_read_only)
      sed -i.bak 's|source: .*storage|source: collector-storage|' "${fixture_root}/deploy/observability/compose.grafana.yaml"
      sed -i.bak 's/type: bind/type: volume/' "${fixture_root}/deploy/observability/compose.grafana.yaml"
      awk '{ print; if ($0 ~ /target: \/var\/lib\/otelcol\/storage/) print "        read_only: true" }' "${fixture_root}/deploy/observability/compose.grafana.yaml" >"${fixture_root}/deploy/observability/compose.grafana.yaml.tmp"
      mv "${fixture_root}/deploy/observability/compose.grafana.yaml.tmp" "${fixture_root}/deploy/observability/compose.grafana.yaml"
      cat >>"${fixture_root}/deploy/observability/compose.grafana.yaml" <<'EOF'
volumes:
  collector-storage: {}
EOF
      ;;
    bind_storage_read_only)
      awk '{ print; if ($0 ~ /target: \/var\/lib\/otelcol\/storage/) print "        read_only: true" }' "${fixture_root}/deploy/observability/compose.grafana.yaml" >"${fixture_root}/deploy/observability/compose.grafana.yaml.tmp"
      mv "${fixture_root}/deploy/observability/compose.grafana.yaml.tmp" "${fixture_root}/deploy/observability/compose.grafana.yaml"
      ;;
    storage_mount_symlink)
      local outside_root
      outside_root="$(dirname "${fixture_root}")/outside-storage"
      mkdir -p "${outside_root}/tempo"
      ln -s "${outside_root}" "${fixture_root}/storage-link"
      sed -i.bak "s|source: .*storage|source: ${fixture_root}/storage-link|" "${fixture_root}/deploy/observability/compose.grafana.yaml"
      rm -f "${fixture_root}/deploy/observability/compose.grafana.yaml.bak"
      ;;
    *)
      fail "unsupported fixture mutation: ${name}"
      ;;
  esac
}

run_case() {
  local name="$1"
  local expected_status="$2"
  local expected_category="$3"
  local expected_path="$4"
  local fixture_root="${temp_root}/${name}"
  local output_file="${fixture_root}/output.txt"
  local exit_code=0

  write_base_fixture "${fixture_root}"
  apply_fixture_mutation "${name}" "${fixture_root}"

  # A neutral CWD and empty inherited environment prevent the test from reading a
  # repository-local or user-local .env. The fixture root is the sole input tree.
  if (
    cd "${fixture_root}"
    env -i PATH="${PATH}" "${CHECKER_PATH}" --repo-root "${fixture_root}"
  ) >"${output_file}" 2>&1; then
    exit_code=0
  else
    exit_code=$?
  fi

  local output
  output="$(<"${output_file}")"

  if [[ "${output}" == *"${SYNTHETIC_SECRET}"* ]]; then
    fail "${name} leaked the synthetic secret"
  fi

  case "${expected_status}" in
    pass)
      if [[ "${exit_code}" -ne 0 ]]; then
        fail "${name} expected success, got exit ${exit_code}: ${output}"
      fi
      ;;
    fail)
      if [[ "${exit_code}" -eq 0 ]]; then
        fail "${name} expected failure category ${expected_category}"
      elif [[ "${output}" != *"${expected_category}"* ]]; then
        fail "${name} expected category ${expected_category}, got: ${output}"
      elif [[ "${output}" != *"${expected_path}"* ]]; then
        fail "${name} expected file path ${expected_path}, got: ${output}"
      fi
      ;;
    *)
      fail "${name} declared an unsupported expected status: ${expected_status}"
      ;;
  esac
}

run_case \
  'valid_minimum' \
  'pass' \
  '' \
  ''
run_case \
  'latest_image' \
  'fail' \
  'latest_image' \
  'deploy/observability/versions.env'
run_case \
  'missing_healthcheck' \
  'fail' \
  'missing_healthcheck' \
  'deploy/observability/compose.grafana.yaml'
run_case \
  'missing_resource_limit' \
  'fail' \
  'missing_resource_limit' \
  'deploy/observability/compose.grafana.yaml'
run_case \
  'host_port_conflict' \
  'fail' \
  'host_port_conflict' \
  'deploy/observability/compose.grafana.yaml'
run_case \
  'application_backend_endpoint' \
  'fail' \
  'application_backend_endpoint' \
  'manifest/config/config.yaml'
run_case \
  'legacy_tracing_dependency' \
  'fail' \
  'legacy_tracing_dependency' \
  'go.mod'
run_case \
  'legacy_jaeger_dependency' \
  'fail' \
  'legacy_tracing_dependency' \
  'go.mod'
run_case \
  'invalid_collector_pipeline' \
  'fail' \
  'invalid_collector_pipeline' \
  'deploy/observability/collector/collector-grafana.yaml'
run_case \
  'storage_path_unavailable' \
  'fail' \
  'storage_path_unavailable' \
  'deploy/observability/collector/collector-grafana.yaml'
run_case \
  'missing_compose_config' \
  'fail' \
  'missing_compose_config' \
  'deploy/observability'
run_case \
  'missing_collector_config' \
  'fail' \
  'missing_collector_config' \
  'deploy/observability/collector'
run_case \
  'storage_path_symlink' \
  'fail' \
  'storage_path_unavailable' \
  'deploy/observability/collector/collector-grafana.yaml'
run_case \
  'storage_runtime_user_unwritable' \
  'fail' \
  'storage_path_unavailable' \
  'deploy/observability/collector/collector-grafana.yaml'
run_case \
  'empty_collector_pipeline' \
  'fail' \
  'invalid_collector_pipeline' \
  'deploy/observability/collector/collector-grafana.yaml'
run_case \
  'named_storage_volume' \
  'pass' \
  '' \
  ''
run_case \
  'named_storage_volume_read_only' \
  'fail' \
  'storage_path_unavailable' \
  'deploy/observability/collector/collector-grafana.yaml'
run_case \
  'bind_storage_read_only' \
  'fail' \
  'storage_path_unavailable' \
  'deploy/observability/collector/collector-grafana.yaml'
run_case \
  'storage_mount_symlink' \
  'fail' \
  'storage_path_unavailable' \
  'deploy/observability/collector/collector-grafana.yaml'

if [[ "${failures}" -gt 0 ]]; then
  printf 'config_check_test: %d assertion(s) failed\n' "${failures}" >&2
  exit 1
fi

printf 'config_check_test: all assertions passed\n'
