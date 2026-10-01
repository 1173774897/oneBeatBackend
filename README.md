# OneBeat Store API

收藏小铺的 Go 服务端骨架。服务默认直接解析 HTTPS，使用 PostgreSQL 保存业务数据，并通过
golang-migrate 管理表结构。

## 本地启动

需要 Go 1.22+、Docker 与 OpenSSL。Vultr 已部署时，完整说明（与线网差异、日常循环、何时推
`staging`）见 [部署文档 · 本地开发环境](docs/deployment.md#本地开发环境)。

首次准备：

```bash
cd /Users/lizhe/work/oneBeatBackend

chmod +x scripts/bootstrap-local-env.sh scripts/dev-db-migrate.sh scripts/generate-dev-certs.sh
./scripts/bootstrap-local-env.sh

./scripts/compose.sh -f deploy/docker-compose.database.yml up -d --wait
./scripts/dev-db-migrate.sh
./scripts/generate-dev-certs.sh
```

PostgreSQL 只映射到本机 `127.0.0.1:5432`。收藏小铺秘密（华为 Client Secret、pepper 等）见
[deploy/secrets/README.md](deploy/secrets/README.md)：

```bash
./scripts/init-local-secrets.sh
# 编辑 deploy/secrets/test.env 与 deploy/secrets/test/*.json

./scripts/run-api-dev.sh
```

`run-api-dev.sh` 会打印环境摘要、Postgres 探测、`go build -v` 编译过程，再启动
`bin/onebeat-api-dev`；连库与服务日志仍为应用输出的 JSON 行。若需明文 HTTP：
`STORE_API_ADDR=:8080 STORE_API_INSECURE_HTTP=1 ./scripts/run-api-dev.sh`。

服务默认监听 `https://localhost:8443`。自签名证书不受系统信任，因此命令行联调需加 `-k`：

```bash
curl -k https://localhost:8443/api/v1/test
curl -k https://localhost:8443/api/v1/database/test
curl -k https://localhost:8443/api/v1/store/bootstrap
curl -k https://localhost:8443/healthz
```

登录用户、验单和恢复购买接口分别为：

```text
POST /api/v1/auth/huawei
POST /api/v1/iap/purchases/verify
POST /api/v1/iap/purchases/restore
```

这三项依赖真实华为 Account/IAP 凭据，不能用探活 curl 伪造成功。启动前尤其要确认
`HUAWEI_IAP_PRIVATE_KEY` 来自 AGC「应用内支付 → 配置密钥」且为 EC P-256 私钥；完整字段映射和检查命令见
[运行时秘密说明](deploy/secrets/README.md#harmony-iap-服务端密钥三项怎么填)。

测试接口返回示例：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "service": "onebeat-store-api",
    "status": "ready",
    "scheme": "https",
    "environment": "development",
    "version": "dev"
  }
}
```

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `STORE_API_ADDR` | `:8443` | 监听地址 |
| `STORE_API_TLS_CERT` | `certs/dev-cert.pem` | TLS 证书路径 |
| `STORE_API_TLS_KEY` | `certs/dev-key.pem` | TLS 私钥路径 |
| `STORE_API_INSECURE_HTTP` | 未设置 | 仅当值为 `1` 时启用明文 HTTP |
| `APP_ENV` | `development` | 运行环境标识 |
| `APP_VERSION` | `dev` | 构建版本，CI 中为提交短 SHA |
| `DATABASE_URL` | 无 | PostgreSQL 连接 URL，必须提供 |
| `IAP_RECONCILIATION_TIMEZONE` | `Asia/Shanghai` | 生产订单对账自然日时区 |
| `IAP_RECONCILIATION_OBSERVE_ONLY` | `1` | 生产首次上线默认只观察交易，不改业务表 |
| `IAP_BACKFILL_ENABLED` | `0` | 仅生产 worker 使用；`1` 时执行历史回补 |
| `IAP_BACKFILL_DAYS` | `180` | 历史回补天数，上限 180 |

收藏小铺的 Account/IAP/会话秘密见 `deploy/secrets/test.env.example` 与
[deploy/secrets/README.md](deploy/secrets/README.md)。中国区 IAP Order/Subscription 根地址固定在代码中，
不通过环境变量配置；站点选择以华为官方
[IAP 公共说明](https://developer.huawei.com/consumer/cn/doc/HMSCore-References-V5/api-common-statement-0000001050986127-V5)
为准。

若只是排查本机网络，可临时使用明文模式：

```bash
STORE_API_ADDR=:8080 STORE_API_INSECURE_HTTP=1 go run ./cmd/api
```

生产环境不要提交私钥或使用自签名证书。应配置可信 CA 签发的证书，或由 Nginx、Caddy、云负载均衡等网关终止 TLS，再把流量转发给服务。

## 测试

```bash
go test ./...
```

接口单元测试使用数据库替身，不要求本机运行 PostgreSQL；能够验证 HTTPS 请求、数据库状态、
匿名商店目录、默认免费权益、方法限制和安全响应头。

## Docker

```bash
docker build \
  --build-arg APP_ENV=test \
  --build-arg APP_VERSION=local \
  -t onebeat:test .
```

镜像默认从 `/certs/fullchain.pem` 和 `/certs/privkey.pem` 加载 HTTPS 证书，并以非 root
用户运行。生产与测试 API 分别连接 `onebeat_prod` 和 `onebeat_test`，两个逻辑库位于同一个
PostgreSQL 容器中。

## GitHub CI/CD

`master` 自动构建并发布生产镜像到 `443`，`staging` 自动构建并发布测试镜像到
`8443`。镜像存储在 GitHub Container Registry。Vultr 初始化、GitHub Secrets、证书权限、
验证和回滚步骤见
[部署文档](docs/deployment.md)。

## 设计文档

- [收藏小铺系统设计](docs/store-system-design.md)：前后端分工、Huawei IAP/Account Kit
  接入边界、权益规则、接口、数据库表和私密配置方案。
