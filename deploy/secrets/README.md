# 收藏小铺运行时秘密

与 [store-system-design.md §10.2](../../docs/store-system-design.md) 一致。这些值**不得**进入 Git、
GitHub Actions Secrets 或 Docker 镜像。数据库连接仍使用上一级的 `deploy/.env.test` /
`deploy/.env.prod`（仅 `DATABASE_URL`）。

## 目录说明

| 路径 | 是否提交 | 用途 |
| --- | --- | --- |
| `test.env.example` / `prod.env.example` | 是 | 键名与注释模板 |
| `test.env` / `prod.env` | **否** | **全部**单行秘密：OneBeat pepper、JWT、华为 Account/IAP（含 PEM 私钥 `\n` 转义） |
| `test/redemption_codes.json` | **否** | 口令 HMAC 摘要（仍用 JSON，见 §10.3） |
| `test/*.json.example` | 是 | 口令文件格式示例 |

华为 IAP 凭据**不再**使用 `huawei_iap_credentials.json`，统一写在 `test.env` / `prod.env`，便于
`#` 注释。

## 本地（Mac）一次性准备

```bash
./scripts/init-local-secrets.sh
```

编辑 `deploy/secrets/test.env`：填写 `REPLACE_ME` 与华为段；有口令活动时编辑
`deploy/secrets/test/redemption_codes.json`。

```bash
./scripts/run-api-dev.sh
```

脚本会在仓库根目录加载上述两个文件并 `go run ./cmd/api`。若手动 source，也请在仓库根目录执行，
以便 `REDEMPTION_CODES_PATH=deploy/secrets/test/redemption_codes.json` 相对路径有效。

`HUAWEI_IAP_PRIVATE_KEY` 为单行 PEM，换行写为 `\n`；Go 实现读取后会还原为真实换行。

### 华为 IAP 服务账号三项字段怎么填

这三项必须来自华为开发者联盟 **API Console 创建服务账号后下载的同一个 JSON 文件**：

| JSON 字段 | 环境变量 | 要求 |
| --- | --- | --- |
| `private_key` | `HUAWEI_IAP_PRIVATE_KEY` | RSA 私钥，服务账号 JWT 使用 PS256；PEM 换行写成 `\n` |
| `key_id` | `HUAWEI_IAP_KEY_ID` | 与该 RSA 私钥配套，写入 JWT `kid` |
| `sub_account` | `HUAWEI_IAP_ISSUER_ID` | 写入 JWT `iss` |

