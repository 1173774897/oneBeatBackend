#!/bin/sh
# 本地开发：加载 deploy/.env.local 与 deploy/secrets/test.env 后启动 API。
# 用法：./scripts/run-api-dev.sh
# Docker/Vultr 容器由 Compose env_file 注入，不需要本脚本。

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
BIN="$REPO_ROOT/bin/onebeat-api-dev"

ENV_LOCAL="$REPO_ROOT/deploy/.env.local"
ENV_SECRETS="$REPO_ROOT/deploy/secrets/test.env"

log() {
  printf '[onebeat-dev] %s\n' "$*" >&2
}

require_file() {
  if [ ! -f "$1" ]; then
    log "Missing $1"
    case "$1" in
      *env.local)
        log 'Run: ./scripts/bootstrap-local-env.sh'
        ;;
      *test.env)
        log 'Run: ./scripts/init-local-secrets.sh and edit deploy/secrets/test.env'
        ;;
    esac
    exit 1
  fi
}

mask_database_url() {
  url="${DATABASE_URL:-}"
  if [ -z "$url" ]; then
    printf '(DATABASE_URL not set)'
    return
  fi
  printf '%s' "$url" | sed -E 's|(postgres://[^:/]+):[^@]+@|\1:***@|'
}

log 'Loading deploy/.env.local and deploy/secrets/test.env ...'
require_file "$ENV_LOCAL"
require_file "$ENV_SECRETS"

cd "$REPO_ROOT"

set -a
# shellcheck disable=SC1090
. "$ENV_LOCAL"
# shellcheck disable=SC1090
. "$ENV_SECRETS"
set +a

addr="${STORE_API_ADDR:-:8443}"
scheme="https"
if [ "${STORE_API_INSECURE_HTTP:-}" = "1" ]; then
  scheme="http"
fi

log "Environment: APP_ENV=${APP_ENV:-development} APP_VERSION=${APP_VERSION:-dev}"
log "Listen: ${scheme}://${addr} (STORE_API_ADDR=${addr})"
log "Database: $(mask_database_url)"

if command -v docker >/dev/null 2>&1; then
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx onebeat-postgres; then
    log 'Postgres: container onebeat-postgres is running'
  else
    log 'Postgres: onebeat-postgres not running — start with:'
    log '  ./scripts/compose.sh -f deploy/docker-compose.database.yml up -d --wait'
  fi
fi

if command -v nc >/dev/null 2>&1; then
  if nc -z 127.0.0.1 5432 2>/dev/null; then
    log 'Postgres: TCP 127.0.0.1:5432 is open'
  else
    log 'Postgres: nothing listening on 127.0.0.1:5432 yet'
  fi
fi

mkdir -p "$REPO_ROOT/bin"
log "Compiling ./cmd/api -> bin/onebeat-api-dev (verbose) ..."
if ! go build -v -trimpath -o "$BIN" ./cmd/api; then
  log 'Build failed.'
  exit 1
fi

log 'Build OK. Connecting to database and starting HTTP server (application logs below).'
log 'Press Ctrl+C to stop.'
exec "$BIN" "$@"
