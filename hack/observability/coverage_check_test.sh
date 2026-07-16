#!/usr/bin/env bash
# 覆盖率门禁的离线契约测试：临时仓库证明 profile、diff 与 merge-base 必须来自同一
# 可审计边界，缺少生产包 profile 不能被当成“没有可执行行”而假通过。
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly CHECKER_PATH="${SCRIPT_DIR}/coverage_check.sh"

temp_root="$(mktemp -d)"
trap 'rm -rf "${temp_root}"' EXIT

failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

write_repo() {
  local root="$1"
  mkdir -p "${root}/internal/observability"
  git -C "${root}" init -q
  git -C "${root}" config user.email "coverage-test@example.test"
  git -C "${root}" config user.name "coverage-test"
  printf 'module example.test/coverage\n\ngo 1.24.0\n' >"${root}/go.mod"
  printf 'package observability\n\nfunc Value() int { return 1 }\n' >"${root}/internal/observability/value.go"
  git -C "${root}" add .
  git -C "${root}" commit -qm baseline
  git -C "${root}" branch -M main
  git -C "${root}" checkout -qb coverage-feature
  printf 'package observability\n\nfunc Value() int { return 2 }\n' >"${root}/internal/observability/value.go"
  git -C "${root}" add .
  git -C "${root}" commit -qm changed
}

write_profile() {
  local path="$1"
  local count="$2"
  printf 'mode: atomic\nexample.test/coverage/internal/observability/value.go:3.20,3.28 1 %s\n' "${count}" >"${path}"
}

run_case() {
  local name="$1"
  local want="$2"
  local profile_count="$3"
  local omit_profile="$4"
  local root="${temp_root}/${name}"
  local profile="${root}/coverage.out"
  local output
  local status

  write_repo "${root}"
  if [[ "${omit_profile}" != "true" ]]; then
    write_profile "${profile}" "${profile_count}"
  else
    printf 'mode: atomic\n' >"${profile}"
  fi
  if [[ "${name}" == "ignored_untracked_production_file" ]]; then
    printf 'internal/observability/ignored.go\n' >>"${root}/.gitignore"
    printf 'package observability\n\nfunc Ignored() int { return 3 }\n' >"${root}/internal/observability/ignored.go"
  fi

  set +e
  output="$(cd "${root}" && bash "${CHECKER_PATH}" --profile "${profile}" --base main --threshold 80 --scope internal/observability 2>&1)"
  status=$?
  set -e

  if [[ "${want}" == "pass" && ${status} -ne 0 ]]; then
    fail "${name} unexpectedly failed: ${output}"
  elif [[ "${want}" == "fail" && ${status} -eq 0 ]]; then
    fail "${name} unexpectedly passed"
  fi
}

run_case covered_change pass 1 false
run_case uncovered_change fail 0 false
run_case missing_package_profile fail 1 true
run_case ignored_untracked_production_file fail 1 false

if [[ ${failures} -gt 0 ]]; then
  printf 'coverage_check_test: %d assertion(s) failed\n' "${failures}" >&2
  exit 1
fi

printf 'coverage_check_test: all assertions passed\n'
