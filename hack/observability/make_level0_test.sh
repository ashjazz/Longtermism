#!/usr/bin/env bash
# Level 0 Make 门禁契约：默认目标不得依赖 Docker 或真实凭据。
# 该脚本以最小环境执行目标，避免开发者本机 .env、shell export 或 Docker 状态掩盖依赖。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly TEST_TMPDIR="$(mktemp -d)"
readonly MODULE_CACHE="$(go env GOMODCACHE)"

cleanup() {
  rm -rf "${TEST_TMPDIR}"
}
trap cleanup EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

run_isolated_target() {
  local target="$1"
  local expected_exit="$2"
  local output_file="${TEST_TMPDIR}/${target}.log"

  set +e
  env -i \
    PATH="${PATH}" \
    HOME="${TEST_TMPDIR}/home" \
    GOCACHE="${TEST_TMPDIR}/go-cache" \
    GOMODCACHE="${MODULE_CACHE}" \
    make -C "${REPO_ROOT}" "${target}" >"${output_file}" 2>&1
  local actual_exit=$?
  set -e

  case "${expected_exit}" in
    zero)
      [[ "${actual_exit}" -eq 0 ]] || {
        cat "${output_file}" >&2
        fail "${target} exited ${actual_exit}, want 0"
      }
      ;;
    nonzero)
      [[ "${actual_exit}" -ne 0 ]] || fail "${target} unexpectedly succeeded"
      ;;
    *)
      fail "unsupported expected exit ${expected_exit}"
      ;;
  esac

  if rg -n -i 'docker' "${output_file}"; then
    fail "${target} invoked Docker"
  fi
}

for target in verify obs-contract obs-smoke-offline obs-config-check; do
  make -C "${REPO_ROOT}" -n "${target}" >"${TEST_TMPDIR}/${target}.dry-run" 2>&1 || {
    cat "${TEST_TMPDIR}/${target}.dry-run" >&2
    fail "${target} is not a Make target"
  }
  if rg -n -i 'docker' "${TEST_TMPDIR}/${target}.dry-run"; then
    fail "${target} dry-run contains Docker"
  fi
done

run_isolated_target verify zero
run_isolated_target obs-contract zero
run_isolated_target obs-smoke-offline zero
# 部署资产在 T051/T052 前尚不存在；静态 checker 应明确拒绝而不能假通过。
run_isolated_target obs-config-check nonzero

printf 'PASS: Level 0 Make targets are offline and fail closed\n'
