#!/usr/bin/env bash
set -euo pipefail

json=false
if [[ "${1:-}" == "--json" ]]; then
  json=true
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
feature_json="$root/.specify/feature.json"

if [[ ! -f "$feature_json" ]]; then
  echo "missing .specify/feature.json" >&2
  exit 1
fi

feature_dir="$(sed -n 's/.*"feature_directory"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$feature_json" | head -1)"
if [[ -z "$feature_dir" ]]; then
  echo "feature_directory not found in .specify/feature.json" >&2
  exit 1
fi

specs_dir="$root/$feature_dir"
feature_spec="$specs_dir/spec.md"
impl_plan="$specs_dir/plan.md"
template="$root/.specify/templates/plan-template.md"

mkdir -p "$specs_dir/contracts"

if [[ ! -f "$feature_spec" ]]; then
  echo "feature spec not found: $feature_spec" >&2
  exit 1
fi

if [[ ! -f "$impl_plan" ]]; then
  cp "$template" "$impl_plan"
fi

branch="no-git"
if git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  branch="$(git -C "$root" branch --show-current 2>/dev/null || true)"
  if [[ -z "$branch" ]]; then
    branch="detached"
  fi
fi

if [[ "$json" == true ]]; then
  printf '{"FEATURE_SPEC":"%s","IMPL_PLAN":"%s","SPECS_DIR":"%s","BRANCH":"%s"}\n' "$feature_spec" "$impl_plan" "$specs_dir" "$branch"
else
  printf 'FEATURE_SPEC=%s\nIMPL_PLAN=%s\nSPECS_DIR=%s\nBRANCH=%s\n' "$feature_spec" "$impl_plan" "$specs_dir" "$branch"
fi
