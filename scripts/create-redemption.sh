#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
ENV_FILE="${1:-$REPO_ROOT/deploy/secrets/test.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "找不到 $ENV_FILE" >&2
  echo "用法: $0 [deploy/secrets/test.env 的绝对或相对路径]" >&2
  exit 1
fi

set -a
# shellcheck source=/dev/null
. "$ENV_FILE"
set +a

if [ -z "${REDEMPTION_CODE_PEPPER:-}" ] || [ "${REDEMPTION_CODE_PEPPER#REPLACE}" != "${REDEMPTION_CODE_PEPPER}" ]; then
  echo "REDEMPTION_CODE_PEPPER 未在 $ENV_FILE 中配置" >&2
  exit 1
fi

export REDEMPTION_CODE_PEPPER
python3 - <<'PY'
import getpass
import hashlib
import hmac
import os
import unicodedata

pepper = os.environ["REDEMPTION_CODE_PEPPER"].strip().encode("utf-8")
phrase = unicodedata.normalize("NFKC", getpass.getpass("请输入口令：")).strip()

if not phrase or len(phrase) > 64:
    raise SystemExit("口令规范化后必须为 1～64 个 Unicode 字符")

digest = hmac.new(pepper, phrase.encode("utf-8"), hashlib.sha256)
print(digest.hexdigest())
PY
