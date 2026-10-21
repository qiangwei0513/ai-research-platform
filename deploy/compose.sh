#!/usr/bin/env bash
# Full local stack / existing host reverse proxy. Arguments pass through to Compose.
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"
env_file="${DEPLOY_ENV_FILE:-$project_dir/deploy/.env}"
if [[ ! -f "$env_file" ]]; then
  printf '%s\n' "缺少配置文件：$env_file" \
    "请复制 deploy/.env.example 为 deploy/.env，并填写配置。" >&2
  exit 1
fi
exec docker compose --env-file "$env_file" \
  -f compose.yaml -f deploy/compose.data.yaml -f deploy/compose.existing-proxy.yaml "$@"
