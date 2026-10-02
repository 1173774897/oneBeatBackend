#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
CHECK_KEY=''
CHECK_FILE=''
if [ "${1:-}" = '--check' ]; then
  if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
    echo "用法: $0 --check codeKey 摘要JSON文件 [env文件]" >&2
    exit 1
  fi
  CHECK_KEY="$2"
  CHECK_FILE="$3"
  shift 3
elif [ "$#" -gt 1 ]; then
  echo "用法: $0 [env文件] 或 $0 --check codeKey 摘要JSON文件 [env文件]" >&2
  exit 1
fi
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
printf '使用 env 文件: %s\n' "$ENV_FILE" >&2
python3 - "$CHECK_KEY" "$CHECK_FILE" <<'PY'
import getpass
import hashlib
import hmac
import json
import os
import sys
import unicodedata
import warnings

# Python strip() also removes U+001C–U+001F, while Go strings.TrimSpace does not.
# Keep the generator's normalization identical to the server's Unicode White_Space.
GO_WHITESPACE = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"
pepper = os.environ["REDEMPTION_CODE_PEPPER"].strip(GO_WHITESPACE).encode("utf-8")
if len(pepper) < 32:
    raise SystemExit("REDEMPTION_CODE_PEPPER 必须至少为 32 字节，与服务端要求一致")

def read_phrase(prompt):
    # Never fall back to echoing a secret when no controlling terminal is available.
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("error", getpass.GetPassWarning)
            raw = getpass.getpass(prompt)
    except (getpass.GetPassWarning, EOFError):
        raise SystemExit("无法安全读取口令，请在交互终端中运行此脚本")
    return unicodedata.normalize("NFKC", raw).strip(GO_WHITESPACE)

phrase = read_phrase("请输入口令：")

if not phrase or len(phrase) > 64:
    raise SystemExit("口令规范化后必须为 1～64 个 Unicode 字符")

digest = hmac.new(pepper, phrase.encode("utf-8"), hashlib.sha256).hexdigest()
check_key, check_file = sys.argv[1:3]
if check_key:
    try:
        with open(check_file, encoding="utf-8") as handle:
            expected = json.load(handle)["codes"][check_key]
        expected_bytes = bytes.fromhex(expected)
    except (OSError, ValueError, KeyError, TypeError):
        raise SystemExit("无法读取指定 codeKey 的有效摘要，请检查 JSON 文件")
    if len(expected_bytes) != 32 or not hmac.compare_digest(bytes.fromhex(digest), expected_bytes):
        raise SystemExit("校验失败：输入口令、此 env 的 pepper 与指定摘要不匹配")
    print("校验通过：输入口令与指定摘要匹配")
else:
    if phrase != read_phrase("请再次输入口令："):
        raise SystemExit("两次口令输入不一致，未生成摘要")
    print(digest)
PY
