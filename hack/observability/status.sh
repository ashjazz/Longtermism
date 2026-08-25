#!/usr/bin/env bash
set -eu

# 状态命令刻意不调用 docker compose：Compose 会解析本地 env 文件，而这里仅需要
# 容器标签上的服务名与健康事实。固定字段投影避免 ports、command、labels、endpoint
# 或 credential 意外进入操作者终端和 CI 日志。
profile=${SAFE_OBS_PROFILE:-grafana}
case "$profile" in
  grafana)
    project=${SAFE_OBSERVABILITY_COMPOSE_PROJECT:-longtermism-observability}
    ;;
  signoz)
    project=${SAFE_OBSERVABILITY_SIGNOZ_COMPOSE_PROJECT:-longtermism-signoz}
    ;;
  *)
    printf '%s\n' 'obs-status: unsupported OBS_PROFILE' >&2
    exit 2
    ;;
esac

# project 会进入 docker argv，因此先执行字符闭集与长度门禁。错误只报告变量名，
# 不回显可能由外部包装层注入的原值。
case "$project" in
  ''|[!a-z0-9]*|*[!a-z0-9_-]*)
    printf '%s\n' 'obs-status: invalid compose project configuration' >&2
    exit 2
    ;;
esac
if [ "${#project}" -gt 63 ]; then
  printf '%s\n' 'obs-status: invalid compose project configuration' >&2
  exit 2
fi

if ! rows=$(docker ps --all \
  --filter "label=com.docker.compose.project=$project" \
  --format '{{.Label "com.docker.compose.service"}}|{{.Status}}|{{.Image}}' 2>/dev/null); then
  printf '%s\n' 'obs-status: docker inventory query failed' >&2
  exit 1
fi
if [ "${#rows}" -gt 32768 ]; then
  printf '%s\n' 'obs-status: docker inventory exceeds diagnostic budget' >&2
  exit 1
fi

printf 'profile=%s evidence=diagnostic_only query_evidence=not_run\n' "$profile"
if [ -z "$rows" ]; then
  printf '%s\n' 'service=none state=absent health=unknown version=unknown'
  exit 0
fi

row_count=0
while IFS='|' read -r raw_service raw_status raw_image _; do
  row_count=$((row_count + 1))
  if [ "$row_count" -gt 64 ]; then
    printf '%s\n' 'obs-status: docker inventory exceeds diagnostic budget' >&2
    exit 1
  fi
  service=$raw_service
  case "$profile:$service" in
    grafana:collector-storage-init|grafana:collector|grafana:collector-health-probe|grafana:loki-health-probe|grafana:tempo-health-probe|grafana:prometheus|grafana:loki|grafana:tempo|grafana:grafana) ;;
    signoz:collector|signoz:collector-health-probe|signoz:collector-storage-init|signoz:signoz|signoz:signoz-otel-collector|signoz:clickhouse) ;;
    grafana:langfuse-db|grafana:langfuse-clickhouse|grafana:langfuse-clickhouse-init|grafana:langfuse-redis|grafana:langfuse-minio|grafana:langfuse-minio-init|grafana:langfuse-web|grafana:langfuse-worker) ;;
    signoz:langfuse-db|signoz:langfuse-clickhouse|signoz:langfuse-clickhouse-init|signoz:langfuse-redis|signoz:langfuse-minio|signoz:langfuse-minio-init|signoz:langfuse-web|signoz:langfuse-worker) ;;
    *) service=unknown ;;
  esac

  case "$raw_status" in
    Up*) state=running ;;
    Exited*) state=stopped ;;
    Created*) state=created ;;
    Restarting*) state=restarting ;;
    Paused*) state=paused ;;
    Removing*) state=removing ;;
    Dead*) state=dead ;;
    *) state=unknown ;;
  esac
  case "$raw_status" in
    *'(unhealthy)'*) health=unhealthy ;;
    *'(healthy)'*) health=healthy ;;
    *'(health: starting)'*) health=starting ;;
    *) health=none ;;
  esac

  image_name=${raw_image##*/}
  case "$image_name" in
    *:*) version=${image_name##*:} ;;
    *) version=unknown ;;
  esac
  version=${version%%@*}
  if [[ ! "$version" =~ ^v?[0-9]+(\.[0-9]+){1,3}([.+_-][A-Za-z0-9]+)*$ ]] &&
    [[ ! "$version" =~ ^RELEASE\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}-[0-9]{2}-[0-9]{2}Z$ ]]; then
    version=unknown
  fi

  printf 'service=%s state=%s health=%s version=%s\n' "$service" "$state" "$health" "$version"
done <<EOF
$rows
EOF
