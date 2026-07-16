#!/usr/bin/env bash
# 解析调用方刚刚生成的 atomic coverprofile，并对 merge-base 后的核心 Go 行执行门禁。
set -euo pipefail

profile=""
base_ref="origin/main"
threshold="80"
scopes=()

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --profile) profile="$2"; shift 2 ;;
    --base) base_ref="$2"; shift 2 ;;
    --threshold) threshold="$2"; shift 2 ;;
    --scope) scopes+=("$2"); shift 2 ;;
    *) printf 'coverage_check: unsupported argument\n' >&2; exit 2 ;;
  esac
done

if [[ -z "${profile}" || ${#scopes[@]} -eq 0 || ! -r "${profile}" ]]; then
  printf 'coverage_check: readable profile and at least one scope are required\n' >&2
  exit 2
fi

module_name="$(awk '$1 == "module" { print $2; exit }' go.mod)"
if [[ -z "${module_name}" ]]; then
  printf 'coverage_check: go.mod module is required\n' >&2
  exit 2
fi

# 唯一允许从分母移除的 Go 文件类型。config-only 文件不在 Go scope 内；不能通过目录
# 名或宽泛的 "generated" 子串排除业务代码，避免开发者用命名绕过覆盖率门禁。
is_allowed_generated_or_test_file() {
  case "$(basename "$1")" in
    *_test.go|*.pb.go|*_grpc.pb.go|*.gen.go|*_mock.go|zz_generated*.go|mock_*.go) return 0 ;;
    *) return 1 ;;
  esac
}

empty_tree="$(git hash-object -t tree /dev/null)"
if git rev-parse --verify --quiet "${base_ref}^{commit}" >/dev/null; then
  merge_base="$(git merge-base "${base_ref}" HEAD)"
else
  merge_base="${empty_tree}"
fi

diff_file="$(mktemp)"
trap 'rm -f "${diff_file}"' EXIT
# 与 merge-base 比较工作树而非仅 HEAD：本地提交前也必须检查 staged/unstaged 的
# 项目源码改动。未忽略的临时 Go 源码仍须进入门禁；只有 Git 明确忽略的本地学习/实验
# 文件不参与统计，避免污染项目质量信号。
git diff --unified=0 --no-color "${merge_base}" -- "${scopes[@]}" >"${diff_file}"
while IFS= read -r file; do
  case "${file}" in
    internal/cmd/*.go|internal/observability/*.go|internal/logic/chat/*.go|pkg/ai/obs/*.go)
      git diff --unified=0 --no-color --no-index /dev/null "${file}" >>"${diff_file}" || true
      ;;
  esac
done < <(git ls-files --others --exclude-standard)

result="$(awk -v module_prefix="${module_name}/" -v threshold="${threshold}" '
function excluded(path, base) {
  base=path; sub(/^.*\//, "", base)
  return path !~ /\.go$/ || path ~ /(_test|\.pb|_grpc\.pb|\.gen|_mock)\.go$/ || base ~ /^(zz_generated|mock_)/
}
FILENAME == ARGV[1] {
  if ($1 == "mode:") next
  split($1, location, ":"); file=location[1]; sub(module_prefix, "", file)
  split(location[2], span, ","); split(span[1], start_part, "."); split(span[2], end_part, ".")
  count=$3 + 0; n[file]++; start[file,n[file]]=start_part[1]; finish[file,n[file]]=end_part[1]; hit[file,n[file]]=(count > 0)
  seen[file]=1; next
}
/^\+\+\+ b\// { file=$0; sub(/^\+\+\+ b\//, "", file); next }
/^\+\+\+ \/dev\/null/ { file=""; next }
/^@@/ { if (match($0, /\+[0-9]+/)) { new_line=substr($0, RSTART+1, RLENGTH-1) + 0 }; next }
/^\+[^+]/ {
  if (file != "" && !excluded(file)) {
    if (!seen[file]) { missing[file]=1 } else {
      statement=0; covered=0
      for (i=1; i<=n[file]; i++) if (new_line >= start[file,i] && new_line <= finish[file,i]) { statement=1; if (hit[file,i]) covered=1 }
      if (statement) { total++; if (covered) hits++; else missed=missed file ":" new_line "," }
    }
  }
  new_line++; next
}
/^-[^-]/ || /^\\/ { next }
{ new_line++ }
END {
  for (file in missing) missing_text=missing_text file ","
  if (missing_text != "") { print "missing=" missing_text; exit 2 }
  percent=(total == 0 ? 100 : 100 * hits / total)
  printf "summary=%d/%d %.1f\n", hits, total, percent
  if (percent < threshold) { print "missed=" missed; exit 1 }
}' "${profile}" "${diff_file}")" || status=$?
status="${status:-0}"

printf 'coverage_check: %s\n' "${result}"
if [[ ${status} -eq 2 ]]; then
  printf 'coverage_check: changed production files missing from profile\n' >&2
  exit 2
fi
if [[ ${status} -ne 0 ]]; then
  printf 'coverage_check: changed core lines did not meet threshold\n' >&2
  exit 1
fi

chat_files=()
if [[ -d internal/logic/chat ]]; then
  while IFS= read -r file; do
    is_allowed_generated_or_test_file "${file}" || chat_files+=("${file}")
  done < <(find internal/logic/chat -name '*.go' -type f | sort)
fi
if [[ ${#chat_files[@]} -gt 0 ]]; then
  for file in "${chat_files[@]}"; do
    if ! grep -Fq "${module_name}/${file}:" "${profile}"; then
      printf 'coverage_check: chat files missing from profile\n' >&2
      exit 2
    fi
  done
  chat_result="$(awk -v module_prefix="${module_name}/" '
    $1 != "mode:" {
      split($1, location, ":"); file=location[1]; sub(module_prefix, "", file)
      if (file ~ /^internal\/logic\/chat\//) {
        split(location[2], span, ","); split(span[1], start_part, "."); split(span[2], end_part, ".")
        lines=end_part[1] - start_part[1] + 1; total += lines; if ($3 > 0) covered += lines
      }
    }
    END { if (total == 0) exit 2; printf "%.1f", 100 * covered / total }
  ' "${profile}")" || {
    printf 'coverage_check: chat files missing from profile\n' >&2
    exit 2
  }
  awk -v value="${chat_result}" 'BEGIN { exit(value + 0 < 90) }' || {
    printf 'coverage_check: chat usecase coverage %s%% is below 90%%\n' "${chat_result}" >&2
    exit 1
  }
  printf 'coverage_check: chat usecase coverage %s%%\n' "${chat_result}"
fi
