# OneBeat Store API

收藏小铺的 Go 服务端骨架。当前仅提供测试与健康检查接口，默认直接解析 HTTPS。

## 本地启动

需要 Go 1.22+ 与 OpenSSL。首次启动先生成仅用于本地开发的自签名证书：

```bash
cd /Users/lizhe/work/oneBeatBackend
chmod +x scripts/generate-dev-certs.sh
./scripts/generate-dev-certs.sh
go run ./cmd/api
```

服务默认监听 `https://localhost:8443`。自签名证书不受系统信任，因此命令行联调需加 `-k`：

```bash
curl -k https://localhost:8443/api/v1/test
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

若只是排查本机网络，可临时使用明文模式：

```bash
STORE_API_ADDR=:8080 STORE_API_INSECURE_HTTP=1 go run ./cmd/api
```

生产环境不要提交私钥或使用自签名证书。应配置可信 CA 签发的证书，或由 Nginx、Caddy、云负载均衡等网关终止 TLS，再把流量转发给服务。

## 测试

```bash
go test ./...
```

接口测试使用内存中的真实 TLS 服务，能够验证 HTTPS 请求、响应结构、方法限制和安全响应头。

## Docker

```bash
docker build \
  --build-arg APP_ENV=test \
  --build-arg APP_VERSION=local \
  -t onebeat:test .
```

镜像默认从 `/certs/fullchain.pem` 和 `/certs/privkey.pem` 加载 HTTPS 证书，并以非 root
用户运行。

## GitHub CI/CD

`master` 自动构建并发布生产镜像到 `443`，`staging` 自动构建并发布测试镜像到
`8443`。镜像存储在 GitHub Container Registry。Vultr 初始化、GitHub Secrets、证书权限、
验证和回滚步骤见
[部署文档](docs/deployment.md)。
