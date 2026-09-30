#!/bin/sh
# 从 Mac 上传 deploy/secrets/* 到 Vultr（test + prod，含 IAP）。
# GitHub Actions 不会同步这些文件，需手工执行。
#
# 用法（在仓库根目录）：
#   VULTR_HOST=<IP> ./scripts/upload-vultr-secrets.sh
#   ./scripts/upload-vultr-secrets.sh onebeat@<IP>
#
# 可选环境变量：
#   SSH_KEY   默认 ~/.ssh/onebeat_github_actions
#   REMOTE    默认 /opt/onebeatbackend/secrets

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
SECRETS_DIR="$REPO_ROOT/deploy/secrets"

SSH_USER="${SSH_USER:-onebeat}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/onebeat_github_actions}"
REMOTE_SECRETS="${REMOTE:-/opt/onebeatbackend/secrets}"

if [ "${1:-}" != "" ]; then
  SSH_TARGET="$1"
else
  if [ "${VULTR_HOST:-}" = "" ]; then
    echo "用法: VULTR_HOST=<IP> $0" >&2
    echo "  或: $0 onebeat@<IP>" >&2
    exit 1
  fi
  SSH_TARGET="${SSH_USER}@${VULTR_HOST}"
fi

SSH_OPTS="-i $SSH_KEY -o BatchMode=yes"

require_file() {
  if [ ! -f "$1" ]; then
    echo "缺少文件: $1" >&2
    exit 1
  fi
}

require_file "$SECRETS_DIR/test.env"
require_file "$SECRETS_DIR/prod.env"

for f in "$SECRETS_DIR/test.env" "$SECRETS_DIR/prod.env"; do
  if grep -q 'REPLACE_ME' "$f" 2>/dev/null; then
    echo "警告: $f 仍含 REPLACE_ME，确认后再上传。" >&2
  fi
  if grep -q '^REDEMPTION_CODES_PATH=deploy/' "$f" 2>/dev/null; then
    echo "警告: $f 的 REDEMPTION_CODES_PATH 仍是 Mac 相对路径；" \
      "Vultr 上应为 /run/secrets/redemption_codes.json" >&2
  fi
done

if [ ! -f "$SECRETS_DIR/test/redemption_codes.json" ]; then
  echo "提示: 无 test/redemption_codes.json，跳过（无口令活动时可接受）。" >&2
  UPLOAD_TEST_JSON=false
else
  UPLOAD_TEST_JSON=true
fi

if [ ! -f "$SECRETS_DIR/prod/redemption_codes.json" ]; then
  echo "提示: 无 prod/redemption_codes.json，跳过。" >&2
  UPLOAD_PROD_JSON=false
else
  UPLOAD_PROD_JSON=true
fi

echo "目标: $SSH_TARGET:$REMOTE_SECRETS"

ssh $SSH_OPTS "$SSH_TARGET" "umask 077; mkdir -p '$REMOTE_SECRETS/test' '$REMOTE_SECRETS/prod'"

scp $SSH_OPTS \
  "$SECRETS_DIR/test.env" \
  "$SSH_TARGET:$REMOTE_SECRETS/test.env"

scp $SSH_OPTS \
  "$SECRETS_DIR/prod.env" \
  "$SSH_TARGET:$REMOTE_SECRETS/prod.env"

if [ "$UPLOAD_TEST_JSON" = true ]; then
  scp $SSH_OPTS \
    "$SECRETS_DIR/test/redemption_codes.json" \
    "$SSH_TARGET:$REMOTE_SECRETS/test/redemption_codes.json"
fi

if [ "$UPLOAD_PROD_JSON" = true ]; then
  scp $SSH_OPTS \
    "$SECRETS_DIR/prod/redemption_codes.json" \
    "$SSH_TARGET:$REMOTE_SECRETS/prod/redemption_codes.json"
fi

ssh $SSH_OPTS "$SSH_TARGET" <<EOF
set -eu
chmod 700 '$REMOTE_SECRETS/test' '$REMOTE_SECRETS/prod'
chmod 600 '$REMOTE_SECRETS/test.env' '$REMOTE_SECRETS/prod.env'
[ -f '$REMOTE_SECRETS/test/redemption_codes.json' ] && \
  chmod 600 '$REMOTE_SECRETS/test/redemption_codes.json'
[ -f '$REMOTE_SECRETS/prod/redemption_codes.json' ] && \
  chmod 600 '$REMOTE_SECRETS/prod/redemption_codes.json'
ls -la '$REMOTE_SECRETS' '$REMOTE_SECRETS/test' '$REMOTE_SECRETS/prod' 2>/dev/null || ls -la '$REMOTE_SECRETS'
EOF

cat <<'NOTE'

上传完成。在 Vultr 上若 API 已在跑，需重启对应 compose 栈以加载新 env：

  cd /opt/onebeatbackend
  docker compose -f docker-compose.test.yml up -d --force-recreate
  docker compose -f docker-compose.prod.yml up -d --force-recreate

NOTE
