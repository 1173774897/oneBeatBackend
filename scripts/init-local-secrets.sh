#!/bin/sh
# 从 example 生成本地 gitignore 秘密文件（不覆盖已存在文件）。

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SECRETS_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/../deploy/secrets" && pwd)

copy_if_missing() {
  src="$1"
  dst="$2"
  if [ -e "$dst" ]; then
    printf 'skip (exists): %s\n' "$dst"
  else
    cp "$src" "$dst"
    chmod 600 "$dst"
    printf 'created: %s\n' "$dst"
  fi
}

copy_if_missing "$SECRETS_DIR/test.env.example" "$SECRETS_DIR/test.env"
copy_if_missing \
  "$SECRETS_DIR/test/redemption_codes.json.example" \
  "$SECRETS_DIR/test/redemption_codes.json"

printf '\nEdit deploy/secrets/test.env (含华为 IAP 字段) 与 redemption_codes.json，然后：\n'
printf '  ./scripts/run-api-dev.sh\n'
