# GitHub Actions 部署

## 发布路径

- `master`：构建 `prod` 与 `prod-<短 SHA>` 镜像，部署到 Vultr 的 `443`。
- `staging`：构建 `test` 与 `test-<短 SHA>` 镜像，部署到 Vultr 的 `8443`。
- Registry：`ghcr.io/1173774897/onebeatbackend`。
- 服务器目录：`/opt/onebeatbackend`。
- 数据库：一个 PostgreSQL 17 容器，内部隔离为 `onebeat_prod` 和 `onebeat_test` 两个逻辑库。

工作流位于 `.github/workflows/deploy.yml`。GitHub Actions 使用仓库自带的
`GITHUB_TOKEN` 推送 GHCR 镜像，然后通过 SSH 上传对应 Compose 文件并执行
`docker compose up -d --wait`。健康检查未通过时部署任务会失败。

## 本地开发环境

Vultr 已按本文档后续章节跑通后，日常改代码**不必**在本地复刻整台机器（双 API 容器、Certbot、
公网 `443`、GHCR 拉镜像、华为 webhook 公网回调）。本地目标是：与线网共用 **同一套 SQL 迁移**
和 **`onebeat_test` 逻辑库**，在宿主机用 HTTPS API 做快速验证，再推 `staging` 让 Actions 在
Vultr 上跑 **test 镜像** 做与线上一致的最后一跳。

收藏小铺的业务规则、华为接入与秘密含义见 [store-system-design.md](store-system-design.md)；
秘密文件布局见 [deploy/secrets/README.md](../deploy/secrets/README.md)。

### 与 Vultr 对照

| 能力 | Vultr（staging / test） | 本地 Mac |
| --- | --- | --- |
| PostgreSQL 17 + `onebeat_test` | `onebeat-postgres` | `deploy/docker-compose.database.yml` |
| 表结构 | Actions：`migrate up` | `scripts/dev-db-migrate.sh` |
| API | `onebeat-test` 容器，公网 `:8443` | `./scripts/run-api-dev.sh` → `https://localhost:8443` |
| TLS | Let’s Encrypt（`certs/test`） | `scripts/generate-dev-certs.sh` → 仓库根 `certs/dev-*.pem` |
| 数据库连接配置 | `/opt/onebeatbackend/.env.test` | `deploy/.env.local`（`127.0.0.1:5432`） |
| 收藏小铺秘密 | `/opt/onebeatbackend/secrets/test.env` 等 | `deploy/secrets/test.env`（不进 Git） |
| 秘密注入方式 | Compose `env_file` + 挂载 `secrets/test` | `run-api-dev.sh` 内 source 两个 env 文件 |
| `onebeat_prod` | 有 prod 容器与库 | 本地通常不启 prod API；首次起库时会一并创建 prod 库 |

**不要填错位置：** GitHub Actions 的 Repository secrets 仅用于 SSH 部署（本文 §6），**不是**
`deploy/secrets/test.env` 里的 pepper / 华为凭据。

### 配置文件分工

| 文件 | 提交 Git | 内容 |
| --- | --- | --- |
| `deploy/.env.database` | 否 | Postgres 管理员与 `onebeat_prod` / `onebeat_test` 建库密码 |
| `deploy/.env.test` | 否 | 迁移容器用 `DATABASE_URL`（主机名 `onebeat-postgres`） |
| `deploy/.env.local` | 否 | 宿主机 `go run` 用 `DATABASE_URL`（`127.0.0.1`） |
| `deploy/secrets/test.env` | 否 | OneBeat pepper、JWT、华为 Account/IAP（含 `\n` 转义 PEM） |
| `deploy/secrets/test/redemption_codes.json` | 否 | 口令 HMAC 摘要（有活动时再填） |

`bootstrap-local-env.sh` 只生成数据库相关三个 env；`init-local-secrets.sh` 从 example 复制
`test.env` 与口令 JSON 模板。

### 一次性准备

需要 Go 1.22+、OpenSSL，以及 **Docker 引擎 + Compose CLI**（见下节）。在**仓库根目录**执行：

```bash
chmod +x scripts/bootstrap-local-env.sh \
  scripts/dev-db-migrate.sh \
  scripts/generate-dev-certs.sh \
  scripts/init-local-secrets.sh \
  scripts/run-api-dev.sh

# 1) 数据库密码与 deploy/.env.local
./scripts/bootstrap-local-env.sh

# 2) Postgres 容器（仅监听 127.0.0.1:5432）
./scripts/compose.sh -f deploy/docker-compose.database.yml up -d --wait --wait-timeout 90

# 3) 迁移（读取 deploy/.env.test，走 Docker 网络 onebeat-postgres）
./scripts/dev-db-migrate.sh

# 4) 本地 HTTPS 自签名证书（默认 certs/dev-cert.pem、certs/dev-key.pem）
./scripts/generate-dev-certs.sh

# 5) 收藏小铺秘密模板 → 可编辑文件（不覆盖已存在的 test.env）
./scripts/init-local-secrets.sh
```

然后编辑 **`deploy/secrets/test.env`**：补全仍为 `REPLACE_ME` 的项，以及华为 Account/IAP
段（说明见文件内 `#` 注释）。暂不做口令兑换可保持 `redemption_codes.json` 为示例内容。

若已手工维护过 `deploy/.env.*`，勿重复运行 `bootstrap-local-env.sh`；只要
`.env.database` 里 `ONEBEAT_TEST_DB_PASSWORD` 与 `.env.local` 中测试库密码一致即可。

### 日常开发循环

```bash
# 单元测试（不依赖 Docker，也不加载 secrets）
go test ./...

# 需要数据库时：确保 Postgres 在跑，并应用新迁移
./scripts/compose.sh -f deploy/docker-compose.database.yml up -d --wait
./scripts/dev-db-migrate.sh

# 启动 API（脚本打印环境摘要、Postgres 探测、go build -v，再运行 bin/onebeat-api-dev）
./scripts/run-api-dev.sh
```

