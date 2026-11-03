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
POST /api/v1/store/redemptions
POST /api/v1/account/deletion
```

这几项依赖真实华为账号凭据，不能用探活 curl 伪造成功。付费购买（华为 IAP）自 2026-11-03 起
完全在客户端完成校验与确认，本服务不再提供验单、恢复购买和华为通知接口，也不再需要 IAP 密钥
（服务器在香港，购买记录不应出境，见 `docs/store-system-design.md` §15）。

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

收藏小铺的 Account/会话/口令秘密见 `deploy/secrets/test.env.example` 与
[deploy/secrets/README.md](deploy/secrets/README.md)。服务器不调用华为 IAP 接口，不需要 IAP 配置。

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

- [收藏小铺：端侧购买与服务端兑换技术设计](docs/store-device-iap-technical-design.md)：**现行方案**。付费购买在客户端
  直接与华为完成，服务器只负责华为账号登录、口令兑换和账号注销。
- [口令月卡与角色限免技术设计](docs/redemption-free-access-technical-design.md)：兑换和限免细节（华为相关部分已弃用）。
- [收藏小铺系统设计](docs/store-system-design.md)：**已弃用**，仅商品目录、权益模型、自然月等部分仍可参考。
- [IAP 订单与对账方案调整](docs/store-system-design-tweak.md)：**已弃用**，仅作历史记录。
