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
    "scheme": "https"
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