`run-api-dev.sh` 会 source `deploy/.env.local` 与 `deploy/secrets/test.env`，**不必**手敲
`set -a`。连库与请求日志为应用输出的 JSON（如 `database connection established`）。修改
`test.env` 后重新执行脚本即可；新开终端需再跑一遍。

另开终端验证（自签名证书需 `-k`）：

```bash
curl -k https://localhost:8443/healthz
curl -k https://localhost:8443/api/v1/test
curl -k https://localhost:8443/api/v1/database/test
```

期望：`database` 为 `onebeat_test`，`migrationDirty` 为 `false`。本地默认
`environment` 为 `development`；若要与 staging 一致，在 `deploy/.env.local` 增加
`APP_ENV=test`。

仅排查 HTTP、临时关闭 TLS：

```bash
STORE_API_ADDR=:8080 STORE_API_INSECURE_HTTP=1 ./scripts/run-api-dev.sh
```

### 可选：本地跑 test 容器

与 Vultr 完全一致时，可在 `deploy/` 目录构建镜像并用 `docker-compose.test.yml` 启动；需已存在
`deploy/.env.test`、`deploy/secrets/test.env` 及 `secrets/test/redemption_codes.json`。日常改 Go
代码仍推荐 `run-api-dev.sh`，迭代更快。

### 图形化看库

Postgres 绑定 **`127.0.0.1:5432`**，无需 SSH 隧道。DBeaver / TablePlus：

- Database：`onebeat_test`
- User / Password：`deploy/.env.database` 中的 `ONEBEAT_TEST_DB_*`

Vultr 上同一库按本文 §5.5 经 SSH 隧道访问；勿将服务器 `5432` 开放到公网。

### 何时推 staging

- 改 Go、迁移、Compose 或健康检查：`go test ./...`、上述 curl 通过后，推 `staging`。
- 只改文档或纯客户端：可跳过本地 API。

更短命令清单见 [README.md](../README.md)「本地启动」。

### Docker Compose CLI

本地文档里的 Compose 命令统一写成 `./scripts/compose.sh -f …`，内部优先 `docker compose`（V2
插件），否则回退 `docker-compose`。

若直接运行 `docker compose -f …` 出现 **`unknown shorthand flag: 'f' in -f`**，说明当前只有
独立 `docker` 客户端、**未安装 Compose 插件**（`docker` 会把 `-f` 当成自身参数）。Mac 上推荐：

