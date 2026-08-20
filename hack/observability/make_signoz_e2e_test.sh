#!/usr/bin/env bash
# T148：用 fake docker/go 验证 SigNoz 备选 profile 的 Make 编排，不启动任何后端。
# 关键生产风险：失败路径跳过 compose cleanup、cleanup 误删低敏证据（down -v）、
# 或清理越界触碰 Grafana 主线 project。
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

assert_contains() {
  local output="$1" expected="$2"
  if ! printf '%s\n' "${output}" | grep -q -F -e "${expected}"; then
    printf 'missing command fragment: %s\n%s\n' "${expected}" "${output}" >&2
    exit 1
  fi
}

refuse_contains() {
  local output="$1" forbidden="$2"
  if printf '%s\n' "${output}" | grep -q -F -e "${forbidden}"; then
    printf 'forbidden command fragment present: %s\n%s\n' "${forbidden}" "${output}" >&2
    exit 1
  fi
}

set_synthetic_signoz_environment() {
  export LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL="http://127.0.0.1:3301"
  export LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL="http://127.0.0.1:3001"
  export LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL="test-langfuse-credential"
  export LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL="http://127.0.0.1:8000"
  export LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL="test-ai-plane-credential"
  export LONGTERMISM_SMOKE_APP_BASE_URL="http://127.0.0.1:8000"
  export LONGTERMISM_SMOKE_CHAT_AUTHORIZATION="test-chat-authorization"
  export LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT="$(mktemp -d)"
}

test_signoz_up_uses_dedicated_project() {
  local temporary log output status
  temporary="$(mktemp -d)"
  log="${temporary}/commands.log"
  setup_fake_tools "${temporary}"
  set_synthetic_signoz_environment

  set +e
  output="$(cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" make OBSERVABILITY_LOCAL_ENV_FILE="${temporary}/missing.env" obs-signoz-up 2>&1)"
  status=$?
  set -e
  [[ "${status}" -eq 0 ]] || { printf 'obs-signoz-up failed: %s\n' "${output}" >&2; exit 1; }
  assert_contains "${output}" "--project-name longtermism-signoz"
  assert_contains "${output}" "-f deploy/observability/compose.langfuse.yaml"
  assert_contains "${output}" "-f deploy/observability/compose.signoz.yaml"
  assert_contains "${output}" "up -d --wait"
}

test_signoz_e2e_failure_cleanup_is_scoped() {
  local temporary log output status commands
  temporary="$(mktemp -d)"
  log="${temporary}/commands.log"
  setup_fake_tools "${temporary}"
  set_synthetic_signoz_environment

  # infra smoke 失败（fake go 退出 1）：trap 必须执行 down 清理，且清理只针对
  # signoz project。trap 里的 compose 命令不经过 make 回显，证据在 fake 工具日志。
  set +e
  output="$(cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" FAKE_GO_EXIT=1 make OBSERVABILITY_LOCAL_ENV_FILE="${temporary}/missing.env" obs-signoz-e2e 2>&1)"
  status=$?
  set -e
  [[ "${status}" -ne 0 ]] || { printf 'obs-signoz-e2e unexpectedly passed\n' "${output}" >&2; exit 1; }
  commands="$(<"${log}")"
  assert_contains "${commands}" "--project-name longtermism-signoz"
  assert_contains "${commands}" "deploy/observability/compose.signoz.yaml down"
  refuse_contains "${commands}" "down -v"
  refuse_contains "${commands}" "--project-name longtermism-observability"
  assert_contains "${output}" "LONGTERMISM_SMOKE_PROFILE=signoz go run ./cmd/obs-smoke infra --profile=signoz"
}

test_signoz_e2e_success_runs_both_signoz_smokes() {
  local temporary log output status commands
  temporary="$(mktemp -d)"
  log="${temporary}/commands.log"
  setup_fake_tools "${temporary}"
  set_synthetic_signoz_environment

  set +e
  output="$(cd "${REPO_ROOT}" && PATH="${temporary}:${PATH}" FAKE_COMMAND_LOG="${log}" make OBSERVABILITY_LOCAL_ENV_FILE="${temporary}/missing.env" obs-signoz-e2e 2>&1)"
  status=$?
  set -e
  [[ "${status}" -eq 0 ]] || { printf 'obs-signoz-e2e failed: %s\n' "${output}" >&2; exit 1; }
  # 成功路径同样以 down 收尾（compose 栈不留运行），且两个 smoke 都以 signoz
  # profile 调用（CLI 与部署声明双向一致）。
  commands="$(<"${log}")"
  assert_contains "${commands}" "deploy/observability/compose.signoz.yaml down"
  assert_contains "${output}" "LONGTERMISM_SMOKE_PROFILE=signoz go run ./cmd/obs-smoke infra --profile=signoz"
  assert_contains "${output}" "LONGTERMISM_SMOKE_PROFILE=signoz go run ./cmd/obs-smoke chat --live --profile=signoz"
  refuse_contains "${commands}" "down -v"
}

test_signoz_preflight_names_missing_references() {
  local temporary log output status
  temporary="$(mktemp -d)"
  log="${temporary}/commands.log"
  setup_fake_tools "${temporary}"
  # 不设置任何 smoke env：预检必须 fail closed 并只打印变量名。
  env -i PATH="${temporary}:${PATH}" HOME="${HOME}" FAKE_COMMAND_LOG="${log}" \
    bash -c '
      cd "'"${REPO_ROOT}"'" && make OBSERVABILITY_LOCAL_ENV_FILE="'"${temporary}"'/missing.env" obs-signoz-infra-smoke
    ' >"${temporary}/output.txt" 2>&1 || true
  set -e
  output="$(<"${temporary}/output.txt")"
  assert_contains "${output}" "missing required environment variables"
  assert_contains "${output}" "LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL"
  # 预检失败不得调用后端或 go runner。
  if [[ -s "${log}" ]]; then
    printf 'preflight failure must not invoke tools: %s\n' "$(cat "${log}")" >&2
    exit 1
  fi
}

test_signoz_up_uses_dedicated_project
test_signoz_e2e_failure_cleanup_is_scoped
test_signoz_e2e_success_runs_both_signoz_smokes
test_signoz_preflight_names_missing_references
printf 'make_signoz_e2e_test: all assertions passed\n'
