#!/bin/sh
# 统一调用 Compose V2（docker compose）或旧版 docker-compose。
# 用法：./scripts/compose.sh -f deploy/docker-compose.database.yml up -d --wait

set -eu

if docker compose version >/dev/null 2>&1; then
  exec docker compose "$@"
fi

if command -v docker-compose >/dev/null 2>&1; then
  exec docker-compose "$@"
fi

printf 'Neither "docker compose" (Compose V2 plugin) nor "docker-compose" is available.\n' >&2
printf 'Install Docker Desktop, or: brew install docker-compose\n' >&2
printf 'See docs/deployment.md §本地开发环境 → Docker Compose  CLI\n' >&2
exit 1
