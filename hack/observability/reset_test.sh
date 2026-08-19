#!/usr/bin/env bash
# T118 RED 契约测试：hack/observability/reset.sh 的安全清理行为。
#
# 覆盖的生产风险（FR-013 + data-model §14）：误删应用数据、全局 prune 清空
# 无关资源、无确认执行破坏性操作、被中断的 smoke 测试残留无人清理、输出
# 泄露凭据。契约固定：
#
# 1. 无确认拒绝：既没有 --dry-run 也没有 --confirm 时必须以稳定拒绝信息
#    退出（exit 2），且绝不调用 docker；
# 2. dry-run 预览：只发出只读 docker 调用（volume ls + compose ps），列出
#    将删除的卷、容器与 run 残留，不删除任何东西；
# 3. label scoping：volume 查询必须同时携带
#    `label=com.docker.compose.project=<project>` 与
#    `label=longtermism.observability=true` 过滤参数；确认删除只作用于查询
#    返回的名字集合；
# 4. 排除无关资源：应用 DB / 无关 volume 不在计划中；绝不出现 `prune`
#    子命令；compose down 不得携带 `-v`（防止删除项目中未计划的卷）；
# 5. 危险名称拒绝：包含 shell 元字符的 volume 名必须被拒绝（稳定类别
#    unsafe_resource_name，exit 2），不得拼进任何执行命令；
# 6. 中断残留清理：--run-root 下只删除 `run-*` 目录，保留无关文件与目录；
# 7. 输出卫生：reset.sh 不得转发 docker 原始输出，合成秘密字符串绝不出现
#    在 stdout/stderr 合并输出中。
#
# 注意：真实 compose 资产目前尚未声明 longtermism.observability 标签，
# 该标签属于 T128 落地 reset.sh 时同步补齐的部署契约（与 report 允许集
# 扩展同属 GREEN 批次的必然联动）。

set -u -o pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly RESET_PATH="${SCRIPT_DIR}/reset.sh"
readonly SYNTHETIC_SECRET="synthetic-secret-do-not-print"

temp_root="$(mktemp -d)"
trap 'rm -rf "${temp_root}"' EXIT

failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

# ---- fixtures -------------------------------------------------------------

FAKE_DOCKER="${temp_root}/fake-docker"
RECORD_FILE="${temp_root}/docker-calls.log"
RUN_ROOT="${temp_root}/run-root"
OUT_FILE="${temp_root}/output.log"

mkdir -p "${RUN_ROOT}/run-a" "${RUN_ROOT}/run-b" "${RUN_ROOT}/unrelated"
touch "${RUN_ROOT}/keep.txt"

write_fake_docker() {
  local path="$1"
  local mode="$2"
  cat >"${path}" <<EOF
#!/usr/bin/env bash
{
  printf 'CALL'
  printf ' <%s>' "\$@"
  printf '\n'
} >> "\${RECORD_FILE}"
printf 'fake docker stderr ${SYNTHETIC_SECRET}\n' >&2
sub="\$1"
case "\${sub}" in
  volume)
    op="\$2"
    if [[ "\${op}" == "ls" ]]; then
      printf 'obs-volume\n'
      if [[ "${mode}" == "with-appdb" ]]; then
        printf 'app-db-volume\n'
      fi
      if [[ "${mode}" == "malicious" ]]; then
        printf 'evil;rm -rf /tmp/pwned\n'
      fi
      exit 0
    fi
    if [[ "\${op}" == "rm" ]]; then
      printf 'removed %s\n' "\$3"
      exit 0
    fi
    ;;
  compose)
    if [[ "\$2" == "-p" && "\$4" == "ps" ]]; then
      printf 'otel-collector\nlangfuse-worker\n'
      exit 0
    fi
    if [[ "\$2" == "-p" && "\$4" == "down" ]]; then
      printf 'compose down recorded\n'
      exit 0
    fi
    ;;
esac
printf 'unexpected docker call\n' >&2
exit 98
EOF
  chmod +x "${path}"
}

write_fake_docker "${FAKE_DOCKER}" "clean"
export RECORD_FILE

run_reset() {
  # run_reset <expect_rc> [reset args...]
  local expect_rc="$1"
  shift
  : >"${RECORD_FILE}"
  "${RESET_PATH}" "$@" --docker-cmd "${FAKE_DOCKER}" >"${OUT_FILE}" 2>&1
  local actual_rc=$?
  if [[ "${actual_rc}" -ne "${expect_rc}" ]]; then
    fail "$1: exit code ${actual_rc}, want ${expect_rc}; output: $(head -c 300 "${OUT_FILE}")"
    return 1
  fi
  return 0
}

contains() { grep -qF -- "$1" "$2"; }
contains_output() { grep -qF -- "$1" "${OUT_FILE}"; }

# ---- cases ----------------------------------------------------------------

# RED 前置：reset.sh 必须存在且可执行。
if [[ ! -x "${RESET_PATH}" ]]; then
  fail "reset.sh missing or not executable at ${RESET_PATH}"
fi

# 无确认拒绝：既无 --dry-run 也无 --confirm → exit 2 + 稳定拒绝信息 + 零 docker 调用。
if run_reset 2 --project-name ltm-obs; then
  if ! grep -qi 'confirmation' "${OUT_FILE}"; then
    fail "refusal message must mention confirmation"
  fi
  if [[ -s "${RECORD_FILE}" ]]; then
    fail "no-confirmation run invoked docker: $(cat "${RECORD_FILE}")"
  fi
  if contains_output "${SYNTHETIC_SECRET}"; then
    fail "refusal output leaked the synthetic secret"
  fi
