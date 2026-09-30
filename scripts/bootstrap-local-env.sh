#!/bin/sh
# 生成本地 deploy/.env.*（密码随机），与 Vultr 上 §5.2 格式一致。
# 迁移容器用 onebeat-postgres；宿主机 go run 读 deploy/.env.local 里的 127.0.0.1 URL。

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DEPLOY_DIR="$REPO_ROOT/deploy"

for file in .env.database .env.test .env.prod .env.local; do
  if [ -e "$DEPLOY_DIR/$file" ]; then
    printf 'Refusing to overwrite existing %s\n' "$DEPLOY_DIR/$file" >&2
    printf 'Remove it first if you want to regenerate passwords.\n' >&2
    exit 1
  fi
done

umask 077

ADMIN_PASSWORD=$(openssl rand -hex 32)
PROD_PASSWORD=$(openssl rand -hex 32)
TEST_PASSWORD=$(openssl rand -hex 32)

cat >"$DEPLOY_DIR/.env.database" <<EOF
POSTGRES_USER=onebeat_admin
POSTGRES_PASSWORD=${ADMIN_PASSWORD}
POSTGRES_DB=postgres
ONEBEAT_PROD_DB_USER=onebeat_prod
ONEBEAT_PROD_DB_PASSWORD=${PROD_PASSWORD}
ONEBEAT_TEST_DB_USER=onebeat_test
ONEBEAT_TEST_DB_PASSWORD=${TEST_PASSWORD}
EOF

cat >"$DEPLOY_DIR/.env.prod" <<EOF
DATABASE_URL=postgres://onebeat_prod:${PROD_PASSWORD}@onebeat-postgres:5432/onebeat_prod?sslmode=disable
EOF

cat >"$DEPLOY_DIR/.env.test" <<EOF
DATABASE_URL=postgres://onebeat_test:${TEST_PASSWORD}@onebeat-postgres:5432/onebeat_test?sslmode=disable
EOF

cat >"$DEPLOY_DIR/.env.local" <<EOF
# 供宿主机 go run / curl 联调；不要用于 docker compose 里的 migrate（需 onebeat-postgres 主机名）。
DATABASE_URL=postgres://onebeat_test:${TEST_PASSWORD}@127.0.0.1:5432/onebeat_test?sslmode=disable
APP_ENV=development
APP_VERSION=dev
EOF

chmod 600 "$DEPLOY_DIR/.env.database" "$DEPLOY_DIR/.env.prod" "$DEPLOY_DIR/.env.test" "$DEPLOY_DIR/.env.local"

printf 'Created deploy/.env.database, .env.test, .env.prod, and .env.local\n'
printf 'Next: ./scripts/compose.sh -f deploy/docker-compose.database.yml up -d --wait\n'
printf 'Then run migrations (see docs/deployment.md §本地开发环境).\n'
