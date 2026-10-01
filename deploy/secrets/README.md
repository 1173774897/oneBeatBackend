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

### Harmony IAP 服务端密钥三项怎么填

查单 / 确认发货走 [IAP 服务端 JWT](https://developer.huawei.com/consumer/cn/doc/harmonyos-references/iap-jwt-description)（`Authorization: Bearer <JWT>`，`aud=iap-v1`，**ES256**，带请求体 `digest`）。须从 **AppGallery Connect → 应用 → 应用内支付 → 配置密钥** 创建并下载**同一套**密钥（多为 **EC P-256**）：

| 密钥文件字段 | 环境变量 | 要求 |
| --- | --- | --- |
| 私钥 PEM | `HUAWEI_IAP_PRIVATE_KEY` | **ECDSA P-256**；换行写成 `\n` |
| 密钥 ID | `HUAWEI_IAP_KEY_ID` | JWT `kid` |
| Issuer ID | `HUAWEI_IAP_ISSUER_ID` | JWT `iss` |

App ID（`6917617181108973504`）在代码里作为 JWT 的 `aid`，不必另配 env。

**不要**把 API Console **服务账号 RSA** 私钥填进 `HUAWEI_IAP_PRIVATE_KEY`：Harmony IAP REST 不接受 OAuth 换得的 access token，RSA 也无法签 ES256 请求 JWT。

检查私钥类型（勿输出 PEM 内容）：

```bash
# 应看到 prime256v1 / P-256，而不是 RSA 2048/4096
openssl pkey -in /path/to/iap-server-key.pem -text -noout | head -n 3
```

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

ssh -i "$KEY" "$HOST" 'chmod 600 '"$REMOTE"'/*.env; \
  chmod 755 '"$REMOTE"'/test '"$REMOTE"'/prod; \
  chmod 644 '"$REMOTE"'/test/redemption_codes.json '"$REMOTE"'/prod/redemption_codes.json 2>/dev/null || true; \
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

## AGC 关键事件通知（退款 / 撤销 webhook）

测试环境在 **AppGallery Connect → 盈利 → 应用内购买 → 事件通知** 配置：

| 项 | 测试 staging 值 |
| --- | --- |
| 通知 URL | `https://onebeatapistaging.liluanxin.com:8443/api/v1/webhooks/huawei/iap` |
| 方法 | `POST`，JSON 体 `{"jwsNotification":"..."}` |
| 与 API 环境 | `APP_ENV=test` + `HUAWEI_IAP_ENVIRONMENT=sandbox`，勿指向生产库 |

联调前核对：URL 公网 HTTPS 可达、事件类型包含退款/撤销/订阅状态变更（以 AGC 当前勾选项为准）、
沙盒与 staging secrets 一致。服务端先保存脱敏通知，再验签 JWS；以 `notificationRequestId` 幂等写入
`iap_webhook_events`，复核 Huawei 订单/订阅后更新 Provider 交易、订阅周期和权益。若通知暂时无法
关联 OneBeat 用户，事件保持 `FAILED` 并返回可重试错误；用户恢复购买完成账号绑定后，Huawei 重试或
后续人工重放即可继续处理，不能把未归因通知静默标成成功。

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
