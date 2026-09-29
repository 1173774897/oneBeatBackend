# GitHub Actions 部署

## 发布路径

- `master`：构建 `prod` 与 `prod-<短 SHA>` 镜像，部署到 Vultr 的 `443`。
- `staging`：构建 `test` 与 `test-<短 SHA>` 镜像，部署到 Vultr 的 `8443`。
- Registry：`ghcr.io/1173774897/onebeatbackend`。
- 服务器目录：`/opt/onebeatbackend`。

工作流位于 `.github/workflows/deploy.yml`。GitHub Actions 使用仓库自带的
`GITHUB_TOKEN` 推送 GHCR 镜像，然后通过 SSH 上传对应 Compose 文件并执行
`docker compose up -d --wait`。健康检查未通过时部署任务会失败。

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

## 5. 配置 GitHub Actions Secrets

在仓库 `Settings > Secrets and variables > Actions` 中创建 Repository secrets：

| Secret | 说明 |
| --- | --- |
| `SSH_PRIVATE_KEY` | `~/.ssh/onebeat_github_actions` 的完整私钥内容，不是 `.pub` 文件 |
| `SSH_KNOWN_HOSTS` | 在可信环境执行 `ssh-keyscan -H <Vultr IP>` 得到的内容 |
| `SSH_HOST` | Vultr IP 或域名 |
| `SSH_PORT` | 可选，未设置时默认 `22` |

工作流推送 GHCR 使用 GitHub 自动提供的 `GITHUB_TOKEN`，不用额外配置 Registry 密码。

建议在 `Settings > Environments` 创建 `production` 和 `testing`。如果生产发布需要人工确认，
给 `production` 环境配置 Required reviewers；工作流本身无需修改。

## 6. 首次发布与验证

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

ssh onebeat@<Vultr IP> 'docker ps'
ssh onebeat@<Vultr IP> 'docker inspect --format "{{.State.Health.Status}}" onebeat-prod'
ssh onebeat@<Vultr IP> 'docker inspect --format "{{.State.Health.Status}}" onebeat-test'
```

响应中的 `environment` 应分别为 `prod` 与 `test`，`version` 应等于本次提交短 SHA。

## 7. 回滚

版本标签不会随滚动标签覆盖。回滚生产到 `prod-abc1234`：

```bash
cd /opt/onebeatbackend
ONEBEAT_IMAGE=ghcr.io/1173774897/onebeatbackend:prod-abc1234 \
  docker compose -f docker-compose.prod.yml up -d --wait --wait-timeout 60
```

测试环境同理，把标签改为 `test-<短 SHA>` 并使用 `docker-compose.test.yml`。