不要使用 AGC 调试签名、IAP 通知验签、客户端签名或其他地方生成的 EC P-256 私钥。它们即使也是
`-----BEGIN PRIVATE KEY-----`，算法仍不兼容。官方文档写 `alg=PS256`，但换取 `access_token` 时华为 OAuth 实际校验 **RS256**（PKCS#1 v1.5 + SHA-256）；本仓库 `internal/huawei/iap` 已按 RS256 签发 assertion。另见
SHA-256 with RSA/PSS（部分文档仍写 PS256）：
[基于 Service Account 开放鉴权](https://developer.huawei.com/consumer/cn/doc/hmscore-guides/open-platform-service-account-0000001053509221)。

在不输出私钥内容的前提下，可检查密钥类型：

```bash
# 在 API Console 下载 JSON 后执行。输出应包含 "Private-Key: (2048 bit" 或更高 RSA 位数。
jq -r '.private_key' /path/to/service-account.json | openssl pkey -text -noout | head -n 1
```

如果输出包含 `ASN1 OID: prime256v1`、`NIST CURVE: P-256` 或命令无法按 RSA 服务账号密钥解析，
不要把它填入 IAP 配置。`private_key`、`key_id`、`sub_account` 必须成套使用，不能跨服务账号拼接。

中国区 Order 与 Subscription 地址已按华为官方站点表固定在 Go 代码中，不需要也不允许在 env 中
另配。若其他资料出现不同域名，以
[IAP 公共说明](https://developer.huawei.com/consumer/cn/doc/HMSCore-References-V5/api-common-statement-0000001050986127-V5)
为准。

## Vultr 服务器

```text
/opt/onebeatbackend/secrets/test.env      # 含华为 IAP（sandbox）
/opt/onebeatbackend/secrets/test/redemption_codes.json
/opt/onebeatbackend/secrets/prod.env      # 含华为 IAP（production，与 test 不同 pepper/凭据）
/opt/onebeatbackend/secrets/prod/redemption_codes.json
```

容器内将 `REDEMPTION_CODES_PATH` 设为 `/run/secrets/redemption_codes.json`（`prod.env.example`
已如此）；**上传到 Vultr 前**请把 Mac 本地 `test.env` 里若仍为
`deploy/secrets/test/redemption_codes.json` 的路径改成容器路径。生产 `prod.env` 须单独填写正式
华为凭据，`HUAWEI_IAP_ENVIRONMENT=production`。

### Mac → Vultr 命令清单（test + prod IAP）

**上传前（Mac，仓库根目录）**

```bash
# 若还没有 prod 秘密文件
cp deploy/secrets/prod.env.example deploy/secrets/prod.env
chmod 600 deploy/secrets/prod.env

# 编辑 test.env / prod.env：REPLACE_ME、华为 Account + IAP（prod 用正式密钥）
# test：HUAWEI_IAP_ENVIRONMENT=sandbox（或你方测试环境值）
# prod：HUAWEI_IAP_ENVIRONMENT=production
# 两者 REDEMPTION_CODES_PATH=/run/secrets/redemption_codes.json
# 可选口令文件
mkdir -p deploy/secrets/prod
# deploy/secrets/test/redemption_codes.json
# deploy/secrets/prod/redemption_codes.json
```

**一键上传（推荐）**

```bash
export VULTR_HOST='<Vultr-IP>'   # 或 SSH_KEY=~/.ssh/你的密钥
./scripts/upload-vultr-secrets.sh
# 等价：./scripts/upload-vultr-secrets.sh onebeat@<Vultr-IP>
```

**手工 scp（与脚本相同路径）**

```bash
KEY=~/.ssh/onebeat_github_actions
HOST=onebeat@<Vultr-IP>
REMOTE=/opt/onebeatbackend/secrets

ssh -i "$KEY" "$HOST" 'umask 077; mkdir -p '"$REMOTE"'/test '"$REMOTE"'/prod'

scp -i "$KEY" deploy/secrets/test.env  "$HOST:$REMOTE/test.env"
scp -i "$KEY" deploy/secrets/prod.env  "$HOST:$REMOTE/prod.env"
scp -i "$KEY" deploy/secrets/test/redemption_codes.json  "$HOST:$REMOTE/test/redemption_codes.json"
scp -i "$KEY" deploy/secrets/prod/redemption_codes.json  "$HOST:$REMOTE/prod/redemption_codes.json"

ssh -i "$KEY" "$HOST" 'chmod 700 '"$REMOTE"'/test '"$REMOTE"'/prod; \
  chmod 600 '"$REMOTE"'/*.env '"$REMOTE"'/test/* '"$REMOTE"'/prod/*; \
  ls -la '"$REMOTE"' '"$REMOTE"'/test '"$REMOTE"'/prod'
```

**上传后（Vultr）**

```bash
ssh -i ~/.ssh/onebeat_github_actions onebeat@<Vultr-IP>
cd /opt/onebeatbackend
docker compose -f docker-compose.test.yml up -d --force-recreate
docker compose -f docker-compose.prod.yml up -d --force-recreate
```

Actions 只部署镜像与迁移，**不会**复制 `secrets/`；改秘密后只需 scp + 上述 recreate，不必重新 push。

## Docker Compose

- `env_file`：`./secrets/test.env` 或 `./secrets/prod.env`（`required: false`）
- `volume`：`./secrets/test` → `/run/secrets:ro`（主要挂载 `redemption_codes.json`）

## 生成随机秘密

```bash
openssl rand -hex 32
```

## 公共配置（不要写进本目录）

商品 ID、`iss`/JWKS 等见设计文档 §10.1。`HUAWEI_CLIENT_ID` 在 secrets 里是为服务端调华为方便，
与公共配置应保持一致。
