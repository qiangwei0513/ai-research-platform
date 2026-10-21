# 部署与运维

Go 镜像内含 API、Worker 和静态网页，Python 执行研究；V5 还需要 MySQL、缓存 Redis 和独立队列 Redis。前端不单独构建，也不需要在服务器安装 Node.js、Go 或 Python。

V7 已补齐本地完整容器部署、应用启动重试与可重复验收。镜像在开发机构建，服务器只导入并启动。以下默认目标为 **Linux x86_64 / amd64**，服务器部署复用已有的 Nginx / Caddy。

```text
浏览器 → 已有反向代理 / HTTPS → 127.0.0.1:18080 → Go API → MySQL
                                                        ↕         ↕
                                                   缓存 Redis   Worker → 队列 Redis
                                                                  ↓
                                                               Python → 模型与 Tavily
```

Go 也使用缓存 Redis 读取任务状态。V5 新增独立 Worker 和队列 Redis：API 将任务与 outbox 写入 MySQL，Worker 投递/消费 Redis Streams 后调用 Python。MySQL、两套 Redis、Worker 和 Python 均不公开宿主机端口。登录后台任务不需要长 HTTP 等待；保留的匿名同步接口仍要求代理读取超时覆盖 310 秒预算。V6 SSE 返回阶段进度，使用 15 秒心跳；代理应关闭该接口的响应缓冲。

## 本地完整启动 V7

在 **Mac / 开发机项目根目录** 执行。Docker Desktop 需要处于运行状态；Linux 使用 Docker Engine 与 Compose 插件。运行应用不要求宿主机装 Go、Python 或 Node.js，只有下文的自动验收脚本需要宿主机 Python 3.10+。

1. 创建配置（保留已有文件），填写真实服务配置：

   ```bash
   test -f deploy/.env || cp deploy/.env.example deploy/.env
   test -f agent-service/.env || cp agent-service/.env.example agent-service/.env
   chmod 600 deploy/.env agent-service/.env
   ```

   `deploy/.env` 填写 MySQL / Redis 密码及至少 32 字节的 `JWT_SECRET`；`agent-service/.env` 填写模型 Key、Base URL、Tavily Key。不要保留 `replace-with-*` 占位值。配置不会放入镜像。密码建议使用十六进制字符串，避免 URL / DSN 特殊字符。

2. 在本机构建应用、准备数据镜像，再启动：

   ```bash
   bash deploy/compose.sh config --quiet
   bash deploy/compose.sh build backend agent-service
   bash deploy/compose.sh pull mysql redis redis-queue
   bash deploy/compose.sh up -d --no-build --pull never --wait --wait-timeout 240
   bash deploy/compose.sh ps
   curl http://127.0.0.1:18080/health
   ```

   六个服务应为 `healthy`。打开 **http://127.0.0.1:18080** 注册网站账号。`/health` 应包含 `mysql=ok`、`redis=ok`、`queue=ok`、`async_tasks=true`、`task_events=true`。真实研究需要模型与搜索服务可用，并会产生用量。

3. 日志、停止与再次启动：

   ```bash
   bash deploy/compose.sh logs --tail=100 backend worker agent-service
   bash deploy/compose.sh down
   bash deploy/compose.sh up -d --no-build --pull never --wait --wait-timeout 240
   ```

`compose.sh` 固定合并根 Compose、数据服务和已有代理配置，从任何目录调用都能找到项目；`DEPLOY_ENV_FILE=/绝对路径/配置.env bash deploy/compose.sh ps` 可选择另一份配置。它不会自动创建密码或改变数据卷。默认项目名保持 `ai-research-platform`，已有数据升级时不要改变它。

容器内部由 Compose 默认网络提供服务名解析：API / Worker 使用 `mysql:3306`、`redis:6379`、`redis-queue:6379` 和 `agent-service:8000`。仅 API 的 `127.0.0.1:${APP_PORT:-18080}` 映射到宿主机。该网络允许 Python 出站访问模型和搜索，不是禁止所有出站流量的 `internal: true` 网络。

| 配置 | 默认值 | 改动效果 |
| --- | --- | --- |
| `APP_PLATFORM` | `linux/amd64` | 所有服务架构；改为 ARM 后重新构建应用与拉取基础服务镜像 |
| `APP_PORT` | `18080` | 宿主机回环地址的网页/API 端口；容器内仍用 8080 |
| `BACKEND_IMAGE` | `ai-research-platform-backend:latest` | API 与 Worker 共用标签 |
| `AGENT_IMAGE` | `ai-research-platform-agent-service:latest` | Python 镜像标签，不随 Compose 项目名变化 |
| `AGENT_ENV_FILE` | `./agent-service/.env` | Python 运行配置，路径相对于项目根目录；测试可指定绝对路径 |

