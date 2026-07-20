#!/usr/bin/env bash
# T066：用 fake docker/go 验证 Make 编排，而不是在单测中启动昂贵且有状态的后端。
# 关键生产风险是失败路径跳过 compose cleanup 或用 `down -v` 删除低敏 smoke 证据。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

setup_fake_tools() {
  local directory="$1"
  cat >"${directory}/docker" <<'EOF'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"${FAKE_COMMAND_LOG}"
exit 0
EOF
  cat >"${directory}/go" <<'EOF'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"${FAKE_COMMAND_LOG}"
if [[ "${FAKE_GO_EXIT:-0}" != "0" ]]; then
  exit "${FAKE_GO_EXIT}"
fi
EOF
  chmod +x "${directory}/docker" "${directory}/go"
}

assert_contains_in_order() {
  local output="$1"
  shift
  local previous=0
  local expected line
  for expected in "$@"; do
    line="$(printf '%s\n' "${output}" | grep -n -F "${expected}" | head -n 1 | cut -d: -f1 || true)"
    [[ -n "${line}" && "${line}" -gt "${previous}" ]] || {
      printf 'missing or out-of-order command: %s\n%s\n' "${expected}" "${output}" >&2
      exit 1
    }
    previous="${line}"
  done
}

test_e2e_success_and_idempotent_lifecycle() {
  local temporary log output status
  temporary="$(mktemp -d)"
  log="${temporary}/commands.log"
  setup_fake_tools "${temporary}"
  mkdir -p "${temporary}/reports"

  set +e
  output="$(cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" make obs-grafana-e2e 2>&1)"
  status=$?
  set -e
  [[ "${status}" -eq 0 ]] || { printf 'e2e success path failed\n%s\n' "${output}" >&2; exit 1; }
  assert_contains_in_order "$(cat "${log}")" \
    "docker compose --env-file deploy/observability/versions.env -f deploy/observability/compose.grafana.yaml up -d --wait --wait-timeout 180" \
    "docker compose --env-file deploy/observability/versions.env -f deploy/observability/compose.grafana.yaml ps" \
    "go run ./cmd/obs-smoke infra" \
    "docker compose --env-file deploy/observability/versions.env -f deploy/observability/compose.grafana.yaml down"
  [[ "${output}" != *"down -v"* ]] || { printf 'e2e output used destructive volume cleanup\n' >&2; exit 1; }

  : >"${log}"
  (cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" make obs-grafana-up)
  (cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" make obs-grafana-up)
  (cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" make obs-grafana-down)
  local lifecycle
  lifecycle="$(cat "${log}")"
  [[ "$(printf '%s\n' "${lifecycle}" | grep -c ' up -d --wait --wait-timeout 180$')" == "2" ]] || { printf 'up target was not repeatable\n%s\n' "${lifecycle}" >&2; exit 1; }
  [[ "${lifecycle}" == *" down"* && "${lifecycle}" != *"down -v"* ]] || { printf 'down target lost persistent volumes\n%s\n' "${lifecycle}" >&2; exit 1; }
  rm -rf "${temporary}"
}

test_e2e_failure_still_cleans_up_and_preserves_reports() {
  local temporary log report output status
  temporary="$(mktemp -d)"
  log="${temporary}/commands.log"
  report="${REPO_ROOT}/build/observability/smoke-reports/t066-retained-test-report.json"
  setup_fake_tools "${temporary}"
  mkdir -p "$(dirname "${report}")"
  printf '{}\n' >"${report}"

  set +e
  output="$(cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" FAKE_GO_EXIT=1 make obs-grafana-e2e 2>&1)"
  status=$?
  set -e
  [[ "${status}" -ne 0 ]] || { printf 'e2e succeeded despite smoke failure\n' >&2; exit 1; }
  [[ -f "${report}" ]] || { printf 'e2e cleanup removed smoke report\n' >&2; exit 1; }
  [[ "$(cat "${log}")" == *"go run ./cmd/obs-smoke infra"* ]] || { printf 'infra smoke was not invoked\n%s\n' "$(cat "${log}")" >&2; exit 1; }
  [[ "$(cat "${log}")" == *" down"* ]] || { printf 'cleanup did not run after smoke failure\n%s\n%s\n' "${output}" "$(cat "${log}")" >&2; exit 1; }
  rm -f "${report}"
  rmdir "$(dirname "${report}")" 2>/dev/null || true
  rm -rf "${temporary}"
}

test_e2e_success_and_idempotent_lifecycle
test_e2e_failure_still_cleans_up_and_preserves_reports
printf '%s\n' 'make_grafana_e2e_test: pass'
