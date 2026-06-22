#!/usr/bin/env bash
set -euo pipefail

json=false
branch_name=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --json)
      json=true
      shift
      ;;
    --branch)
      branch_name="${2:-}"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
feature_json="$root/.specify/feature.json"

if [[ ! -d "$root/.git" ]]; then
  echo "git repository not found: $root" >&2
  exit 1
fi

if [[ -z "$branch_name" ]]; then
  if [[ ! -f "$feature_json" ]]; then
    echo "missing .specify/feature.json" >&2
    exit 1
  fi

  feature_dir="$(sed -n 's/.*"feature_directory"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$feature_json" | head -1)"
  if [[ -z "$feature_dir" ]]; then
    echo "feature_directory not found in .specify/feature.json" >&2
    exit 1
  fi

  branch_name="$(basename "$feature_dir")"
fi

if [[ ! "$branch_name" =~ ^[0-9]{3}-[a-z0-9][a-z0-9-]*$ ]]; then
  echo "invalid spec-kit branch name: $branch_name" >&2
  echo "expected format: NNN-short-name, for example 001-agent-framework-spec" >&2
  exit 1
fi

current="$(git -C "$root" symbolic-ref --short HEAD 2>/dev/null || true)"

if [[ "$current" != "$branch_name" ]]; then
  if git -C "$root" show-ref --verify --quiet "refs/heads/$branch_name"; then
    git -C "$root" switch "$branch_name"
  else
    git -C "$root" switch -c "$branch_name"
  fi
fi

if [[ "$json" == true ]]; then
  printf '{"BRANCH_NAME":"%s","FEATURE_NUM":"%s"}\n' "$branch_name" "${branch_name%%-*}"
else
  printf 'BRANCH_NAME=%s\nFEATURE_NUM=%s\n' "$branch_name" "${branch_name%%-*}"
fi