### 健康检查与启动重试

- MySQL 使用应用账号执行 `SELECT 1`，Redis 使用带认证的 `PING`；Python 检查 `/health`。
- API 容器检查 `/health`，数据库不可用时返回 503；`/ping` 仍只是进程存活检查。缓存故障时 `/health` 仍返回 200，同时显示 `redis=unavailable`，查询回源 MySQL。
- API / Worker 等待 MySQL 健康；Worker 还等待队列和 Python 健康。缓存 Redis 只要求容器已启动，因为它是可降级依赖。
- 两个 Go 进程即使绕过 Compose 的依赖顺序，也会在启动的 30 秒预算内重试数据库连接：250ms 起指数退避，上限 2 秒；收到退出信号或到期即停止，权限/密码/数据库名错误立即退出。连接与迁移共用该预算。
- Worker `--healthcheck` 的总预算为 3 秒，直接检查 MySQL / 队列，不进入启动重试；普通 Worker 运行时会重连 Redis 并重建消费组。健康探针不证明消费循环有进展，也不验证模型 Key，仍需观察任务。
- `restart: unless-stopped` 在进程退出时生效；容器仅变为 `unhealthy` 并不会自动重启。数据库短暂中断后的连接由应用连接池恢复。API / Worker 设置 20 秒停止宽限，给 HTTP 排空及任务释放留出时间。

