# OneBeat Store API

收藏小铺的 Go 服务端骨架。服务默认直接解析 HTTPS，使用 PostgreSQL 保存业务数据，并通过
golang-migrate 管理表结构。

## 本地启动

需要 Go 1.22+、Docker 与 OpenSSL。首次启动先创建本地数据库配置：

```bash
cd /Users/lizhe/work/oneBeatBackend

cp deploy/.env.database.example deploy/.env.database
cp deploy/.env.test.example deploy/.env.test

# 将三个示例密码替换为不同的随机值，并保证 .env.test 中的密码与测试密码一致。
openssl rand -hex 32

docker compose -f deploy/docker-compose.database.yml up -d --wait

# 本地仓库中的迁移目录位于项目根目录，因此直接运行迁移镜像。
docker run --rm \
  --network onebeat-backend \
  --env-file deploy/.env.test \
  -v "$PWD/migrations:/migrations:ro" \
  --entrypoint /bin/sh \
  migrate/migrate:v4.19.1 \
  -ec 'exec migrate -path=/migrations -database "$DATABASE_URL" up'
```

PostgreSQL 只映射到本机 `127.0.0.1:5432`，不会监听公网地址。然后生成仅用于本地开发的
自签名证书并启动 API：

```bash
chmod +x scripts/generate-dev-certs.sh
./scripts/generate-dev-certs.sh

DATABASE_URL='postgres://onebeat_test:<测试密码>@127.0.0.1:5432/onebeat_test?sslmode=disable' \
  go run ./cmd/api
```

服务默认监听 `https://localhost:8443`。自签名证书不受系统信任，因此命令行联调需加 `-k`：

```bash
curl -k https://localhost:8443/api/v1/test
curl -k https://localhost:8443/api/v1/database/test
curl -k https://localhost:8443/healthz
```

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

若只是排查本机网络，可临时使用明文模式：

```bash
STORE_API_ADDR=:8080 STORE_API_INSECURE_HTTP=1 go run ./cmd/api
```

生产环境不要提交私钥或使用自签名证书。应配置可信 CA 签发的证书，或由 Nginx、Caddy、云负载均衡等网关终止 TLS，再把流量转发给服务。

## 测试

```bash
go test ./...
```

接口单元测试使用数据库替身，不要求本机运行 PostgreSQL；能够验证 HTTPS 请求、数据库状态
响应、方法限制和安全响应头。

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
