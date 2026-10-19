# 部署到 Ubuntu / Debian 服务器

本方案运行三个容器：Caddy 接收浏览器请求并提供 HTTPS，Go 提供网页与 API，Python 执行 Agent 工作流。前端是静态文件，构建时随 Go 一起打包，不需要安装 Node.js。

```text
访客浏览器 → 域名 / HTTPS :443 → Caddy → Go :8080 → Python :8000
                                             ↓            ↓
                                          静态网页    模型服务 + Tavily
```

公开端口只有 80、443；8000、8080 只在容器网络内使用。服务器需要能访问模型服务、Tavily，以及构建使用的 Docker 镜像和依赖仓库。当前请求会等待报告完整生成，最长约 5 分钟；还没有后台任务队列。本文默认部署匿名入口；注册登录、数据库和缓存使用 [V3/V4 数据服务扩展](../docs/v3-v4.md#7-服务器部署备份与恢复)。

如果服务器只有 **2 GiB 内存且已经运行简历、Umami 或数据库**，优先使用后面的「在开发机构建，服务器只运行」方案。镜像构建会额外占用 CPU 和内存，不应与线上站点争抢有限资源。

## 1. 安装 Docker 和 Compose

在 Ubuntu / Debian 服务器上安装 Git、Docker Engine，以及 Docker Compose 插件。按照与你系统匹配的官方文档中「Install using the apt repository」步骤操作：

- [Ubuntu 安装步骤](https://docs.docker.com/engine/install/ubuntu/#install-using-the-apt-repository)
- [Debian 安装步骤](https://docs.docker.com/engine/install/debian/#install-using-the-apt-repository)

安装 Docker 时需要包含 `docker-compose-plugin` 和 `docker-buildx-plugin`。后续命令使用 `sudo docker compose`，不需要额外配置 Docker 用户组。

```bash
sudo apt update
sudo apt install -y git
sudo systemctl enable --now docker
sudo docker version
sudo docker compose version
```

最后两个命令应显示版本，且 Docker 服务端没有连接错误。Docker 会在镜像内安装 Go、Python 和依赖，服务器不需要再单独安装它们。

## 2. 上传或拉取项目

若使用 GitHub，在服务器执行：

```bash
mkdir -p ~/apps
cd ~/apps
git clone https://github.com/qiangwei0513/ai-research-platform.git
cd ai-research-platform
```

如果已经上传了整个项目，直接进入项目根目录即可；该目录中应有 `compose.yaml`、`frontend/`、`backend/` 和 `agent-service/`。后续命令都在这个目录执行。

## 3. 配置 DNS 和端口

假设你准备使用 `agent.luananwei.me`，在域名的 DNS 控制台添加：

| 类型 | 主机记录 | 值 |
|---|---|---|
| A | `agent` | 服务器的公网 IPv4 地址 |

这里新增 `agent` 子域名即可，**保留 `luananwei.me` 的根域名记录和现有个人简历站点**。只有服务器确实配置了可用的公网 IPv6 时才添加 AAAA；已有错误的 AAAA 记录需要删除或修正，否则部分访问和证书申请可能失败。

在云服务器安全组中放行 TCP **80、443**，同时保留你当前的 SSH 管理端口；无需放行 8000 或 8080。确认 80、443 没有被已有 Nginx、Apache 或其他服务占用。使用 Cloudflare 等 DNS 服务时，首次部署先使用「仅 DNS」模式，使域名直接指向服务器；额外代理可能有比本项目更短的请求等待上限。

## 4. 填写两个环境变量文件

这两个文件用途不同：

| 文件 | 谁读取 | 保存什么 |
|---|---|---|
| `deploy/.env` | Docker Compose | 域名、公开服务的请求限制 |
| `agent-service/.env` | Python 容器 | 模型与 Tavily 的凭据 |

首次部署、对应文件不存在时复制示例：

```bash
cp deploy/.env.example deploy/.env
cp deploy/agent.env.example agent-service/.env
chmod 600 deploy/.env agent-service/.env
```

已有配置时直接编辑，不要再次复制覆盖。

```bash
nano deploy/.env
```

将 `SITE_ADDRESS` 改为你的真实域名，**不要加路径**：

```dotenv
SITE_ADDRESS=agent.luananwei.me
MAX_CONCURRENT_RESEARCH=2
RATE_LIMIT_PER_MINUTE=20
```

然后编辑模型配置：

```bash
nano agent-service/.env
```

将你本地已跑通的 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`TAVILY_API_KEY` 填入，并保留 `OPENAI_AGENTS_DISABLE_TRACING=1`。API 地址必须能从服务器访问，不能是仅在你电脑上运行的 `localhost` 地址。目前各 Agent 使用的模型名称仍为 `deepseek-flash`，服务商需要支持该模型。

这些 `.env` 文件被 Git 忽略，Docker 构建也会排除它们。Compose 仅在启动时将模型凭据注入 Python 容器；浏览器、Go 和 Caddy 都不需要这些 Key。不要把含有真实配置的 `docker compose config` 完整输出分享给别人，校验时使用下面的 `--quiet`。

## 5. 构建并启动

下面的默认方案用于 **80、443 端口空闲**的服务器。如果个人简历已经在同一台服务器占用这两个端口，请使用后面的「与现有简历站点共用服务器」方案，不要停止现有站点。

```bash
sudo docker compose --env-file deploy/.env config --quiet
sudo docker compose --parallel 1 --env-file deploy/.env build
sudo docker compose --env-file deploy/.env up -d --no-build
sudo docker compose --env-file deploy/.env ps
```

首次构建需要下载镜像和依赖，耗时取决于服务器网络和配置。`--parallel 1` 让 Compose 依次构建服务，不能作为内存上限。Python 健康检查通过后 Go 才启动，Go 健康检查通过后 Caddy 才启动；健康检查只确认进程接口可响应，不检查模型 Key 和 Tavily Key 是否有效。

DNS 已指向服务器且端口可访问时，Caddy 会为 `SITE_ADDRESS` 中的域名申请 HTTPS 证书并自动续期，无需再安装 Certbot。证书存放在 `caddy_data` 卷中，普通容器重建不会丢失。[Caddy 自动 HTTPS 说明](https://caddyserver.com/docs/automatic-https)

在浏览器打开：

```text
https://agent.luananwei.me
```

用一个简单问题先试普通问答，再试完整研究。页面能打开只证明网页已部署，完整研究成功才说明服务器能访问模型和 Tavily。

命令行也可以检查：

```bash
curl https://agent.luananwei.me/ping
curl --max-time 330 https://agent.luananwei.me/api/research \
  -H 'Content-Type: application/json' \
  -d '{"query":"研究 Go context 如何实现超时和取消传播","mode":"research"}'
```

请把命令里的示例域名换成你的域名。第二条命令会真实调用模型和 Tavily，产生相应 API 用量。

如果只想在运行 Docker 的本机预览，可以保留 `SITE_ADDRESS=http://localhost` 并打开 `http://localhost`；这个配置明确使用 HTTP，不申请证书，也不使用自签名 HTTPS。公开域名上线时要替换此值并重建 Caddy 容器。

## 与现有简历站点共用服务器

如果 `luananwei.me` 已经由这台服务器上的 Nginx 或 Caddy 提供服务，保留它作为唯一的公网入口。本项目使用额外的 Compose 配置，只把 Go 暴露到服务器本机 `127.0.0.1:18080`，不会启动本项目的 Caddy，也不会抢占 80、443：

```bash
sudo docker compose -f compose.yaml -f deploy/compose.existing-proxy.yaml --env-file deploy/.env config --quiet
sudo docker compose --parallel 1 -f compose.yaml -f deploy/compose.existing-proxy.yaml --env-file deploy/.env build
sudo docker compose -f compose.yaml -f deploy/compose.existing-proxy.yaml --env-file deploy/.env up -d --no-build
curl http://127.0.0.1:18080/ping
```

使用这个方案后，后续的 `ps`、`logs`、`up`、`down` 都需要保留这两个 `-f` 参数。`SITE_ADDRESS` 此时不会配置现有代理，需要你在现有代理中新增 **agent 子域名站点**，不要替换简历站点的配置。

若现有代理是安装在宿主机上的 Caddy，在其配置中追加：

```caddyfile
agent.luananwei.me {
    reverse_proxy 127.0.0.1:18080 {
        transport http {
            response_header_timeout 320s
            read_timeout 320s
            write_timeout 320s
        }
    }
}
```

按现有 Caddy 的管理方式校验并重新加载配置，Caddy 会处理该子域名的证书。

若现有代理是安装在宿主机上的 Nginx，新增一个独立的子域名配置，HTTP 入口可以从下面开始：

```nginx
server {
    listen 80;
    server_name agent.luananwei.me;

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

配置放到现有 Nginx 加载的目录中，启用前先执行 `sudo nginx -t`，通过后再重新加载。DNS 生效且 HTTP 可访问后，按现有证书管理方式为 **agent.luananwei.me** 配置 HTTPS；使用 Certbot 时，可以参考 [Ubuntu 的 TLS 证书指南](https://ubuntu.com/server/docs/how-to/security/obtain-tls-certificates/) 对该域名单独签发。对外分享时使用 HTTPS 地址。[Nginx 代理超时说明](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_read_timeout)

如果简历站点由宝塔、1Panel 等面板管理，在面板中新增子域名站点，反向代理到 `http://127.0.0.1:18080`，为子域名启用 HTTPS，并将代理读取超时设置为至少 320 秒。若现有反向代理本身也在容器中，它的 `127.0.0.1` 指向代理容器自身，需要用该代理支持的容器网络连接方式访问后端，不能直接套用宿主机地址。

## 在开发机构建，服务器只运行

以下流程适合与简历站点共用的小规格服务器，沿用上面的现有代理方案。构建放在你的电脑上，服务器只导入和运行镜像。电脑需要安装并启动 Docker Desktop。

先在**服务器**确认 CPU 架构：

```bash
uname -m
```

`x86_64` 对应 `linux/amd64`，`aarch64` 对应 `linux/arm64`。Apple 芯片 Mac 默认是 ARM 架构，因此必须明确指定服务器的平台，不能直接上传默认构建的镜像。

在**开发机的项目根目录**执行。以下以服务器为 `x86_64` 为例；如果是 `aarch64`，把第一行改为 `linux/arm64`：

```bash
AI_RESEARCH_PLATFORM=linux/amd64
docker buildx build --platform "$AI_RESEARCH_PLATFORM" --load \
  -t ai-research-platform-backend:latest -f deploy/backend.Dockerfile .
docker buildx build --platform "$AI_RESEARCH_PLATFORM" --load \
  -t ai-research-platform-agent-service:latest -f deploy/agent-service.Dockerfile .
docker image save -o /tmp/ai-research-images.tar \
  ai-research-platform-backend:latest ai-research-platform-agent-service:latest
scp /tmp/ai-research-images.tar root@47.80.21.229:/tmp/
```

这两次构建依次执行。Go 使用构建机原生编译器生成目标平台程序；Python 在目标平台环境中安装依赖，在不同架构的 Mac 上可能用到 Docker Desktop 的模拟运行，耗时可能更长。[Docker 多平台构建说明](https://docs.docker.com/build/building/multi-platform/)

上面的镜像名对应 `compose.yaml` 中的项目名 `ai-research-platform`。此流程不要另设 `-p` 或 `COMPOSE_PROJECT_NAME`，否则 Compose 会寻找其他名称的镜像。镜像不包含 `.env` 文件，服务器仍需要第 4 步的配置。

先将同一版本的项目文件同步到服务器，保留服务器自己的 `.env`，再在**服务器**执行：

```bash
cd /opt/ai-research-platform
docker image load -i /tmp/ai-research-images.tar
docker compose -f compose.yaml -f deploy/compose.existing-proxy.yaml --env-file deploy/.env config --quiet
docker compose -f compose.yaml -f deploy/compose.existing-proxy.yaml --env-file deploy/.env \
  up -d --no-build --pull never backend agent-service
docker compose -f compose.yaml -f deploy/compose.existing-proxy.yaml --env-file deploy/.env ps
curl http://127.0.0.1:18080/ping
```

`--no-build --pull never` 只使用已经导入的镜像；缺少镜像时会报错，不会自动在服务器上开始构建。镜像导入仍需要可用内存和磁盘空间，但省去了 Go 编译和 Python 依赖安装。确认启动正常后，可删除两端的 `/tmp/ai-research-images.tar` 释放空间。

代码更新时，在开发机重新构建并上传镜像，服务器再次导入、执行上述启动命令。仅修改服务环境变量时无需重新构建或上传镜像。

## 6. 日常更新和日志

在服务器上构建的方案，从 GitHub 更新后依次构建并启动；如果采用开发机构建方案，按上一节更新镜像：

```bash
git pull --ff-only
sudo docker compose --parallel 1 --env-file deploy/.env build
sudo docker compose --env-file deploy/.env up -d --no-build
```

修改 `.env` 后执行 `up -d --no-build` 即可，使容器使用新配置；只运行 `restart` 不会重新读取容器的环境变量。与已有简历共用服务器时，继续保留两个 `-f` 参数。更新会中断正在执行的请求，当前版本没有任务恢复功能，尽量在无人使用时更新。

查看近期日志：

```bash
sudo docker compose --env-file deploy/.env logs --tail=100 backend agent-service
sudo docker compose --env-file deploy/.env logs --tail=100 caddy
```

实时跟踪可加 `-f`，按 Ctrl+C 只会退出日志查看。日志可能包含研究问题、搜索词和资料链接，分享前注意内容。

停止服务：

```bash
sudo docker compose --env-file deploy/.env down
```

不要给这条命令加 `-v`，否则会删除证书等持久卷。已有镜像时重新启动使用 `up -d --no-build`，无需再次构建。容器设置了 `restart: unless-stopped`，系统重启后 Docker 会恢复之前运行中的容器。

## 常见问题

Go 镜像使用 `-tags=nomsgpack` 排除项目没有使用的 MsgPack 功能，避免编译 `github.com/ugorji/go/codec`。现有接口使用 JSON，不受影响；将来若要新增 MsgPack 接口，需要调整这个构建标签。[Gin 官方说明](https://gin-gonic.com/zh-cn/docs/build-tags/nomsgpack/)

`GOMAXPROCS=1` 和 `go build -p 1 -v` 让 Go 串行编译依赖并输出包名；BuildKit 缓存挂载保留 Go 编译结果，后端源码修改后可复用未变化的依赖。`go mod download` 只下载模块源码，不能替代编译缓存。这些措施不改变应用运行时并发，也不是构建的 CPU 或内存硬上限；首次构建和缓存清理后仍要重新编译。[Docker 缓存说明](https://docs.docker.com/build/cache/optimize/)

如果构建期间 SSH 无响应，先尝试在原构建终端按 Ctrl+C 停止构建，再通过云控制台的命令助手或 VNC 检查 `free -h`、`uptime` 和内核日志。CPU 使用率高或控制台某一次内存读数，不能单独证明发生了内存耗尽；不要同时重复发起多个构建。

| 现象 | 检查方法 |
|---|---|
| 域名打不开或证书申请失败 | 确认 DNS 的 A/AAAA、云安全组、80/443 端口占用，再查看 Caddy 日志 |
| `port is already allocated` | 服务器已经有程序占用 80 或 443；先确认现有站点用途，再调整端口或反向代理方案 |
| Go 或 Python 显示 `unhealthy` | 查看对应容器日志；确认依赖下载完成，配置文件格式正确 |
| 网页正常但研究失败 | 查看 `agent-service` 日志，确认服务器的模型和 Tavily 凭据及网络；网页不会显示完整内部异常 |
| 返回 429 | 服务同时处理的请求过多或短时间请求过密，稍后重试 |
| 约 5 分钟后超时 | 请求没有在总预算内完成，缩小问题范围；若前面还有 CDN/代理，也检查它的等待上限 |
| 修改页面后仍是旧版本 | 按所选方案重新构建后端镜像并更新容器，然后强制刷新浏览器 |
| 构建时镜像或依赖下载失败 | 分别检查 Docker Hub、ghcr.io、PyPI、Go 模块下载地址的网络；这与模型 Key 无关 |

## 部署文件说明

| 文件 | 作用 |
|---|---|
| 根目录 `compose.yaml` | 三个服务的启动顺序、内部地址、端口、健康检查和证书卷 |
| `deploy/agent-service.Dockerfile` | 使用 Python 3.12 和锁定依赖构建 Python 服务；以非 root 用户运行 |
| `deploy/backend.Dockerfile` | 使用 Go 1.26.4 交叉编译，排除 MsgPack 并缓存编译结果，打包静态前端；以非 root 用户运行 |
| `deploy/compose.existing-proxy.yaml` | 共用已有反向代理时，仅将 Go 映射到宿主机回环地址 |
| `deploy/Caddyfile` | 反向代理到 Go；研究请求等待上限为 320 秒 |
| `deploy/.env.example` | 域名和服务限制的示例 |
| `deploy/agent.env.example` | 模型和 Tavily 凭据的占位示例 |
| 根目录 `.dockerignore` | 排除真实 `.env`、本地虚拟环境、Git 数据和测试文件 |

本文的默认配置未启用用户数据库，网页仍为匿名入口。启用 compose.data.yaml 后，可通过登录 API 保存和查询个人研究记录，具体配置、数据库备份和恢复见 [V3/V4 说明](../docs/v3-v4.md)。所有用户仍共享服务端 API 额度，进程级请求限制不是个人配额或分布式限流。匿名网页结果刷新后仍会消失。
