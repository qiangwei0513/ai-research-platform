# Go 后端

基于 Gin 提供研究 API、账号认证和个人研究历史。研究请求转发给 Python Agent 服务执行，MySQL 保存用户、任务和报告，Redis 缓存单个任务的状态。

## 本地运行

需要 Go 1.26.4 或更高版本（见 `go.mod`）。执行研究还需要可用的 [Agent 服务](../agent-service/README.md)，默认地址为 `http://127.0.0.1:8000/research`。

### 匿名模式

不设置 `MYSQL_DSN` 时，仅提供匿名研究接口，不保存历史，也不启用账号、任务查询和任务状态缓存。

在项目根目录执行：

```bash
cd backend
go run .
```

默认监听 `127.0.0.1:8080`。可用以下命令检查进程：

```bash
curl http://127.0.0.1:8080/ping
```

### 启用账号与历史

可以使用已有 MySQL（提前创建数据库并提供建表和读写权限），也可以从项目根目录启动项目自带的开发服务：

```bash
docker compose -f deploy/compose.dev-data.yaml up -d --wait
```

该配置提供本机 `13306` 端口的 MySQL 和 `16379` 端口的 Redis，与下面的 `.env.example` 一致。Redis 是可选缓存，MySQL 是账号和历史的必要依赖。

在 `backend` 目录执行（已有 `.env` 时直接编辑）：

```bash
test -f .env || cp .env.example .env
openssl rand -hex 32
```

编辑 `.env`：将 `MYSQL_DSN` 改为实际连接信息，将生成的随机字符串填入 `JWT_SECRET`；按需设置 `REDIS_URL`，不使用缓存则删除该项或设为空。示例中的端口也需按本机服务修改。

Go 不会自动加载 `.env`，启动前需要导出：

```bash
set -a
. ./.env
set +a
go run .
```

启动时检查 MySQL 连接并自动执行数据库迁移。数据库不可用、迁移失败或 JWT 密钥不足 32 字节时会启动失败。重启时保持 `JWT_SECRET` 不变，更换密钥会使已签发的 token 失效。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GO_ADDR` | `127.0.0.1:8080` | HTTP 监听地址 |
| `AGENT_SERVICE_URL` | `http://127.0.0.1:8000/research` | Agent 研究接口的完整 URL |
| `MYSQL_DSN` | 未设置 | MySQL 连接串；设置后启用账号与历史 |
| `JWT_SECRET` | 未设置 | 启用 MySQL 时必填，至少 32 字节 |
| `REDIS_URL` | 未设置 | 任务状态缓存连接地址；未设置则禁用缓存 |
| `CACHE_PREFIX` | `arp` | Redis 键前缀，不同环境应使用不同前缀 |
| `MAX_CONCURRENT_RESEARCH` | `2` | 整个进程共享的研究并发上限，至少为 1 |
| `RATE_LIMIT_PER_MINUTE` | `20` | 整个进程每分钟的研究请求上限，`0` 表示不限频率 |
| `FRONTEND_DIR` | `../frontend` | 静态文件目录；目录不可用时 API 仍可运行 |

MySQL 连接串格式示例：`research:your-password@tcp(127.0.0.1:3306)/research?charset=utf8mb4`。

## API

以下路径均以 Go 服务地址为基准。账号和任务查询接口仅在启用 MySQL 后注册。

| 方法与路径 | 作用 | 认证 |
| --- | --- | --- |
| `GET /ping` | 检查 Go 进程，返回 `{"message":"pong"}` | 无 |
| `GET /health` | 检查 MySQL 与任务状态缓存 | 无 |
| `POST /api/auth/register` | 注册账号 | 无 |
| `POST /api/auth/login` | 登录并获取 Bearer token | 无 |
| `POST /api/research` | 执行问答或研究 | 可选 |
| `GET /api/tasks?page=1&page_size=20` | 分页查询自己的任务，按开始时间倒序 | 必须 |
| `GET /api/tasks/:id` | 查询任务状态、时间与失败原因 | 必须 |
| `GET /api/tasks/:id/runs` | 查询已保存的阶段记录 | 必须 |
| `GET /api/tasks/:id/report` | 查询成功任务的报告、计划与来源 | 必须 |

### 注册与登录

两个接口使用相同的 JSON 请求格式：

```json
{"email":"reader@example.com","password":"replace-with-your-password"}
```

邮箱会去除首尾空白并转为小写，密码保持原样，要求 8～72 字节，使用 bcrypt 保存哈希。注册成功返回 `201` 和用户信息；重复邮箱返回 `409`。

登录成功返回 `access_token`、`token_type: "Bearer"`、`expires_in: 86400` 和用户信息。后续请求携带 `Authorization: Bearer <token>`。当前不提供刷新 token、服务端登出、邮箱验证和密码找回接口。

注册和登录共用一组独立于研究接口的进程级限制：并发 2、每分钟 20 次。

### 提交研究

```bash
curl -i --max-time 330 http://127.0.0.1:8080/api/research \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer 替换为登录返回的token' \
  -d '{"query":"解释 Go context 取消机制","mode":"research","fresh":false}'
```

