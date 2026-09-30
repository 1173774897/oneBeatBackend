#!/bin/sh
# 删除本地 Postgres 数据卷并重建（会清空 onebeat_prod / onebeat_test 数据）。
# 用于 init 脚本未执行成功（例如权限错误）后的修复。

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

INIT_SCRIPT="$REPO_ROOT/deploy/postgres/init/01-create-environment-databases.sh"
if [ ! -x "$INIT_SCRIPT" ]; then
  chmod 755 "$INIT_SCRIPT"
fi

if [ ! -f "$REPO_ROOT/deploy/.env.database" ]; then
  printf 'Missing deploy/.env.database — run ./scripts/bootstrap-local-env.sh first.\n' >&2
  exit 1
fi

printf 'This removes Docker volume onebeat-postgres-data and recreates the database container.\n'
printf 'Continue? [y/N] '
read -r answer
case "$answer" in
  y|Y|yes|YES) ;;
  *) printf 'Aborted.\n'; exit 0 ;;
esac

"$SCRIPT_DIR/compose.sh" -f "$REPO_ROOT/deploy/docker-compose.database.yml" down -v
"$SCRIPT_DIR/compose.sh" -f "$REPO_ROOT/deploy/docker-compose.database.yml" up -d --wait --wait-timeout 90
"$SCRIPT_DIR/dev-db-migrate.sh"

printf 'Local Postgres recreated and migrations applied.\n'