`depends_on: service_healthy` 控制启动顺序，不能代替运行时恢复；详细语义见 [Docker Compose 启动顺序](https://docs.docker.com/compose/how-tos/startup-order/)。

### 自动验收与数据保留

先按上面准备四个镜像，然后从项目根目录执行：

```bash
python3 deploy/smoke.py
```

脚本使用随机的 `arp-v7-smoke-*` 项目、空数据库、随机密码和可用端口；不读取现有 `.env`。测试专用替身只通过只读挂载接入 Python，真实 HTTP、工作流编排、Worker、MySQL、Redis 和 SSE 代码仍运行。不会请求付费模型或 Tavily，也不会修改开发数据库。可用 `--platform linux/arm64` 验证已构建的 ARM 镜像。

检查内容：

- 六容器健康、网页可访问、API / Worker / Python 为 UID 10001，仅公开 API 回环端口，镜像没有配置或测试替身。
- 空数据库自动应用三次迁移，注册登录、普通问答、后台研究、阶段与报告存储、SSE 补发。
- 将 `mysqldump` 的结果真实恢复到测试库内另一个数据库，并读回用户和报告。
- 停止缓存后仍可读报告；停止 MySQL 时健康检查返回 503；数据库延迟启动时 API / Worker 自行重试并恢复，进程没有靠重启完成恢复。
- 重建全部六个容器后，账号、任务、报告、事件和队列 AOF 仍在，缓存数据可以丢弃。

成功输出 `PASS all checks`，记录写入 `test-results/v7-compose.json`（已被 Git 忽略）；无论成功失败，最终只删除本次随机测试项目及其卷。普通部署不要照抄脚本清理测试卷的 `down --volumes` 命令。

`mysql_data` 保存用户、任务、报告、事件、迁移记录；`queue_data` 保存队列 AOF。容器重建不会删除这些命名卷，但卷不是异地备份。[Docker 数据卷说明](https://docs.docker.com/engine/storage/volumes/)

## 服务器准备

服务器需要 Docker Engine 和 Compose 插件；安装步骤见 [Ubuntu](https://docs.docker.com/engine/install/ubuntu/) 或 [Debian](https://docs.docker.com/engine/install/debian/) 官方文档。确认服务可用和 CPU 架构：

```bash
docker version
docker compose version
uname -m
```

`x86_64` 对应 `linux/amd64`，`aarch64` 对应 `linux/arm64`，镜像必须匹配服务器。Docker 命令以下假设当前用户有访问权限。运行研究时，服务器仍需访问模型服务和 Tavily；离线包只免除构建和下载镜像。

## 历史离线包

如果你另外保存了 `ai-research-platform-v4-ui-amd64-20261019.tar.gz`，它是 V4 历史版本，不包含 Worker、后台队列和 SSE。当前源码不会自动更新旧包，本次 V7 构建的镜像保存在本机 Docker 中；需要上传时按下文导出，不以旧包是否存在作为启动前提。

历史包的外层 `.tar.gz` 含运行配置和安装脚本，应先解压，再把内部 `images.tar` 交给 `docker image load`。其中可能含真实凭据，只能私下保管；镜像导出文件和配置都不上传 GitHub。保留旧版本用于回退时，应同时保留与其匹配的数据库备份和配置。

## 从源码构建新版镜像

以下适合本机验证后上传服务器，使用根目录 Compose 和 `deploy/` 的扩展文件。它导出四个镜像，不自动生成含真实配置的安装包。

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

### 本机构建并导出四个镜像（V7）

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

Go 镜像同时包含 `backend`、`worker` 两个程序和前端文件；API 与 Worker 运行在不同容器中。缓存与队列共用 Redis 镜像，但运行在不同容器中，因此仍只需四个镜像。需要支持 `docker image save --platform` 的 Docker 版本。构建时遇到 builder 连接错误，可用 `docker buildx ls` 查看；本机 Docker Desktop 可在构建命令中明确加 `--builder desktop-linux`。构建在本机依次进行，镜像源和依赖下载仍需要网络。

将同一版本的部署文件同步到服务器项目目录；若用 GitHub 更新，配置文件需要单独保留或安全传输，因为 Git 不包含真实 `.env`。V7 显式指定了应用镜像名称，测试项目改名不会改变镜像名；生产升级仍须保留 `ai-research-platform` 项目名，才能继续使用已有命名卷。镜像架构与交叉编译说明见 [Docker 多平台构建](https://docs.docker.com/build/building/multi-platform/)。

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

这是完整 V3～V7 配置：六个服务（API、Worker、Python、MySQL、缓存 Redis、队列 Redis），账号、个人历史、两种缓存和异步任务均启用。Go 启动时自动应用 `002_async_jobs.sql` 和 `003_task_events.sql`；保留原有表及数据，V7 没有新数据库迁移。`config --quiet` 只校验配置；不要把包含凭据的完整 `config` 输出公开。更新已有数据库前按下文备份，再执行 `up`。普通 `restart` 不会加载新环境变量，应使用 `up -d --no-build --pull never`。

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
        proxy_http_version 1.1;
        proxy_buffering off;
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
dc logs --tail=100 backend worker agent-service mysql redis redis-queue
curl http://127.0.0.1:18080/health
dc exec -T agent-service python -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8000/health").read().decode())'
```

Go 健康响应应为 `status=ok`、`mysql=ok`、`redis=ok`、`queue=ok`、`async_tasks=true`，Python 应为 `status=ok`、`redis=ok`。缓存 Redis 故障时回源，队列 Redis 故障时先保存 outbox 等待恢复；MySQL 不可用则无法接收新后台任务或保存历史。还应检查 Worker 日志和任务是否从 queued 转为 running；Worker 的容器健康检查仅验证 MySQL / 队列连接，不代表模型可用。`/ping` 只检查 Go 进程，健康接口也不会验证模型 Key。

打开网站后注册登录，提交一次快速问答和深入研究，查看「我的历史」，刷新后重新打开报告。真实研究会产生模型与搜索用量；页面显示「服务已连接」只说明 Go 可访问。

更新前先备份；代码或网页改动需要重建对应镜像，服务器重新 `load` 后执行 `dc up -d --no-build --pull never --wait --wait-timeout 240`。仅调整环境变量无需重建镜像。更新会中断正在执行的同步请求。V5 Worker 正常停止时将执行中任务重新排队；被强制终止时，租约过期后由运行中的 Worker 恢复。恢复从整项任务重新执行，不从中间阶段续跑；最多 3 次、总期限 30 分钟。

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

保留命令成功生成的 `.sql` 文件，并定期复制到服务器以外的位置。缓存 Redis 可以重建；V5 队列 Redis 使用独立 `queue_data` 卷、AOF everysec 与 noeviction。MySQL 的任务/outbox/租约仍是恢复依据，Redis 消息丢失可补投；AOF everysec 不等于零丢失，仍应保留数据库备份并监控磁盘和队列堆积。

恢复会覆盖备份中涉及的表，应先保留当前备份、确认目标数据库和版本，并停止应用写入。在维护窗口执行，替换为已核验的备份文件：

```bash
dc stop backend worker agent-service
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
| [compose.data.yaml](compose.data.yaml) | MySQL、两套 Redis、Worker、数据卷及账号 / 缓存 / 队列配置 |
| [compose.existing-proxy.yaml](compose.existing-proxy.yaml) | Go 绑定本机 18080，停用独立 Caddy |
| [compose.sh](compose.sh) | 从任意工作目录运行完整六服务 Compose，透传参数 |
| [smoke.py](smoke.py) | 隔离容器验收、故障演练、备份恢复与数据保留检查 |
| [tests/](tests/) | 仅测试挂载的上游替身与 Compose 覆盖，不进入镜像 |
| [compose.dev-data.yaml](compose.dev-data.yaml) | 本地开发 MySQL 13306 / 缓存 16379 / 队列 16380 |
| [compose.test.yaml](compose.test.yaml) | 独立测试 MySQL 23306 / 缓存 26379 / 队列 26380；MySQL 与队列使用临时文件系统 |
| [backend.Dockerfile](backend.Dockerfile) | API 与 Worker 交叉编译、打包网页，非 root 运行 |
| [agent-service.Dockerfile](agent-service.Dockerfile) | Python 3.12 与锁定依赖，非 root 运行 |
| [Caddyfile](Caddyfile) | 独立入口的 HTTPS 与研究请求超时 |
| [.env.example](.env.example) | Compose 的配置占位示例 |
| [Agent 配置示例](../agent-service/.env.example) | 模型与搜索配置；本地和源码部署共用 |
| [.dockerignore](../.dockerignore) | 排除真实凭据、依赖、测试、备份和镜像归档 |

## 常见问题

| 现象 | 检查位置 |
| --- | --- |
| 登录不可用、`mysql=disabled` | 是否启用 `compose.data.yaml` 或完整离线包；Go 是否收到 `MYSQL_DSN` / `JWT_SECRET` |
| 一直 queued | Worker 是否运行、两端数据库/队列前缀是否一致、队列可用性与 outbox 日志；检查是否在等待重试 |
| 数据库密码错误 | 已有数据卷保留初次创建的账号密码；不要通过删卷重建解决 |
| `port is already allocated` | 检查端口所属服务；已有 Nginx / Caddy 时使用 existing-proxy 配置 |
| 网页能打开，但研究失败 | Python 日志、模型 / Tavily 凭据和服务器出网能力 |
| 返回 `429` | 等待并发名额或频率窗口释放，不重复刷新提交 |
| 研究长时间后超时 | Python / Go 预算、代理 320 秒读取超时，以及前置 CDN 限制 |
| 修改网页后仍是旧页面 | 是否重新构建并部署 Go 镜像，随后刷新浏览器 |
| `no matching manifest` 或架构错误 | 核对服务器架构，重新构建和导出对应平台 |
| `--pull never` 报缺少镜像 | 是否导入了所选 Compose 需要的全部镜像，名称与标签是否对应 |

更多接口与缓存机制见 [后端说明](../backend/README.md)，研究限制见 [Agent 说明](../agent-service/README.md)，网页操作见 [前端说明](../frontend/README.md)。

## V6 SSE 代理与升级检查

升级同时重建 Go（含 Worker 与前端）和 Python 镜像；本次源代码更新不会自动更新 `dist/` 的旧镜像归档。先备份 MySQL，再替换镜像并用 `up -d --no-build --pull never` 重建服务；旧表不改写，迁移增加事件表与任务内序号表。V6 没有新增环境变量。

上面的 Nginx 简化示例关闭该站点响应缓冲；也可只在 `/api/tasks/*/events` 对应 location 关闭。后端附带 `X-Accel-Buffering: no`，不要配置忽略该头。代理读取超时应大于 15 秒心跳间隔，示例 320 秒兼顾旧同步请求。[Nginx 缓冲说明](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering)

Caddy 对 `text/event-stream` 响应自动及时刷新；现有 Caddyfile 可继续使用，不需全局添加负数 `flush_interval`。仍需检查外层 CDN、压缩或其他代理是否缓冲。[Caddy streaming 说明](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming)

上线后的验收：使用登录 token 和一个正在执行的任务 ID，分别对 Go 内网地址、站点 HTTPS 地址运行 `curl -N .../api/tasks/任务ID/events -H 'Authorization: Bearer token'`。应立即看到 snapshot，随后收到阶段事件和心跳，而非结束时才一次输出；用同一游标重连应补发。网页中验证刷新恢复、取消和来源。Go 健康检查需有 `task_events=true`，它只证明能力已启用，不证明 Worker/模型或公网代理已验收。

本次 V6 已验证本地链路；未在远端服务器安装新镜像，也未验证公网 TLS/CDN/SSE。不要据此勾选后续版本的线上部署验收。
