#!/usr/bin/env bash
# Re-key the redemption phrases of one environment for another, e.g. give prod
# the same phrases as test. Digests are HMACs under each environment's own
# REDEMPTION_CODE_PEPPER, so a file cannot simply be copied: every phrase is
# typed once (never echoed), verified against the source digest, then hashed
# with the target pepper. Phrases and digests are never printed or stored.
#
# 用法（在仓库根目录、交互终端中运行）：
#   ./scripts/copy-redemption-codes.sh
#   ./scripts/copy-redemption-codes.sh <源env> <源摘要JSON> <目标env> <目标摘要JSON> <目标pepperVersion>
# 默认：test → prod，pepperVersion=prod-v1。
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
SOURCE_ENV="${1:-$REPO_ROOT/deploy/secrets/test.env}"
SOURCE_CODES="${2:-$REPO_ROOT/deploy/secrets/test/redemption_codes.json}"
TARGET_ENV="${3:-$REPO_ROOT/deploy/secrets/prod.env}"
TARGET_CODES="${4:-$REPO_ROOT/deploy/secrets/prod/redemption_codes.json}"
TARGET_PEPPER_VERSION="${5:-prod-v1}"

for file in "$SOURCE_ENV" "$SOURCE_CODES" "$TARGET_ENV"; do
  if [ ! -f "$file" ]; then
    echo "找不到 $file" >&2
    exit 1
  fi
done

# Each env file is sourced in its own subshell so the two peppers cannot clobber
# each other; they reach Python through the environment, never through argv.
pepper_of() {
  ( set -a; . "$1"; set +a; printf '%s' "${REDEMPTION_CODE_PEPPER:-}" )
}
SOURCE_PEPPER=$(pepper_of "$SOURCE_ENV")
TARGET_PEPPER=$(pepper_of "$TARGET_ENV")
export SOURCE_PEPPER TARGET_PEPPER

printf '源：%s + %s\n目标：%s → %s\n' "$SOURCE_ENV" "$SOURCE_CODES" "$TARGET_ENV" "$TARGET_CODES" >&2
python3 - "$SOURCE_CODES" "$TARGET_CODES" "$TARGET_PEPPER_VERSION" <<'PY'
import getpass
import hashlib
import hmac
import json
import os
import sys
import tempfile
import unicodedata
import warnings

# Must match scripts/create-redemption.sh and the server (NFKC, then Go
# strings.TrimSpace semantics, at most 64 runes).
GO_WHITESPACE = "\t\n\v\f\r \u0085                 　"
source_codes_path, target_codes_path, target_version = sys.argv[1:4]

def pepper(name):
    value = os.environ[name].strip(GO_WHITESPACE)
    if len(value.encode("utf-8")) < 32 or value.startswith("REPLACE"):
        raise SystemExit(f"{name} 未配置或不足 32 字节")
    return value.encode("utf-8")

source_pepper = pepper("SOURCE_PEPPER")
target_pepper = pepper("TARGET_PEPPER")
if hmac.compare_digest(source_pepper, target_pepper):
    raise SystemExit("源和目标使用了同一个 pepper；生产必须使用独立的 pepper")

with open(source_codes_path, encoding="utf-8") as handle:
    source = json.load(handle)
source_codes = source.get("codes") or {}
if not source_codes:
    raise SystemExit("源摘要文件没有任何 codeKey")

def read_phrase(prompt):
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("error", getpass.GetPassWarning)
            raw = getpass.getpass(prompt)
    except (getpass.GetPassWarning, EOFError):
        raise SystemExit("无法安全读取口令，请在交互终端中运行此脚本")
    phrase = unicodedata.normalize("NFKC", raw).strip(GO_WHITESPACE)
    if not phrase or len(phrase) > 64:
        return None
    return phrase

target_codes = {}
for key in sorted(source_codes):
    expected = bytes.fromhex(source_codes[key])
    for attempt in range(3):
        phrase = read_phrase(f"请输入 {key} 的口令：")
        if phrase is not None and hmac.compare_digest(
                hmac.new(source_pepper, phrase.encode("utf-8"), hashlib.sha256).digest(), expected):
            target_codes[key] = hmac.new(target_pepper, phrase.encode("utf-8"), hashlib.sha256).hexdigest()
            print(f"  {key}：与源环境一致，已生成目标摘要", file=sys.stderr)
            break
        print("  与源环境的摘要不匹配，请重新输入", file=sys.stderr)
    else:
        raise SystemExit(f"{key} 连续 3 次不匹配，未写入任何文件")

# Same checks the server applies at startup (redemption.LoadCodeBook).
if len(set(target_codes.values())) != len(target_codes):
    raise SystemExit("两个 codeKey 生成了相同摘要，未写入")

document = {
    "_comment": "生产口令摘要；与 test 相同的口令，用 prod.env 的 REDEMPTION_CODE_PEPPER 计算。",
    "pepperVersion": target_version,
    "codes": target_codes,
}
directory = os.path.dirname(os.path.abspath(target_codes_path))
os.makedirs(directory, exist_ok=True)
descriptor, temporary = tempfile.mkstemp(dir=directory, prefix=".redemption_codes.", suffix=".tmp")
try:
    os.fchmod(descriptor, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        json.dump(document, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    os.replace(temporary, target_codes_path)
except BaseException:
    if os.path.exists(temporary):
        os.unlink(temporary)
    raise
print(f"已写入 {target_codes_path}（权限 600），codeKey：{', '.join(sorted(target_codes))}", file=sys.stderr)
PY
