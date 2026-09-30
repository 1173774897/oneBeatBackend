#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

docker run --rm \
  --network onebeat-backend \
  --env-file "$REPO_ROOT/deploy/.env.test" \
  -v "$REPO_ROOT/migrations:/migrations:ro" \
  --entrypoint /bin/sh \
  migrate/migrate:v4.19.1 \
  -ec 'exec migrate -path=/migrations -database "$DATABASE_URL" up'
