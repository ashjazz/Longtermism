#!/usr/bin/env bash
set -eu

# 所有检查都发生在 Docker/Go/网络之前。这里只输出变量名和稳定错误类别，绝不
# 回显 endpoint、credential、路径或 project 原值。
mode=${1:-}
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)

fail() {
  printf 'observability live preflight: %s\n' "$1" >&2
  exit 2
}

validate_project() {
  local project=$1
  case "$project" in
    ''|[!a-z0-9]*|*[!a-z0-9_-]*) fail 'invalid compose project configuration' ;;
  esac
  [ "${#project}" -le 63 ] || fail 'invalid compose project configuration'
}

validate_optional_env_file() {
  local path=$1 current component resolved metadata owner mode permissions
  case "$path" in
    ''|*[!A-Za-z0-9_./-]*) fail 'invalid local env file configuration' ;;
  esac
  # 不存在的可选文件不会进入 Compose argv；存在时必须是仓库内无 symlink 的普通文件。
  if [ ! -e "$path" ] && [ ! -L "$path" ]; then
    return
  fi
  case "$path" in
    /*) current='' ;;
    *) current=$repo_root ;;
  esac
  IFS='/' read -r -a components <<< "$path"
  for component in "${components[@]}"; do
    case "$component" in
      ''|.) continue ;;
      ..) fail 'invalid local env file configuration' ;;
    esac
    current="$current/$component"
    [ ! -L "$current" ] || fail 'invalid local env file configuration'
  done
  [ -f "$current" ] || fail 'invalid local env file configuration'
  resolved=$(cd "$(dirname "$current")" && pwd -P)/$(basename "$current")
  case "$resolved" in
    "$repo_root"/*) ;;
    *) fail 'invalid local env file configuration' ;;
  esac
  if metadata=$(stat -f '%u %Lp' "$current" 2>/dev/null); then
    :
  elif metadata=$(stat -c '%u %a' "$current" 2>/dev/null); then
    :
  else
    fail 'invalid local env file configuration'
  fi
  read -r owner mode <<< "$metadata"
  [ "$owner" = "$(id -u)" ] || fail 'invalid local env file ownership'
  permissions=$((8#$mode))
  (( (permissions & 077) == 0 )) || fail 'insecure local env file permissions'
}

validate_artifact_paths() {
  local spec kind name path current component parent
  for spec in "$@"; do
    kind=${spec%%:*}
    name=${spec#*:}
    path=${!name}
    case "$path" in
      /*) ;;
      *) fail 'invalid live artifact path configuration' ;;
    esac
    case "$path" in
      *'//'|*/.|*/..|*/./*|*/../*|*/) fail 'invalid live artifact path configuration' ;;
    esac
    current=''
    IFS='/' read -r -a components <<< "$path"
    for component in "${components[@]}"; do
      case "$component" in
        '') continue ;;
        .|..) fail 'invalid live artifact path configuration' ;;
      esac
      current="$current/$component"
      [ ! -L "$current" ] || fail 'invalid live artifact path configuration'
    done
    if [ -e "$path" ]; then
      case "$kind" in
        dir) [ -d "$path" ] || fail 'invalid live artifact type' ;;
        file) [ -f "$path" ] || fail 'invalid live artifact type' ;;
        *) fail 'invalid artifact contract' ;;
      esac
      continue
    fi
    parent=$(dirname "$path")
    while [ ! -e "$parent" ]; do
      [ "$parent" != / ] || break
      parent=$(dirname "$parent")
    done
    [ -d "$parent" ] && [ -w "$parent" ] || fail 'invalid live artifact parent'
  done
}

require_values() {
  local missing='' name value
  for name in "$@"; do
    value=${!name-}
    if [ -z "$value" ]; then
      missing="$missing $name"
    elif [[ "$value" == *$'\n'* || "$value" == *$'\r'* ]] || [ "${#value}" -gt 8192 ]; then
      fail 'invalid live reference configuration'
    fi
  done
  if [ -n "$missing" ]; then
    printf 'observability live preflight: missing required environment variables:%s\n' "$missing" >&2
    exit 2
  fi
}

validate_loopback_urls() {
  local name value port
  for name in "$@"; do
    value=${!name}
    if [[ ! "$value" =~ ^http://127\.0\.0\.1(:([0-9]+))?/?$ ]]; then
      fail 'invalid loopback endpoint configuration'
    fi
    port=${BASH_REMATCH[2]-}
    if [ -n "$port" ] && { [ "${#port}" -gt 5 ] || (( 10#$port < 1 || 10#$port > 65535 )); }; then
      fail 'invalid loopback endpoint configuration'
    fi
  done
}

grafana_project=${SAFE_OBSERVABILITY_COMPOSE_PROJECT:-longtermism-observability}
signoz_project=${SAFE_OBSERVABILITY_SIGNOZ_COMPOSE_PROJECT:-longtermism-signoz}
local_env_file=${SAFE_OBSERVABILITY_LOCAL_ENV_FILE:-deploy/observability/.env.local}
validate_optional_env_file "$local_env_file"

case "$mode" in
  grafana-project)
    validate_project "$grafana_project"
    ;;
  signoz-project)
    validate_project "$signoz_project"
    ;;
  grafana-e2e)
    validate_project "$grafana_project"
    require_values \
      LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_APP_BASE_URL \
      LONGTERMISM_SMOKE_CHAT_AUTHORIZATION LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT \
      LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH \
      LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST \
      LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION
    validate_loopback_urls \
      LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL LONGTERMISM_SMOKE_APP_BASE_URL
    [[ "$LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST" =~ ^sha256:[a-f0-9]{64}$ ]] || \
      fail 'invalid collector runtime digest'
    [ "$LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY" = 'otlphttp/loki' ] || \
      fail 'invalid collector component identity'
    validate_artifact_paths \
      dir:LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT file:LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH \
      file:LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH dir:LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT
    ;;
  signoz-e2e)
    validate_project "$signoz_project"
    require_values \
      LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_APP_BASE_URL \
      LONGTERMISM_SMOKE_CHAT_AUTHORIZATION LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT
    validate_loopback_urls \
      LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL LONGTERMISM_SMOKE_APP_BASE_URL
    validate_artifact_paths dir:LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT
    ;;
  resilience-e2e)
    validate_project "$grafana_project"
    require_values \
      LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT \
      LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL LONGTERMISM_SMOKE_CHAT_AUTHORIZATION \
      LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT \
      LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH
    [ "$LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT" = "$grafana_project" ] || \
      fail 'resilience compose project mismatch'
    validate_loopback_urls \
      LONGTERMISM_SMOKE_APP_BASE_URL LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL \
      LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL
    validate_artifact_paths \
      dir:LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT file:LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH \
      file:LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH
    ;;
  *)
    fail 'unsupported preflight mode'
    ;;
esac
