#!/usr/bin/env bash
# T118 契约的 GREEN 实现：label-scoped 安全 reset（FR-013）。
#
# 安全边界：
# 1. 无 --dry-run/--confirm 时拒绝执行（exit 2），绝不调用 docker；
# 2. dry-run 只发出只读调用（volume ls + compose ps），列出将删除的卷、
#    容器与 run 残留，不删除任何东西；
# 3. 卷查询必须同时携带 project label 与 longtermism.observability=true
#    过滤参数；确认删除只作用于查询返回且通过安全名称校验的名字集合；
# 4. 绝不使用 docker volume/system prune；compose down 不携带 -v
#    （防止删除项目中未计划的卷）；应用 DB / 无关卷不在计划中；
# 5. 包含 shell 元字符的卷名被拒绝（稳定类别 unsafe_resource_name），
#    拒绝发生在任何删除命令之前；
# 6. --run-root 下只清理 run-* 目录，无关文件与目录保留；
# 7. docker 原始 stderr 一律不转发（防 credential/secret 泄漏），
#    docker 失败映射为稳定 docker_error 类别。
set -u -o pipefail

project_name=""
run_root=""
docker_cmd="docker"
mode=""

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --project-name)
      [[ "$#" -ge 2 ]] || { printf '%s\n' 'argument_error: --project-name requires a value' >&2; exit 2; }
      project_name="$2"
      shift 2
      ;;
    --run-root)
      [[ "$#" -ge 2 ]] || { printf '%s\n' 'argument_error: --run-root requires a value' >&2; exit 2; }
      run_root="$2"
      shift 2
      ;;
    --docker-cmd)
      [[ "$#" -ge 2 ]] || { printf '%s\n' 'argument_error: --docker-cmd requires a value' >&2; exit 2; }
      docker_cmd="$2"
      shift 2
      ;;
    --dry-run) mode="dry-run"; shift ;;
    --confirm) mode="confirm"; shift ;;
    *)
      printf '%s\n' 'argument_error: unsupported argument' >&2
      exit 2
      ;;
  esac
done

if [[ -z "${mode}" ]]; then
  printf '%s\n' 'refused: reset requires explicit confirmation (--confirm) or a dry-run preview (--dry-run)' >&2
  exit 2
fi

if [[ -z "${project_name}" ]]; then
  printf '%s\n' 'argument_error: --project-name is required' >&2
  exit 2
fi

# 安全名称边界：与 failure 包 DockerControl 同一类校验。project 名在
# 进入任何 docker 调用之前拒绝。
if ! [[ "${project_name}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]]; then
  printf '%s\n' "unsafe_resource_name: ${project_name}" >&2
  exit 2
fi

safe_name() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]]
}

# 只读查询：label 过滤是作用域的核心（仅当前 project + observability 卷）。
volumes="$( "${docker_cmd}" volume ls --format '{{.Name}}' \
  --filter "label=com.docker.compose.project=${project_name}" \
  --filter 'label=longtermism.observability=true' 2>/dev/null )" || {
  printf '%s\n' 'docker_error: volume listing failed' >&2
  exit 1
}

containers="$( "${docker_cmd}" compose -p "${project_name}" ps -a --format '{{.Name}}' 2>/dev/null )" || {
  printf '%s\n' 'docker_error: compose ps failed' >&2
  exit 1
}

# 危险名称校验必须在任何打印与删除之前完成：包含 shell 元字符的卷名
# 拒绝整个操作（exit 2），而不是跳过该卷继续。
for name in ${volumes}; do
  if ! safe_name "${name}"; then
    printf '%s\n' "unsafe_resource_name: ${name}" >&2
    exit 2
  fi
done

printf '%s\n' 'planned volumes:'
printf '%s\n' ${volumes}
printf '%s\n' 'planned containers:'
printf '%s\n' ${containers}

if [[ -n "${run_root}" ]]; then
  printf '%s\n' 'planned run residue:'
  for entry in "${run_root}"/run-*; do
    [[ -e "${entry}" ]] || continue
    printf '%s\n' "${entry}"
  done
fi

if [[ "${mode}" == "dry-run" ]]; then
  exit 0
fi

# 确认模式：只删除计划内的卷（单 argv 传递），随后 scoped compose down
#（不带 -v），最后清理 run-* 残留。
for name in ${volumes}; do
  "${docker_cmd}" volume rm "${name}" >/dev/null 2>/dev/null || {
    printf '%s\n' "docker_error: failed to remove volume ${name}" >&2
    exit 1
  }
done

"${docker_cmd}" compose -p "${project_name}" down >/dev/null 2>/dev/null || {
  printf '%s\n' 'docker_error: compose down failed' >&2
  exit 1
}

if [[ -n "${run_root}" ]]; then
  for entry in "${run_root}"/run-*; do
    [[ -e "${entry}" ]] || continue
    rm -rf -- "${entry}"
  done
fi

printf '%s\n' 'reset completed'
