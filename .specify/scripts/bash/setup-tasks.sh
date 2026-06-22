#!/usr/bin/env bash
set -euo pipefail

json=false
if [[ "${1:-}" == "--json" ]]; then
  json=true
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
feature_json="$root/.specify/feature.json"
template="$root/.specify/templates/tasks-template.md"

if [[ ! -f "$feature_json" ]]; then
  echo "missing .specify/feature.json" >&2
  exit 1
fi

feature_dir_rel="$(sed -n 's/.*"feature_directory"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$feature_json" | head -1)"
if [[ -z "$feature_dir_rel" ]]; then
  echo "feature_directory not found in .specify/feature.json" >&2
  exit 1
fi

feature_dir="$root/$feature_dir_rel"
if [[ ! -d "$feature_dir" ]]; then
  echo "feature directory not found: $feature_dir" >&2
  exit 1
fi

available_docs=""
while IFS= read -r file; do
  rel="${file#$feature_dir/}"
  available_docs="${available_docs}${rel},"
done < <(find "$feature_dir" -maxdepth 3 -type f \
  \( -name 'spec.md' -o -name 'plan.md' -o -name 'research.md' -o -name 'data-model.md' -o -name 'quickstart.md' -o -path '*/contracts/*' \) | sort)
available_docs="${available_docs%,}"

if [[ "$json" == true ]]; then
  printf '{"FEATURE_DIR":"%s","TASKS_TEMPLATE":"%s","AVAILABLE_DOCS":"%s"}\n' "$feature_dir" "$template" "$available_docs"
else
  printf 'FEATURE_DIR=%s\nTASKS_TEMPLATE=%s\nAVAILABLE_DOCS=%s\n' "$feature_dir" "$template" "$available_docs"
fi