fi

# 缺失 --project-name → 参数错误。
if run_reset 2 --dry-run; then
  :
fi

# dry-run 预览：只读调用、完整计划、不删除任何东西、不含 secret。
rm -rf "${RUN_ROOT}/run-a" "${RUN_ROOT}/run-b"
mkdir -p "${RUN_ROOT}/run-a" "${RUN_ROOT}/run-b"
if run_reset 0 --project-name ltm-obs --dry-run --run-root "${RUN_ROOT}"; then
  contains_output 'obs-volume' || fail "dry-run must list the planned volume obs-volume"
  contains_output 'otel-collector' || fail "dry-run must list the project container otel-collector"
  contains_output 'run-a' || fail "dry-run must list interrupted run residue run-a"
  contains_output 'run-b' || fail "dry-run must list interrupted run residue run-b"

  contains '<volume> <ls>' "${RECORD_FILE}" || fail "dry-run must issue a read-only volume ls"
  contains '<compose> <-p> <ltm-obs> <ps>' "${RECORD_FILE}" || fail "dry-run must issue compose ps scoped to the project"
  contains '--filter' "${RECORD_FILE}" || fail "volume query must carry label filters"
  contains 'label=com.docker.compose.project=ltm-obs' "${RECORD_FILE}" || fail "volume query must filter by compose project label"
  contains 'label=longtermism.observability=true' "${RECORD_FILE}" || fail "volume query must filter by observability label"

  if contains '<volume> <rm>' "${RECORD_FILE}" || contains 'prune' "${RECORD_FILE}" || contains '<down>' "${RECORD_FILE}"; then
    fail "dry-run must not issue delete commands: $(cat "${RECORD_FILE}")"
  fi
  [[ -d "${RUN_ROOT}/run-a" ]] || fail "dry-run deleted run-a"
  [[ -d "${RUN_ROOT}/run-b" ]] || fail "dry-run deleted run-b"
  [[ -f "${RUN_ROOT}/keep.txt" ]] || fail "dry-run deleted unrelated keep.txt"
  if contains_output "${SYNTHETIC_SECRET}"; then
    fail "dry-run output leaked the synthetic secret"
  fi
fi

# 确认执行：精确删除计划资源、down 不带 -v、run 残留被清理、无关文件保留。
rm -rf "${RUN_ROOT}/run-a" "${RUN_ROOT}/run-b"
mkdir -p "${RUN_ROOT}/run-a" "${RUN_ROOT}/run-b"
if run_reset 0 --project-name ltm-obs --confirm --run-root "${RUN_ROOT}"; then
  [[ "$(grep -c '<volume> <rm> <obs-volume>' "${RECORD_FILE}")" -eq 1 ]] ||
    fail "confirm must remove exactly obs-volume once: $(cat "${RECORD_FILE}")"
  contains '<compose> <-p> <ltm-obs> <down>' "${RECORD_FILE}" || fail "confirm must stop the scoped compose project"
  if contains '<-v>' "${RECORD_FILE}" || contains '<-volumes>' "${RECORD_FILE}"; then
    fail "compose down must not carry -v: $(cat "${RECORD_FILE}")"
  fi
  if contains 'prune' "${RECORD_FILE}"; then
    fail "global prune must never run: $(cat "${RECORD_FILE}")"
  fi
  if contains 'app-db-volume' "${RECORD_FILE}"; then
    fail "application database volume must never be touched"
  fi
  [[ ! -e "${RUN_ROOT}/run-a" ]] || fail "confirm did not clean interrupted run-a"
  [[ ! -e "${RUN_ROOT}/run-b" ]] || fail "confirm did not clean interrupted run-b"
  [[ -f "${RUN_ROOT}/keep.txt" ]] || fail "confirm deleted unrelated keep.txt"
  [[ -d "${RUN_ROOT}/unrelated" ]] || fail "confirm deleted unrelated directory"
  if contains_output "${SYNTHETIC_SECRET}"; then
    fail "confirm output leaked the synthetic secret"
  fi
fi

# 危险卷名拒绝：shell 元字符卷名 → exit 2 + 稳定类别 + 零删除调用。
write_fake_docker "${temp_root}/fake-docker-malicious" "malicious"
export RECORD_FILE
: >"${RECORD_FILE}"
"${RESET_PATH}" --project-name ltm-obs --confirm --docker-cmd "${temp_root}/fake-docker-malicious" >"${OUT_FILE}" 2>&1
malicious_rc=$?
if [[ "${malicious_rc}" -ne 2 ]]; then
  fail "malicious volume name: exit code ${malicious_rc}, want 2"
else
  contains_output 'unsafe_resource_name' || fail "malicious volume name must yield stable error class unsafe_resource_name"
  if contains '<volume> <rm>' "${RECORD_FILE}"; then
    fail "malicious volume name reached a delete command: $(cat "${RECORD_FILE}")"
  fi
fi
write_fake_docker "${FAKE_DOCKER}" "clean"

if [[ "${failures}" -gt 0 ]]; then
  printf 'reset_test: %d assertion(s) failed\n' "${failures}" >&2
  exit 1
fi

printf 'reset_test: all assertions passed\n'
