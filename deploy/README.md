# 部署与运维

Go 镜像内含静态网页，Python 执行研究；启用账号、历史和缓存后，还需要 MySQL、Redis。前端不单独构建，也不需要在服务器安装 Node.js、Go 或 Python。

优先使用**已有离线部署包**；它包含四个镜像和运行配置，服务器只导入并启动。需要从源码生成新版时，在开发机构建镜像。以下默认目标为 **Linux x86_64 / amd64**，并复用服务器已有的 Nginx / Caddy。

```text
浏览器 → 已有反向代理 / HTTPS → 127.0.0.1:18080 → Go → Python → 模型与 Tavily
                                                  ↓      ↓
                                                MySQL   Redis
```

Go 也使用 Redis 缓存任务状态。MySQL、Redis 和 Python 不映射宿主机端口。当前研究是同步请求，代理读取超时需要覆盖 Go 的 310 秒预算；队列和 SSE 尚未实现。

## 服务器准备

服务器需要 Docker Engine 和 Compose 插件；安装步骤见 [Ubuntu](https://docs.docker.com/engine/install/ubuntu/) 或 [Debian](https://docs.docker.com/engine/install/debian/) 官方文档。确认服务可用和 CPU 架构：

```bash
docker version
docker compose version
uname -m
```

`x86_64` 对应 `linux/amd64`，`aarch64` 对应 `linux/arm64`，镜像必须匹配服务器。Docker 命令以下假设当前用户有访问权限。运行研究时，服务器仍需访问模型服务和 Tavily；离线包只免除构建和下载镜像。

## 使用已有离线包

本地生成的包位于 `dist/`，不上传 GitHub；它对应生成时的代码，修改源码不会自动更新包内镜像。2026-10-19 的含登录与历史页面版本为：

```text
ai-research-platform-v4-ui-amd64-20261019.tar.gz
```

它与 `docker image save` 直接导出的镜像 `.tar` 不同：外层 `.tar.gz` 还包含运行配置和安装脚本。**外层包先用 `tar` 解压，内部 `images.tar` 才交给 Docker 导入。**

包内文件：

| 文件 | 用途 |
| --- | --- |
| `images.tar` | Go（含网页）、Python、MySQL、Redis 四个 amd64 镜像 |
| `compose.yaml` | 直接运行镜像，没有服务器构建步骤 |
| `.env` / `agent.env` | 数据服务、JWT 与模型 / 搜索配置 |
| `install.sh` | 校验、导入、备份已有同项目数据库、启动和健康检查 |
| `SHA256SUMS` | 包内文件校验和 |
| `verification.json` | 构建时的验证记录 |
| `README.txt` | 该版本的安装说明 |

本机包已配置真实凭据，传输给自己的服务器即可，不要公开分享或上传 GitHub。镜像本身不含这些运行时凭据。

在**本机项目根目录**执行，替换 `SERVER_IP`：

```bash
scp dist/ai-research-platform-v4-ui-amd64-20261019.tar.gz root@SERVER_IP:/tmp/
```

在**服务器**执行：

```bash
mkdir -p /opt/ai-research-releases
tar -xzf /tmp/ai-research-platform-v4-ui-amd64-20261019.tar.gz -C /opt/ai-research-releases
cd /opt/ai-research-releases/ai-research-platform-v4-ui-amd64-20261019
bash install.sh
```

安装脚本使用 `--no-build --pull never`，保留项目名 `ai-research-platform` 与 MySQL 数据卷。已有同项目 MySQL 正在运行时，先验证密码并备份；密码不匹配会停止，不替换服务。已有反向代理继续指向 `http://127.0.0.1:18080`。若域名代理尚未配置，按下文添加。

安装完成后，留在解压目录中定义便捷命令，后续运维使用该目录的配置：

```bash
dc() { docker compose --env-file .env -f compose.yaml "$@"; }
dc ps
curl http://127.0.0.1:18080/health
```

不要混用旧源码目录 `/opt/ai-research-platform` 中的 Compose 来管理新包。重复上传版本时保留已使用的配置与备份；安装成功后仍保留一个可回退的完整部署包。

## 从源码构建新版镜像

以下是一条独立的源码发布流程，适合后续修改代码后重新构建。使用根目录 Compose 和 `deploy/` 的扩展文件；它不自动生成上面的完整安装包。

### 配置文件

在开发机和服务器的项目根目录，首次创建配置：

```bash
test -f deploy/.env || cp deploy/.env.example deploy/.env
test -f agent-service/.env || cp agent-service/.env.example agent-service/.env
chmod 600 deploy/.env agent-service/.env
```

| 文件 | 读取方 | 配置内容 |
| --- | --- | --- |
| `deploy/.env` | Docker Compose | MySQL / Redis 密码、JWT 密钥、并发与频率限制、缓存 TTL、可选域名 |
| `agent-service/.env` | Python | 模型 Key、完整 Base URL、Tavily Key、关闭外部 tracing |

将示例占位值改为真实配置。初次部署建议分别用 `openssl rand -hex 32` 生成密码和密钥，避免 URL / DSN 的特殊字符；`JWT_SECRET` 至少 32 字节。升级已有数据库时保留原密码，修改 `.env` 不会自动修改数据库账号密码。

`MYSQL_PASSWORD`、`MYSQL_ROOT_PASSWORD`、`REDIS_PASSWORD` 和 `JWT_SECRET` 是内部服务配置，不是网站账号。网站账号在页面自行注册。模型服务需要支持当前代码中的 `deepseek-flash`。

### 本机构建并导出四个镜像

在**开发机项目根目录**执行；Apple 芯片 Mac 也需要明确指定 `linux/amd64`：

```bash
docker buildx build --platform linux/amd64 --load \
  -t ai-research-platform-backend:latest -f deploy/backend.Dockerfile .
docker buildx build --platform linux/amd64 --load \
  -t ai-research-platform-agent-service:latest -f deploy/agent-service.Dockerfile .
docker pull --platform linux/amd64 mysql:8.4
docker pull --platform linux/amd64 redis:7.4-alpine

mkdir -p dist
docker image save --platform linux/amd64 -o dist/ai-research-images.tar \
  ai-research-platform-backend:latest \
  ai-research-platform-agent-service:latest \
  mysql:8.4 redis:7.4-alpine
scp dist/ai-research-images.tar root@SERVER_IP:/tmp/
```

需要支持 `docker image save --platform` 的 Docker 版本。构建时遇到 builder 连接错误，可用 `docker buildx ls` 查看；本机 Docker Desktop 可在构建命令中明确加 `--builder desktop-linux`。构建在本机依次进行，镜像源和依赖下载仍需要网络。

将同一版本的源文件同步到服务器项目目录；若用 GitHub 更新，配置文件需要单独保留或安全传输，因为 Git 不包含真实 `.env`。上面的镜像名称匹配根 Compose 的项目名，使用此流程时保留 `ai-research-platform`，不要另设项目名。

### 服务器导入与启动

在服务器的**源码项目根目录**执行，例如 `/opt/ai-research-platform`：

```bash
cd /opt/ai-research-platform
docker image load -i /tmp/ai-research-images.tar

dc() {
  docker compose --env-file deploy/.env \
    -f compose.yaml \
    -f deploy/compose.data.yaml \
    -f deploy/compose.existing-proxy.yaml "$@"
}
dc config --quiet
dc up -d --no-build --pull never --wait --wait-timeout 240
dc ps
curl http://127.0.0.1:18080/health
```

这是完整 V3/V4 配置：四个服务，账号、个人历史和两种缓存均启用。`config --quiet` 只校验配置；不要把包含凭据的完整 `config` 输出公开。更新已有数据库前按下文备份，再执行 `up`。普通 `restart` 不会加载新环境变量，应使用 `up -d --no-build --pull never`。

### 独立使用 Caddy

只有服务器没有其他入口占用 80 / 443 时才需要项目自带的 Caddy。设置 `deploy/.env` 的 `SITE_ADDRESS=research.example.com`，去掉 `compose.existing-proxy.yaml`：

```bash
docker compose --env-file deploy/.env \
  -f compose.yaml -f deploy/compose.data.yaml \
  up -d --no-build --wait --wait-timeout 240
```

该方式额外需要 `caddy:2-alpine` 镜像，默认可由服务器下载；如果服务器不能下载，也在本机拉取对应架构并一起导出。`SITE_ADDRESS=http://localhost` 仅用于 HTTP 预览。Caddy 证书保存在 `caddy_data` 卷中。

## 接入现有域名和反向代理

给新子域名添加服务器公网 IP 的 DNS 记录，保持已有网站配置。只有服务器已具备可用 IPv6 时才添加 AAAA。公网放行 80 / 443 及原有 SSH 管理端口，不需要放行 18080、8000、3306、6379。

宿主机 Nginx 可新增独立站点，以下为 HTTP 配置示例：

```nginx
server {
    listen 80;
    server_name research.example.com;

    location / {
        proxy_pass http://127.0.0.1:18080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 5s;
        proxy_read_timeout 320s;
        proxy_send_timeout 320s;
    }
}
```

替换域名，运行 `nginx -t` 后按现有方式重新加载。沿用服务器当前的证书工具或面板，为新子域名单独配置 HTTPS 和续期。已有 HTTPS 站点只需核对代理目标和超时，无需重复配置。

宿主机 Caddy 可以追加：

```caddyfile
research.example.com {
    reverse_proxy 127.0.0.1:18080 {
        transport http {
            response_header_timeout 320s
            read_timeout 320s
            write_timeout 320s
        }
    }
}
```

若反向代理本身在容器中，`127.0.0.1` 指向代理容器自身，需要通过共享 Docker 网络等方式访问 Go，不能直接套用宿主机地址。前置 CDN 也可能有更短的等待上限。

## 检查、日志与更新

以下 `dc` 使用你所选部署方式中定义的函数，并保持在对应目录：

```bash
dc ps
dc logs --tail=100 backend agent-service mysql redis
curl http://127.0.0.1:18080/health
dc exec -T agent-service python -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8000/health").read().decode())'
```

Go 健康响应应为 `status=ok`、`mysql=ok`、`redis=ok`，Python 应为 `status=ok`、`redis=ok`。Redis 故障时可以降级；MySQL 不可用则无法正常保存账号历史。`/ping` 只检查 Go 进程，健康接口也不会验证模型 Key。

打开网站后注册登录，提交一次快速问答和深入研究，查看「我的历史」，刷新后重新打开报告。真实研究会产生模型与搜索用量；页面显示「服务已连接」只说明 Go 可访问。

更新前先备份；代码或网页改动需要重建对应镜像，服务器重新 `load` 后执行 `dc up -d --no-build --pull never --wait --wait-timeout 240`。仅调整环境变量无需重建镜像。更新会中断正在执行的同步请求；过期任务会记录为失败，不会自动续跑。

停止与再次启动：

```bash
dc down
dc up -d --no-build --pull never --wait --wait-timeout 240
```

保留数据卷，不使用 `down -v`。回退时使用上一个完整发布版本的镜像和配置；若数据库迁移发生变化，还需确认旧代码兼容，必要时在维护窗口恢复对应备份。

## 数据库备份与恢复

在正在使用的部署目录定义好 `dc` 后，备份到仅当前用户可访问的文件：

```bash
mkdir -p backups
chmod 700 backups
umask 077
backup_file="backups/research-$(date +%Y%m%d-%H%M%S).sql"
dc exec -T mysql sh -c \
  'MYSQL_PWD="$MYSQL_PASSWORD" exec mysqldump -u research --single-transaction --no-tablespaces research' \
  > "$backup_file.partial" && mv "$backup_file.partial" "$backup_file"
```

保留命令成功生成的 `.sql` 文件，并定期复制到服务器以外的位置。完整离线包的安装脚本也会在识别到正在运行的同项目 MySQL 时自动执行备份。Redis 当前只保存缓存，不是历史记录的持久化来源。

恢复会覆盖备份中涉及的表，应先保留当前备份、确认目标数据库和版本，并停止应用写入。在维护窗口执行，替换为已核验的备份文件：

```bash
dc stop backend agent-service
dc exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql -u research research' \
  < backups/verified-backup.sql
```

确认导入成功后才重新启动应用：

```bash
dc up -d --no-build --pull never --wait --wait-timeout 240
```

## 旧镜像与归档清理

**先启动并验证新版本，再清理旧版本。** `docker load` 只导入镜像，已有容器需要 `dc up` 才会切换；删除镜像不会代替更新容器。

```bash
docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
docker image ls
```

保留当前运行版本与一个可用的回退版本；仅用 `docker image rm 镜像名:旧标签` 删除已确认不用的项目镜像，或用旧镜像 ID 删除被新同名标签替代的镜像。不加 `--force`，也不批量删除其他项目的镜像、容器和卷。

服务器 `/tmp/ai-research-images.tar` 等中转文件在导入、启动成功且另有发布包留存后可删除。`dist/` 下旧发布包也按同样原则处理。镜像归档、运行配置、数据库卷是不同内容；删除旧归档不影响已导入镜像。

## 文件与职责

| 文件 | 用途 |
| --- | --- |
| [根 compose.yaml](../compose.yaml) | Python、Go、Caddy 基础服务与启动依赖 |
| [compose.data.yaml](compose.data.yaml) | MySQL、Redis、数据库卷及账号 / 缓存配置 |
| [compose.existing-proxy.yaml](compose.existing-proxy.yaml) | Go 绑定本机 18080，停用独立 Caddy |
| [compose.dev-data.yaml](compose.dev-data.yaml) | 本地开发 MySQL 13306 / Redis 16379 |
| [compose.test.yaml](compose.test.yaml) | 独立测试 MySQL 23306 / Redis 26379；MySQL 使用临时文件系统 |
| [backend.Dockerfile](backend.Dockerfile) | Go 交叉编译、缓存依赖并打包网页，非 root 运行 |
| [agent-service.Dockerfile](agent-service.Dockerfile) | Python 3.12 与锁定依赖，非 root 运行 |
| [Caddyfile](Caddyfile) | 独立入口的 HTTPS 与研究请求超时 |
| [.env.example](.env.example) | Compose 的配置占位示例 |
| [Agent 配置示例](../agent-service/.env.example) | 模型与搜索配置；本地和源码部署共用 |
| [.dockerignore](../.dockerignore) | 排除真实凭据、依赖、测试、备份和镜像归档 |

## 常见问题

| 现象 | 检查位置 |
| --- | --- |
| 登录不可用、`mysql=disabled` | 是否启用 `compose.data.yaml` 或完整离线包；Go 是否收到 `MYSQL_DSN` / `JWT_SECRET` |
| 数据库密码错误 | 已有数据卷保留初次创建的账号密码；不要通过删卷重建解决 |
| `port is already allocated` | 检查端口所属服务；已有 Nginx / Caddy 时使用 existing-proxy 配置 |
| 网页能打开，但研究失败 | Python 日志、模型 / Tavily 凭据和服务器出网能力 |
| 返回 `429` | 等待并发名额或频率窗口释放，不重复刷新提交 |
| 研究长时间后超时 | Python / Go 预算、代理 320 秒读取超时，以及前置 CDN 限制 |
| 修改网页后仍是旧页面 | 是否重新构建并部署 Go 镜像，随后刷新浏览器 |
| `no matching manifest` 或架构错误 | 核对服务器架构，重新构建和导出对应平台 |
| `--pull never` 报缺少镜像 | 是否导入了所选 Compose 需要的全部镜像，名称与标签是否对应 |

更多接口与缓存机制见 [后端说明](../backend/README.md)，研究限制见 [Agent 说明](../agent-service/README.md)，网页操作见 [前端说明](../frontend/README.md)。