1. 安装并启动 [Docker Desktop](https://www.docker.com/products/docker-desktop/)（自带引擎 + Compose
   V2）；或
2. `brew install docker-compose`，然后使用 `./scripts/compose.sh` 或 `docker-compose -f …`。

安装后应能执行：

```bash
docker compose version
# 或
docker-compose version
```

且 Docker 守护进程在运行（`docker info` 无 socket 错误）。仅有 Homebrew 的 `docker` CLI、未启动
Desktop 时，也会出现 `connect: no such file or directory` 到 `/var/run/docker.sock`。

若 `dev-db-migrate.sh` 报 **`password authentication failed for user "onebeat_test"`**，且
`docker logs onebeat-postgres` 里曾有 **`01-create-environment-databases.sh: Permission denied`**，
说明首次初始化脚本未执行，卷里只有 `onebeat_admin`、没有 `onebeat_test` 角色。仓库内 init
脚本应为可执行（`chmod 755 deploy/postgres/init/01-create-environment-databases.sh`），然后
清空卷重建（**会删除本地 prod/test 库数据**）：

```bash
./scripts/recreate-local-postgres.sh
```

或手工：`compose.sh … down -v` → `up -d --wait` → `dev-db-migrate.sh`。

## 1. 推送发布分支

当前 `origin` 已经是：

```text
https://github.com/1173774897/oneBeatBackend.git
```

本地当前 `main`、`master` 指向同一提交。先在 `main` 提交本次 CI/CD 变更，再将其快进到
`master`，最后由 `master` 创建测试分支：

```bash
cd /Users/lizhe/work/oneBeatBackend

git switch main
git add .
git commit -m "ci: migrate deployment to GitHub Actions"

git switch master
git merge --ff-only main
git push -u origin master

git switch -c staging master
git push -u origin staging

git switch master
```

如果 `staging` 已经存在，使用：

```bash
git switch staging
git merge --ff-only master
git push -u origin staging
```

确认流水线工作正常后，可在 GitHub `Settings > Branches` 把默认分支改为 `master`。
不要在修改默认分支前删除远程 `main`。

## 2. Vultr 一次性准备

### 2.1 三类权限不要混淆

整个部署过程涉及三类相互独立的身份验证：

| 操作 | 解决的问题 | 凭据保存位置 |
| --- | --- | --- |
| 安装 SSH 公钥 | 允许 GitHub Actions 以 `onebeat` 登录服务器 | `/home/onebeat/.ssh/authorized_keys` |
| 加入 `docker` 组并重新登录 | 允许 `onebeat` 访问服务器 Docker daemon | Linux 用户组，由新登录会话加载 |
| `docker login ghcr.io` | 允许 Docker 拉取私有 GHCR 镜像 | `/home/onebeat/.docker/config.json` |

这三步不能互相替代。root 密码只是 root 的密码，不能拿来登录 `onebeat`。下面创建
`onebeat` 时不设置密码，GitHub Actions 只使用专用 SSH 密钥登录。

### 2.2 安装 Docker 并创建部署用户

先以 root 登录 Vultr，执行：

```bash
curl -fsSL https://get.docker.com | sh
id -u onebeat >/dev/null 2>&1 ||
  useradd --create-home --user-group --shell /bin/bash onebeat
usermod -aG docker onebeat
```

`usermod -aG` 只更新系统组配置，不会改变已经存在的登录会话。`onebeat` 后续第一次通过
SSH 公钥登录时就是一个新会话，因此会自动加载 `docker` 组，无需再额外退出一次。

Docker 组可以控制宿主机上的容器，权限接近 root，只应授予专用部署账号。

### 2.3 把应用移出 `/root`

不要继续使用 `/root/opt/onebeatbackend`。`/root` 通常只有 root 可以进入，即使把最内层目录
改成 `onebeat` 所有，`onebeat` 仍无法穿过父目录。

以 root 执行：

```bash
mkdir -p /opt/onebeatbackend
cp -a /root/opt/onebeatbackend/. /opt/onebeatbackend/
mkdir -p /opt/onebeatbackend/{certs/prod,certs/test,data/prod,data/test}
chown -R onebeat:onebeat /opt/onebeatbackend
```

先验证新目录，再决定是否删除旧目录：

```bash
sudo -u onebeat ls -la /opt/onebeatbackend
```

不要通过放宽 `/root` 权限来解决这个问题。工作流中的 `DEPLOY_ROOT` 已固定为
`/opt/onebeatbackend`。

### 2.4 在开发电脑创建 Actions 专用 SSH 密钥

在 Mac 上执行：

```bash
ssh-keygen \
  -t ed25519 \
  -C "github-actions-onebeat" \
  -f ~/.ssh/onebeat_github_actions \
  -N ""
```

生成两个文件：

| 文件 | 用途 |
| --- | --- |
| `~/.ssh/onebeat_github_actions` | 私钥，只放入 GitHub `SSH_PRIVATE_KEY` Secret |
| `~/.ssh/onebeat_github_actions.pub` | 公钥，安装到服务器 `onebeat` 账号 |

Actions 无法交互输入密钥口令，所以这把专用部署密钥不设置 passphrase。不要把私钥上传到仓库、
服务器应用目录或聊天记录中。

### 2.5 通过 root 为 `onebeat` 安装公钥

仍在 Mac 上执行：

```bash
cat ~/.ssh/onebeat_github_actions.pub | ssh root@<Vultr-IP> '
  install -d -m 700 -o onebeat -g onebeat /home/onebeat/.ssh
  touch /home/onebeat/.ssh/authorized_keys
  cat >> /home/onebeat/.ssh/authorized_keys
  chown onebeat:onebeat /home/onebeat/.ssh/authorized_keys
  chmod 600 /home/onebeat/.ssh/authorized_keys
'
```

外层连接是 `root@<Vultr-IP>`，因此这里输入 root 密码只是为了安装公钥。安装完成后，
`onebeat` 不使用 root 密码，也不需要设置自己的密码。

公钥必须属于 `onebeat:onebeat`；`.ssh` 权限必须是 `700`，`authorized_keys` 必须是
`600`，否则 sshd 可能拒绝密钥。

### 2.6 首次登录并验证 Docker 组

在 Mac 上执行：

```bash
ssh \
  -i ~/.ssh/onebeat_github_actions \
  -o IdentitiesOnly=yes \
  onebeat@<Vultr-IP>
```

登录后执行：

```bash
id
docker ps
```

`id` 输出必须包含 `docker` 组，`docker ps` 不应出现 socket permission denied。如果没有
`docker` 组，以 root 再执行 `usermod -aG docker onebeat`，彻底退出 `onebeat` 会话后重新
登录。`newgrp docker` 只能临时改变当前 shell，不适合作为 Actions 的长期配置。

## 3. 让服务器拉取 GHCR 私有镜像

在 GitHub `Settings > Developer settings > Personal access tokens > Tokens (classic)` 创建用于
服务器的 classic PAT，仅勾选 `read:packages`。GitHub Packages 当前不支持使用 fine-grained
PAT 进行 Docker Registry 登录。

完成上一节的密钥登录后，保持在服务器的 `onebeat` 会话中执行一次：

```bash
printf '%s' '<GitHub PAT>' |
  docker login ghcr.io \
    --username '1173774897' \
    --password-stdin
```

成功后凭据保存在 `/home/onebeat/.docker/config.json`。GitHub Actions 后续也是以
`onebeat` 登录，因此可以直接复用该凭据执行 `docker compose pull`。

不要以 root 执行这一步。root 登录产生的是 `/root/.docker/config.json`，`onebeat` 看不到，
Actions 仍会在拉取私有镜像时收到 unauthorized。

如果把 GHCR package 设置为 Public，服务器可以匿名拉取，此登录步骤可省略。

## 4. 放置 HTTPS 证书

### 4.1 证书由谁生成

本项目不在 GitHub Actions、Docker 镜像或开发电脑中生成生产证书。推荐在 Vultr 服务器上使用
Certbot 完成以下工作：

1. Certbot 在服务器本地生成私钥，私钥保存在 `/etc/letsencrypt`，不会发送给 Let’s Encrypt。
2. Certbot 向 Let’s Encrypt 申请证书；Let’s Encrypt 验证域名确实指向这台服务器后签发证书。
3. 将证书和私钥复制到 `/opt/onebeatbackend/certs`，供容器中的 Go 服务读取。
4. Certbot 定期自动续期；续期成功后，部署钩子重新复制文件并重启容器。

`fullchain.pem` 包含服务器证书和中间证书链，可以公开；`privkey.pem` 是服务器私钥，必须严格
保密，不能提交到 Git、放入 GitHub Secret、上传到镜像或发送给其他人。

下面示例假定使用两个域名：

| 环境 | 示例域名 | 对外端口 |
| --- | --- | --- |
| 生产 | `api.example.com` | `443` |
| 测试 | `api-test.example.com` | `8443` |

执行命令前，必须把示例域名和邮箱替换成实际值。一个证书可以同时包含这两个域名；HTTPS
证书校验域名而不是端口，所以测试域名使用 `8443` 不需要单独类型的证书。

### 4.2 配置域名和防火墙

先在域名 DNS 管理页面创建记录：

```text
api.example.com       A     <Vultr 公网 IPv4>
api-test.example.com  A     <Vultr 公网 IPv4>
```

如果同时存在 `AAAA` 记录，它也必须指向该服务器可用的公网 IPv6；否则应先删除错误的
`AAAA` 记录，避免 Let’s Encrypt 从错误地址进行验证。

在开发电脑上确认 DNS 已生效：

```bash
dig +short api.example.com A
dig +short api-test.example.com A
```

两个命令都应返回 Vultr 公网 IP。然后在 Vultr Firewall 中允许以下 TCP 入站端口：

| 端口 | 用途 |
| --- | --- |
| `22` | GitHub Actions SSH 部署，不要在调整防火墙时误封 |
| `80` | Let’s Encrypt HTTP-01 域名验证，不能改成 `8080` 或 `8443` |
| `443` | 生产 API |
| `8443` | 测试 API |

如果服务器启用了 UFW，还需要以 root 执行：

```bash
# 先保留 SSH，避免启用或重载防火墙后失去服务器访问权限。
ufw allow 22/tcp

# Certbot 的 HTTP-01 验证固定从公网 TCP 80 访问服务器。
ufw allow 80/tcp

# oneBeat 生产和测试 API 的对外端口。
ufw allow 443/tcp
ufw allow 8443/tcp

ufw status
```

HTTP-01 验证必须使用公网端口 `80`。当前 Compose 只占用 `443` 和 `8443`，因此 Certbot
可以临时监听 `80`，无需停止 oneBeat 容器。如果不能开放 `80`，应改用 DNS 服务商 API
支持的 DNS-01 插件；不要使用需要每次手工添加 TXT 记录的方式做生产自动续期。

### 4.3 安装 Certbot 并首次签发证书

以下命令适用于 Ubuntu/Debian。以 root 登录服务器后执行：

```bash
apt-get update
apt-get install -y certbot
certbot --version
```

设置实际域名和接收续期通知的邮箱，然后申请一个同时覆盖生产与测试域名的证书：

```bash
# 这三个值必须替换为真实信息。
PROD_DOMAIN="api.example.com"
TEST_DOMAIN="api-test.example.com"
LE_EMAIL="admin@example.com"

# --standalone：Certbot 临时启动 HTTP 服务监听 80 端口完成域名验证。
# --cert-name：固定证书目录名称，避免续期脚本依赖自动生成的目录名。
# -d 可以重复使用，因此同一张证书可覆盖生产和测试两个域名。
certbot certonly \
  --standalone \
  --preferred-challenges http \
  --cert-name onebeat-api \
  --email "$LE_EMAIL" \
  --agree-tos \
  --no-eff-email \
  -d "$PROD_DOMAIN" \
  -d "$TEST_DOMAIN"
```

签发成功后，Certbot 管理的入口位于：

```text
/etc/letsencrypt/live/onebeat-api/fullchain.pem
/etc/letsencrypt/live/onebeat-api/privkey.pem
```

`live` 目录中的文件是指向 `/etc/letsencrypt/archive` 的软链接，Certbot 会在续期时更新链接
目标。可用下面的命令检查域名、有效期和证书路径：

```bash
certbot certificates
openssl x509 \
  -in /etc/letsencrypt/live/onebeat-api/fullchain.pem \
  -noout -subject -issuer -dates -ext subjectAltName
```

### 4.4 复制证书到 oneBeat 容器目录

生产和测试目录都需要以下实际文件，不要把 Certbot 的软链接直接复制成软链接：

```text
/opt/onebeatbackend/certs/prod/fullchain.pem
/opt/onebeatbackend/certs/prod/privkey.pem
/opt/onebeatbackend/certs/test/fullchain.pem
/opt/onebeatbackend/certs/test/privkey.pem
```

镜像使用 UID/GID `10001` 的非 root 用户读取证书。以 root 执行：

```bash
# 创建容器的证书挂载目录。目录需要可进入，但私钥文件本身仅允许 UID 10001 读取。
install -d -m 0755 -o 10001 -g 10001 \
  /opt/onebeatbackend/certs/prod \
  /opt/onebeatbackend/certs/test

# install 会复制软链接指向的实际证书内容，而不是创建指回 /etc/letsencrypt 的链接。
# fullchain 可以只读公开；privkey 只允许容器内 UID 10001 读取。
for ENVIRONMENT in prod test; do
  install -m 0444 -o 10001 -g 10001 \
    /etc/letsencrypt/live/onebeat-api/fullchain.pem \
    "/opt/onebeatbackend/certs/${ENVIRONMENT}/fullchain.pem"

  install -m 0400 -o 10001 -g 10001 \
    /etc/letsencrypt/live/onebeat-api/privkey.pem \
    "/opt/onebeatbackend/certs/${ENVIRONMENT}/privkey.pem"
done

# 数据目录同样由镜像中的非 root 用户写入。
chown -R 10001:10001 /opt/onebeatbackend/data/prod /opt/onebeatbackend/data/test
```

使用普通 `cp -a` 可能保留 Certbot 的软链接。容器只挂载
`/opt/onebeatbackend/certs/<环境>`，无法访问软链接目标 `/etc/letsencrypt/archive`，因此这里使用
`install` 明确复制真实文件并同时设置所有者和权限。

证书准备好后再执行首次 GitHub Actions 部署。Compose 会分别把 `certs/prod` 和 `certs/test`
只读挂载到容器的 `/certs`。

### 4.5 配置自动续期后的复制和重启

Certbot 续期只会更新 `/etc/letsencrypt`。Go 服务在启动时读取证书，不会自动重新加载；如果
不配置部署钩子，即使 Certbot 续期成功，容器仍可能继续使用旧证书。

以 root 创建部署钩子：

```bash
install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy

cat >/etc/letsencrypt/renewal-hooks/deploy/onebeat-copy-certificates.sh <<'SCRIPT'
#!/bin/sh
set -eu

# 此名称来自首次签发时的 --cert-name onebeat-api。
EXPECTED_LINEAGE="/etc/letsencrypt/live/onebeat-api"

# Certbot 续期时提供 RENEWED_LINEAGE；手工运行脚本时则使用固定目录。
SOURCE_LINEAGE="${RENEWED_LINEAGE:-$EXPECTED_LINEAGE}"

# 如果服务器以后还有其他证书，忽略与 oneBeat 无关的续期事件。
if [ "$SOURCE_LINEAGE" != "$EXPECTED_LINEAGE" ]; then
  exit 0
fi

for ENVIRONMENT in prod test; do
  TARGET="/opt/onebeatbackend/certs/${ENVIRONMENT}"
  install -d -m 0755 -o 10001 -g 10001 "$TARGET"
  install -m 0444 -o 10001 -g 10001 \
    "$SOURCE_LINEAGE/fullchain.pem" "$TARGET/fullchain.pem"
  install -m 0400 -o 10001 -g 10001 \
    "$SOURCE_LINEAGE/privkey.pem" "$TARGET/privkey.pem"
done

# Go 的 TLS 服务在启动时读取证书，因此续期后重启已经存在的容器。
# 首次签发时容器可能尚未创建，此时直接跳过，不让证书签发失败。
for CONTAINER in onebeat-prod onebeat-test; do
  if docker container inspect "$CONTAINER" >/dev/null 2>&1; then
    docker restart "$CONTAINER" >/dev/null
  fi
done
SCRIPT

chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/onebeat-copy-certificates.sh
```

Ubuntu/Debian 的 Certbot 软件包通常使用 systemd timer 定期检查续期。启用并检查它：

```bash
systemctl enable --now certbot.timer
systemctl status certbot.timer
systemctl list-timers certbot.timer
```

测试续期流程和部署钩子：

```bash
# 使用 Let’s Encrypt 测试环境验证续期，不消耗正式签发额度。
certbot renew --dry-run

# 手工运行一次部署钩子，确认复制权限和容器重启逻辑没有错误。
/etc/letsencrypt/renewal-hooks/deploy/onebeat-copy-certificates.sh

# 确认证书文件归 UID/GID 10001 所有，私钥权限为 400。
ls -l /opt/onebeatbackend/certs/{prod,test}/*.pem
```

### 4.6 验证线上证书

容器启动后，在开发电脑执行：

```bash
curl --fail --show-error https://api.example.com/healthz
curl --fail --show-error https://api-test.example.com:8443/healthz

# 查看生产端实际返回证书的签发者、有效期和域名列表。
openssl s_client \
  -connect api.example.com:443 \
  -servername api.example.com \
  </dev/null 2>/dev/null |
  openssl x509 -noout -subject -issuer -dates -ext subjectAltName
```

这里不应使用 `curl -k` 或 `--insecure`；如果必须跳过校验才能访问，说明域名、证书链或服务器
时间仍有问题。Let’s Encrypt 的 HTTP-01 验证固定使用端口 `80`，相关原理可参考
[Let’s Encrypt Challenge Types](https://letsencrypt.org/docs/challenge-types/)；Certbot 的 standalone
模式和续期钩子可参考 [Certbot User Guide](https://eff-certbot.readthedocs.io/en/stable/using.html)。

## 5. 部署 PostgreSQL 与数据库迁移

### 5.1 架构和安全边界

数据库部署由 `deploy/docker-compose.database.yml` 管理：

```text
onebeat-prod ──┐
               ├── onebeat-backend 私有 Docker 网络 ── onebeat-postgres
onebeat-test ──┘                                      ├── onebeat_prod
                                                      └── onebeat_test
```

- 只有一个 `onebeat-postgres` 容器和一个名为 `onebeat-postgres-data` 的持久卷。
- 生产与测试使用不同数据库、不同登录账号和不同密码。
- API 账号不是 PostgreSQL 超级用户，也不能创建数据库或角色。
- 容器间通过 `onebeat-backend` 网络连接，连接地址是 `onebeat-postgres:5432`。
- 宿主机只绑定 `127.0.0.1:5432`，便于 SSH 隧道调试；不要在 Vultr Firewall 或 UFW
  中开放公网 `5432`。
- Docker 内部连接使用 `sslmode=disable`，因为流量不离开同一台服务器的 Docker 网络；从开发
  电脑调试时使用 SSH 加密隧道。

两个逻辑库能防止测试数据误写入生产库，但它们仍共享同一个 PostgreSQL 进程、磁盘和故障域。
这符合当前单机部署规模；以后需要独立扩缩容或更强隔离时，再迁移到两个实例或托管数据库。

### 5.2 在服务器创建数据库密码文件

数据库密码不进入 GitHub Secrets、Git 仓库、Docker 镜像或 Actions 日志。它们只保存在服务器
`/opt/onebeatbackend` 下三个权限为 `600` 的文件中。

在第一次推送包含数据库改动的代码之前，先以 `onebeat` 登录服务器：

```bash
ssh -i ~/.ssh/onebeat_github_actions onebeat@<Vultr-IP>
cd /opt/onebeatbackend

# 新建文件默认只允许当前用户读取，避免密码短暂以 644 权限落盘。
umask 077

# 十六进制密码只包含 URL 安全字符，可直接写入 PostgreSQL URL。
ADMIN_PASSWORD="$(openssl rand -hex 32)"
PROD_PASSWORD="$(openssl rand -hex 32)"
TEST_PASSWORD="$(openssl rand -hex 32)"

cat >.env.database <<EOF
POSTGRES_USER=onebeat_admin
POSTGRES_PASSWORD=${ADMIN_PASSWORD}
POSTGRES_DB=postgres
ONEBEAT_PROD_DB_USER=onebeat_prod
ONEBEAT_PROD_DB_PASSWORD=${PROD_PASSWORD}
ONEBEAT_TEST_DB_USER=onebeat_test
ONEBEAT_TEST_DB_PASSWORD=${TEST_PASSWORD}
EOF

cat >.env.prod <<EOF
DATABASE_URL=postgres://onebeat_prod:${PROD_PASSWORD}@onebeat-postgres:5432/onebeat_prod?sslmode=disable
EOF

cat >.env.test <<EOF
DATABASE_URL=postgres://onebeat_test:${TEST_PASSWORD}@onebeat-postgres:5432/onebeat_test?sslmode=disable
EOF

chmod 600 .env.database .env.prod .env.test

# 清除当前 shell 中的明文变量；文件中的值仍然保留。
unset ADMIN_PASSWORD PROD_PASSWORD TEST_PASSWORD

# 只检查所有者和权限，不要把文件内容输出到 Actions 或工单。
ls -l .env.database .env.prod .env.test
```

三个文件应属于 `onebeat`，权限显示为 `-rw-------`。示例文件
`deploy/.env.database.example`、`deploy/.env.prod.example` 和 `deploy/.env.test.example` 只用于说明
格式，不能直接用于生产。

`onebeat_admin` 仅用于数据库初始化、维护和备份。API 只会收到对应环境 `.env` 中的
`DATABASE_URL`，不会拿到管理员密码。

### 5.3 首次启动数据库

服务器密码文件准备好后，优先向 `staging` 推送并观察测试环境：

```bash
git switch staging
git push origin staging
```

新的 Actions 部署顺序是：

1. 构建并推送 API 镜像。
2. 上传数据库 Compose、初始化脚本、当前环境 Compose 和全部迁移文件。
3. 验证服务器存在 `.env.database` 与当前环境的 `.env.test` 或 `.env.prod`。
4. 启动并等待 `onebeat-postgres` 健康。
5. 使用 `golang-migrate` 对当前环境数据库执行所有待执行的 `up` 迁移。
6. 迁移成功后才启动或更新 API，并等待包含数据库检查的 `/healthz` 返回成功。

第一次启动且数据卷为空时，官方 PostgreSQL 镜像会执行
`postgres/init/01-create-environment-databases.sh`，一次性创建两个账号和两个数据库。初始化
脚本只会对空数据目录执行；容器重启或重新创建不会清空数据库，也不会再次执行脚本。

如果需要在 Actions 之外手工启动，可在服务器执行：

```bash
cd /opt/onebeatbackend

docker compose \
  -f docker-compose.database.yml \
  up -d --wait --wait-timeout 90

# 先用测试环境验证迁移。
docker compose \
  -f docker-compose.test.yml \
  --profile migration \
  run --rm migrate

docker compose \
  -f docker-compose.test.yml \
  up -d --remove-orphans --wait --wait-timeout 60
```

不要为“重新运行初始化脚本”执行 `docker volume rm onebeat-postgres-data`。删除该卷会永久删除
生产和测试数据。如果数据卷已经存在但数据库未正确创建，应先检查日志并手工修复，不要删除卷。

### 5.4 验证容器、数据库和接口

在服务器检查容器和逻辑库：

```bash
docker ps --filter name=onebeat
docker logs --tail 100 onebeat-postgres

docker exec onebeat-postgres \
  psql -U onebeat_admin -d postgres \
  -c "SELECT datname FROM pg_database WHERE datname IN ('onebeat_prod', 'onebeat_test') ORDER BY datname;"

docker exec onebeat-postgres \
  psql -U onebeat_admin -d onebeat_test \
  -c "SELECT version, dirty FROM schema_migrations;"
```

预期能看到两个数据库，并且测试库迁移版本为 `2`、`dirty` 为 `false`。随后在开发电脑检查：

```bash
curl --fail https://api-test.example.com:8443/healthz
curl --fail https://api-test.example.com:8443/api/v1/database/test
```

数据库测试接口不会返回密码或主机地址，成功响应类似：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "status": "connected",
    "database": "onebeat_test",
    "user": "onebeat_test",
    "serverTime": "2026-09-29T12:00:00Z",
    "migrationVersion": 2,
    "migrationDirty": false
  }
}
```

生产部署后，`database` 和 `user` 应分别是 `onebeat_prod`。

### 5.5 使用 DBeaver、DataGrip 或 TablePlus 调试

不要把 PostgreSQL 的 `5432` 暴露到公网。Compose 已将它绑定到服务器回环地址
`127.0.0.1:5432`，图形化工具通过 SSH 隧道即可访问。

以 DBeaver/DataGrip 为例，新建 PostgreSQL 数据源：

| 配置区域 | 字段 | 测试环境填写值 |
| --- | --- | --- |
| 数据库/Main | Host | `127.0.0.1` |
| 数据库/Main | Port | `5432` |
| 数据库/Main | Database | `onebeat_test` |
| 数据库/Main | Username | `onebeat_test` |
| 数据库/Main | Password | 服务器 `.env.database` 中对应的测试密码 |
| SSH Tunnel | Host | Vultr IP 或 SSH 域名 |
| SSH Tunnel | Port | 服务器 SSH 端口，通常为 `22` |
| SSH Tunnel | User | `onebeat` |
| SSH Tunnel | Authentication | 个人 SSH 私钥 |

日常人工访问建议为开发者单独创建带口令的 SSH 密钥，不要长期复用无口令的 GitHub Actions
部署私钥。GUI 建立隧道后，数据库看到的连接来自服务器本机，不要求开放任何新防火墙端口。

不支持内置 SSH 隧道的工具，可先在 Mac 终端执行：

```bash
ssh \
  -i ~/.ssh/<个人私钥> \
  -N \
  -L 15432:127.0.0.1:5432 \
  onebeat@<Vultr-IP>
```

保持该终端运行，然后让 GUI 连接：

```text
Host:     127.0.0.1
Port:     15432
Database: onebeat_test
Username: onebeat_test
Password: <TEST_PASSWORD>
```

优先在 `onebeat_test` 调试。生产账号当前是生产库所有者，误执行写操作会直接修改业务数据；
访问生产库前先做备份，并在 GUI 中关闭自动提交或手动开启只读事务。业务进入正式运营后，建议再
创建专门的生产只读诊断账号。

### 5.6 使用 golang-migrate 管理表结构

迁移文件位于仓库 `migrations/`，每个版本必须有一对文件：

```text
000001_create_store_metadata.up.sql
000001_create_store_metadata.down.sql
000002_create_store_domain.up.sql
000002_create_store_domain.down.sql
```

规则：

1. 已经在共享环境执行过的迁移文件不可修改或重命名；修正结构必须新增下一个版本。
2. `up` 描述升级，`down` 只回退该版本自己的变更。
3. 优先使用可短时间完成、可回退的 DDL；大表变更需要单独设计上线方案。
4. Actions 只自动执行 `up`，绝不自动执行 `down` 或 `force`。
5. 生产迁移前先备份，并先在 `onebeat_test` 执行同一组迁移。

#### 本次 IAP 表重构的一次性例外

项目尚未上线且旧测试数据明确不保留，因此当前分支直接重写了
`000002_create_store_domain.*.sql`。如果服务器上的 `onebeat_test` 或 `onebeat_prod` 已执行过旧版
`000002`，普通 `migrate up` 会看到版本仍是 `2` 并跳过，**不会**自动换成新表结构。首次部署本次
重构时必须逐个环境重建逻辑库；完成后 `000002` 恢复为不可修改的已发布迁移。

先在测试环境执行。确认没有需要保留的数据并完成备份后，在服务器停止测试 API：

```bash
cd /opt/onebeatbackend
docker compose -f docker-compose.test.yml down

docker exec onebeat-postgres \
  psql -U onebeat_admin -d postgres \
  -c "DROP DATABASE onebeat_test WITH (FORCE);"

docker exec onebeat-postgres \
  psql -U onebeat_admin -d postgres \
  -c "CREATE DATABASE onebeat_test OWNER onebeat_test;"
```

随后推送 `staging`。Actions 会上传当前迁移、从版本 0 执行到版本 2，再启动测试 API。按 5.4 验证
迁移版本、接口和沙盒购买/退款流程。测试通过后，生产环境采用同样的受控窗口：

```bash
cd /opt/onebeatbackend
docker compose -f docker-compose.prod.yml down

docker exec onebeat-postgres \
  psql -U onebeat_admin -d postgres \
  -c "DROP DATABASE onebeat_prod WITH (FORCE);"

docker exec onebeat-postgres \
  psql -U onebeat_admin -d postgres \
  -c "CREATE DATABASE onebeat_prod OWNER onebeat_prod;"
```

再推送 `master` 让 Actions 迁移并启动生产 API 与对账 worker。以上命令会永久删除目标逻辑库，
只适用于本次“未上线、无需保留旧数据”的明确前提；以后禁止用重建数据库代替正常增量迁移。

手工查看测试库版本：

```bash
cd /opt/onebeatbackend

docker compose \
  -f docker-compose.test.yml \
  --profile migration \
  run --rm migrate \
  'exec migrate -path=/migrations -database "$DATABASE_URL" version'
```

手工执行全部待迁移版本：

```bash
docker compose \
  -f docker-compose.test.yml \
  --profile migration \
  run --rm migrate
```

如迁移中断并显示 `Dirty database version`，先检查失败 SQL 和数据库实际结构。只有确认结构已经
人工恢复到某个准确版本后，才能使用 `force <版本>` 修复迁移标记；不要把 `force` 当作普通
重试命令，也不要在不了解当前结构时执行 `down`。

golang-migrate 的镜像固定为 `migrate/migrate:v4.19.1`，避免 `latest` 更新造成不可预测变化。

### 5.7 生产 IAP 对账 worker

生产 Compose 会随 API 一起启动 `onebeat-iap-reconciler`。它使用相同镜像和生产数据库，但入口为
`/app/onebeat-reconciler`，不会监听公网端口。测试/沙盒环境不启动该 worker，因为 Huawei 的
`trade/orders/query` 不返回沙盒订单和 0 元订单。

worker 的固定行为：

1. 首次上线默认 `IAP_RECONCILIATION_OBSERVE_ONLY=1`，只记录脱敏交易摘要和独立观察 checkpoint，
   不写交易、订阅或权益表。
2. 每天按 `Asia/Shanghai` 自然日处理前一个完整自然日，单个查询窗口为 24 小时，满足 Huawei
   “不超过 48 小时”的限制。
3. 循环读取 `continuationToken` 直到为空；每页成功后才保存分页 checkpoint。
4. 失败窗口 15 分钟后重试；容器停机跨过多个自然日时，从最后完成窗口继续逐日追赶。
5. 正式任务、历史回补及各自的观察任务使用独立 checkpoint 和 PostgreSQL advisory lock，
   多实例不会重复占有同一个任务。
6. 只处理 Huawei 最近 180 天内可查询的数据；180 天前的漏单无法通过该接口恢复，必须依赖已经
   保存的关键事件通知和人工审计。

服务器 `/opt/onebeatbackend/.env.prod` 可配置：

```dotenv
IAP_RECONCILIATION_TIMEZONE=Asia/Shanghai
IAP_RECONCILIATION_OBSERVE_ONLY=1
IAP_BACKFILL_ENABLED=0
IAP_BACKFILL_DAYS=180
```

首次部署保持观察模式，检查日志里的 `tradeType`、`productId`、`purchaseOrderId`、环境和发生时间，
并与 Huawei 后台抽样核对；日志不会输出 purchase token。确认真实响应结构和分页均正确后，把
`IAP_RECONCILIATION_OBSERVE_ONLY` 改为 `0`。观察任务与正式任务使用不同 job name，正式任务不会误把
“已观察”当成“已落库”。

切换正式模式时，若要补齐观察期间或更早的生产订单，先备份数据库，再同时把
`IAP_BACKFILL_ENABLED` 临时改为 `1` 并重建 worker：

```bash
cd /opt/onebeatbackend

docker compose \
  -f docker-compose.prod.yml \
  up -d --force-recreate iap-reconciler

docker logs -f --tail 200 onebeat-iap-reconciler
```

历史回补按“从近到远”执行，完成后 checkpoint 会阻止同一历史窗口重复产生业务记录。随后保持
`IAP_RECONCILIATION_OBSERVE_ONLY=0`，把 `IAP_BACKFILL_ENABLED` 改回 `0` 并再次重建 worker。不要在
沙盒上用此任务验收；沙盒应验证关键事件通知、`QuerySubscription` 和恢复购买链路。

检查进度：

```bash
docker exec onebeat-postgres \
  psql -U onebeat_admin -d onebeat_prod \
  -c "SELECT job_name, direction, window_start, window_end, page_number, status, last_success_at, last_error FROM iap_reconciliation_checkpoints ORDER BY job_name;"
```

`status=FAILED` 时先看 worker 日志和 `last_error`。不要手工改 continuation token；修复网络或凭据后，
worker 会从已保存页继续。若确认 Huawei 已使 token 永久失效，才在备份后由开发人员执行受控 reset。

### 5.8 备份

单容器意味着生产和测试共享同一个持久卷，但备份应按逻辑库分别执行。在服务器上创建只有
`onebeat` 可访问的备份目录：

```bash
install -d -m 0700 /opt/onebeatbackend/backups

docker exec onebeat-postgres \
  pg_dump -U onebeat_admin -d onebeat_prod -Fc \
  >"/opt/onebeatbackend/backups/onebeat_prod_$(date +%Y%m%d_%H%M%S).dump"

docker exec onebeat-postgres \
  pg_dump -U onebeat_admin -d onebeat_test -Fc \
  >"/opt/onebeatbackend/backups/onebeat_test_$(date +%Y%m%d_%H%M%S).dump"

ls -lh /opt/onebeatbackend/backups
```

备份文件仍在同一台 Vultr 上，不足以应对整机或磁盘故障。商店正式存放订单等关键数据前，应把
加密备份定期同步到独立对象存储，并定期在临时数据库执行恢复演练。

PostgreSQL 官方镜像只会在空数据目录运行 `/docker-entrypoint-initdb.d`，相关行为见
[PostgreSQL Docker Official Image](https://hub.docker.com/_/postgres)；迁移文件命名与 CLI 用法见
[golang-migrate](https://github.com/golang-migrate/migrate)。

## 6. 配置 GitHub Actions Secrets

在仓库 `Settings > Secrets and variables > Actions` 中创建 Repository secrets：

| Secret | 说明 |
| --- | --- |
| `SSH_PRIVATE_KEY` | `~/.ssh/onebeat_github_actions` 的完整私钥内容，不是 `.pub` 文件 |
| `SSH_KNOWN_HOSTS` | 在可信环境执行 `ssh-keyscan -H <Vultr IP>` 得到的内容 |
| `SSH_HOST` | Vultr IP 或域名 |
| `SSH_PORT` | 可选，未设置时默认 `22` |

工作流推送 GHCR 使用 GitHub 自动提供的 `GITHUB_TOKEN`，不用额外配置 Registry 密码。
数据库密码不放入 GitHub Secrets；它们由服务器本地的 `.env.database`、`.env.prod` 和
`.env.test` 提供。

建议在 `Settings > Environments` 创建 `production` 和 `testing`。如果生产发布需要人工确认，
给 `production` 环境配置 Required reviewers；工作流本身无需修改。

## 7. 首次发布与验证

```bash
git switch staging
git push origin staging

git switch master
git push origin master
```

在 GitHub 仓库的 `Actions` 页面查看 `Build and deploy oneBeat`。发布后验证：

```bash
curl https://api.example.com/api/v1/test
curl https://api-test.example.com:8443/api/v1/test
curl https://api.example.com/api/v1/database/test
curl https://api-test.example.com:8443/api/v1/database/test

ssh onebeat@<Vultr IP> 'docker ps'
ssh onebeat@<Vultr IP> 'docker inspect --format "{{.State.Health.Status}}" onebeat-prod'
ssh onebeat@<Vultr IP> 'docker inspect --format "{{.State.Health.Status}}" onebeat-test'
```

响应中的 `environment` 应分别为 `prod` 与 `test`，`version` 应等于本次提交短 SHA。

## 8. 回滚

版本标签不会随滚动标签覆盖。回滚生产到 `prod-abc1234`：

```bash
cd /opt/onebeatbackend
ONEBEAT_IMAGE=ghcr.io/1173774897/onebeatbackend:prod-abc1234 \
  docker compose -f docker-compose.prod.yml up -d --wait --wait-timeout 60
```

测试环境同理，把标签改为 `test-<短 SHA>` 并使用 `docker-compose.test.yml`。

应用镜像回滚不会自动回退数据库结构。只有对应旧版本应用确实无法兼容新结构，并且已经完成
数据库备份与回退评审时，才单独执行目标迁移的 `down`。
