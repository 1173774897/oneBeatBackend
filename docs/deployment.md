# GitHub Actions 部署

## 发布路径

- `master`：构建 `prod` 与 `prod-<短 SHA>` 镜像，部署到 Vultr 的 `443`。
- `staging`：构建 `test` 与 `test-<短 SHA>` 镜像，部署到 Vultr 的 `8443`。
- Registry：`ghcr.io/1173774897/onebeatbackend`。
- 服务器目录：`/opt/onebeat`。

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

以下示例假设 GitHub Actions 使用 `myapp` 用户登录；使用 `root` 时请相应调整所有权。

```bash
curl -fsSL https://get.docker.com | sh
sudo useradd --create-home --shell /bin/bash myapp || true
sudo usermod -aG docker myapp
sudo mkdir -p /opt/onebeat/{certs/prod,certs/test,data/prod,data/test}
sudo chown -R myapp:myapp /opt/onebeat
```

重新登录一次，让 `docker` 用户组生效。

## 3. 让服务器拉取 GHCR 私有镜像

在 GitHub `Settings > Developer settings > Personal access tokens > Tokens (classic)` 创建用于
服务器的 classic PAT，仅勾选 `read:packages`。GitHub Packages 当前不支持使用 fine-grained
PAT 进行 Docker Registry 登录。

用与 `SSH_USER` 相同的服务器用户执行一次：

```bash
printf '%s' '<GitHub PAT>' |
  docker login ghcr.io \
    --username '1173774897' \
    --password-stdin
```

如果把 GHCR package 设置为 Public，服务器可以匿名拉取，此登录步骤可省略。

## 4. 放置 HTTPS 证书

生产和测试目录都需要以下实际文件，不要只复制 Certbot 的失效软链接：

```text
/opt/onebeat/certs/prod/fullchain.pem
/opt/onebeat/certs/prod/privkey.pem
/opt/onebeat/certs/test/fullchain.pem
/opt/onebeat/certs/test/privkey.pem
```

镜像使用 UID/GID `10001` 的非 root 用户读取证书：

```bash
sudo chown 10001:10001 /opt/onebeat/certs/{prod,test}/*.pem
sudo chmod 400 /opt/onebeat/certs/{prod,test}/privkey.pem
sudo chmod 444 /opt/onebeat/certs/{prod,test}/fullchain.pem
sudo chown -R 10001:10001 /opt/onebeat/data/{prod,test}
```

续签证书后需要再次复制、设置权限并重启对应容器。

## 5. 配置 GitHub Actions Secrets

在仓库 `Settings > Secrets and variables > Actions` 中创建 Repository secrets：

| Secret | 说明 |
| --- | --- |
| `SSH_PRIVATE_KEY` | CI 专用 SSH 私钥完整内容 |
| `SSH_KNOWN_HOSTS` | 在可信环境执行 `ssh-keyscan -H <Vultr IP>` 得到的内容 |
| `SSH_HOST` | Vultr IP 或域名 |
| `SSH_USER` | `myapp` 或 `root` |
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

ssh myapp@<Vultr IP> 'docker ps'
ssh myapp@<Vultr IP> 'docker inspect --format "{{.State.Health.Status}}" onebeat-prod'
ssh myapp@<Vultr IP> 'docker inspect --format "{{.State.Health.Status}}" onebeat-test'
```

响应中的 `environment` 应分别为 `prod` 与 `test`，`version` 应等于本次提交短 SHA。

## 7. 回滚

版本标签不会随滚动标签覆盖。回滚生产到 `prod-abc1234`：

```bash
cd /opt/onebeat
ONEBEAT_IMAGE=ghcr.io/1173774897/onebeatbackend:prod-abc1234 \
  docker compose -f docker-compose.prod.yml up -d --wait --wait-timeout 60
```

测试环境同理，把标签改为 `test-<短 SHA>` 并使用 `docker-compose.test.yml`。