匿名调用时去掉 `Authorization` 请求头。

| 字段 | 说明 |
| --- | --- |
| `query` | 必填；去除首尾空白后为 1～5000 个字符 |
| `mode` | `chat`（默认）或 `research` |
| `fresh` | 默认 `false`；设为 `true` 时要求 Agent 绕过搜索缓存 |

请求体最大 64 KiB。Go 等待 Agent 的上限为普通问答 130 秒、完整研究 310 秒。

接口同步等待结果，成功响应包含 `result`、`mode`，以及 Agent 返回的 `plan`、`search`、`runs` 等可选字段。登录请求先创建任务，响应头包含 `X-Task-ID`，成功 JSON 增加 `task_id`。任务 ID 不会作为异步收据立即返回。

匿名请求不保存历史。启用账号后，无效或过期 token 返回 `401`；未启用 MySQL 时，携带 `Authorization` 的研究请求返回 `503`。登录用户的任务创建失败时不会调用 Agent；执行失败会尝试保存失败状态和原因。

### 查询历史

所有任务接口都按当前用户过滤；访问不存在或其他用户的记录统一返回 `404`。报告尚未生成时，报告接口也返回 `404`。

任务列表返回 `items`、`total`、`page`、`page_size`。分页范围为 `page=1～1000000`、`page_size=1～100`。阶段记录接口返回 `{"items":[...]}`，执行中的阶段不会实时写入或推送。

### 健康检查与错误

`/health` 中 MySQL 故障返回 `503`；Redis 故障仍返回 `200`，并将 `redis` 标记为 `unavailable`。未启用的数据服务标记为 `disabled`。该接口不检查 Agent 服务或模型的可用性。

| 状态码 | 常见原因 |
| --- | --- |
| `400` | 参数、请求格式或分页范围错误 |
| `401` | 缺少必要的登录凭据，或凭据无效、过期 |
| `404` | 任务或报告不存在，或不属于当前用户 |
| `409` | 邮箱已注册 |
| `413` | 研究请求体超过大小限制 |
| `429` | 超过并发或频率限制，响应带 `Retry-After` |
| `502` | Agent 连接失败、执行失败或返回无效结果 |
| `503` | 数据服务不可用，或任务、报告保存失败 |
| `504` | Agent 执行或通信超时 |

## 数据存储与任务状态

MySQL 保存 `users`（用户）、`tasks`（任务）、`agent_runs`（阶段记录）、`reports`（报告及来源）。`schema_migrations` 记录迁移版本和校验和。

任务状态为 `running → succeeded / failed`。报告、阶段记录和最终状态在同一事务中提交，已结束的任务不能再次覆盖。服务每 30 秒检查过期任务：普通问答的任务期限为 150 秒，完整研究为 330 秒；过期仍为 `running` 的任务会标记为 `execution_interrupted`。重启后继续检查，但不会自动重新执行研究。

Redis 仅缓存单个任务状态，键格式为 `{CACHE_PREFIX}:task:v1:{user_id}:{task_id}`。运行中状态缓存 5 秒，结束状态缓存 60 秒。更新先提交 MySQL，再按版本更新缓存，防止旧状态覆盖新状态。Redis 不可用时回退到 MySQL；任务列表、阶段记录和报告直接读取 MySQL。

迁移文件位于 [`internal/repository/migrations`](internal/repository/migrations)。新增结构变更应添加编号递增的 SQL 文件，不要修改已应用的迁移，否则校验和检查会拒绝启动。

## 目录结构

```text
backend/
├── main.go              # 服务生命周期、依赖初始化与路由注册
├── config.go            # 字符串和整数环境变量解析
├── research.go          # 问答/研究请求、Python 调用、结果与失败落库
├── limits.go            # 请求大小、并发与频率限制
├── web.go               # 安全响应头与静态文件服务
├── internal/
│   ├── handler/         # 账号、认证与任务查询接口
│   ├── service/         # 任务缓存、状态更新与过期处理
│   ├── repository/      # MySQL 读写和数据库迁移
│   └── model/           # 用户、任务、报告与阶段记录模型
└── integration_test.go  # 接口与持久化集成测试
```

## 测试

在 `backend` 目录执行：

```bash
go test -race ./...
go vet ./...
```

依赖真实 MySQL 或 Redis 的测试在缺少相应环境变量时会跳过。完整集成测试使用独立的测试服务；从项目根目录执行：

```bash
docker compose -f deploy/compose.test.yaml up -d --wait
cd backend
TEST_MYSQL_DSN='research:test-password@tcp(127.0.0.1:23306)/research_test?charset=utf8mb4' \
TEST_REDIS_URL='redis://127.0.0.1:26379/0' \
go test -count=1 -race ./...
go vet ./...
```

测试会写入数据，使用测试专用实例或数据库。研究接口测试使用 Agent 替身，无需调用真实模型。该 Compose 将 MySQL 数据放在临时文件系统中，端口与开发服务不同；完成各模块测试后，从项目根目录执行 `docker compose -f deploy/compose.test.yaml down`。不要替换为开发或生产连接。
