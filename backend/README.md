# Go 后端

基于 Gin 提供研究 API、账号认证和个人研究历史，独立 Go Worker 通过 Redis Streams 执行后台任务。研究请求转发给 Python Agent 服务执行，MySQL 保存用户、任务和报告，Redis 缓存单个任务的状态。

## 本地运行

统一容器运行见[部署说明](../deploy/README.md)。V7 的 Go 镜像同时包含 API、Worker 和静态前端，运行时拆为两个独立进程容器。

API / Worker 启动使用 `repository.OpenWithRetry`：在 30 秒启动预算内重试临时连接故障，250ms 指数退避至最多 2 秒；错误账号、权限和数据库名立即失败。探针与普通调用仍用不重试的 `Open`，Worker 健康检查总预算 3 秒。实现见 `internal/repository/startup.go`，测试覆盖恢复、截止、取消和配置错误。容器健康检查使用 `/health`，MySQL 不可用返回 503；`/ping` 保留存活含义。

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

该配置提供本机 `13306` 端口的 MySQL 、`16379` 端口的缓存 Redis 和 `16380` 端口的队列 Redis，与下面的 `.env.example` 一致。Redis 是可选缓存，MySQL 是账号和历史的必要依赖。

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

### 启用 V5 后台任务

在 API 与 Worker 的环境中设置相同的 `QUEUE_REDIS_URL=redis://127.0.0.1:16380/0` 和 `QUEUE_PREFIX=arp`。已有 `.env` 不会自动合并新示例，需要手动补入。API 仍用 `go run .` 启动；另开终端，进入 `backend` 并加载同一个 `.env` 后执行：

```bash
go run ./cmd/worker
```

`WORKER_CONCURRENCY=2` 控制每个 Worker 进程的执行槽位，`MAX_ACTIVE_TASKS_PER_USER=3` 控制异步提交时每个用户的活跃任务数量。API 与 Worker 是两个独立进程，不能只启动其中一个。未设置队列时保留 V3/V4 的同步能力。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GO_ADDR` | `127.0.0.1:8080` | HTTP 监听地址 |
| `AGENT_SERVICE_URL` | `http://127.0.0.1:8000/research` | Agent 研究接口的完整 URL |
| `MYSQL_DSN` | 未设置 | MySQL 连接串；设置后启用账号与历史 |
| `JWT_SECRET` | 未设置 | 启用 MySQL 时必填，至少 32 字节 |
| `REDIS_URL` | 未设置 | 任务状态缓存连接地址；未设置则禁用缓存 |
| `CACHE_PREFIX` | `arp` | Redis 键前缀，不同环境应使用不同前缀 |
| `QUEUE_REDIS_URL` | 未设置 | 独立队列 Redis 地址；API 配置后启用异步提交，Worker 必填 |
| `QUEUE_PREFIX` | `arp` | 队列前缀；API 与 Worker 保持一致，各环境分开 |
| `WORKER_CONCURRENCY` | `2` | 单个 Worker 并行执行数，1～16；多副本会累加 |
| `MAX_ACTIVE_TASKS_PER_USER` | `3` | 异步提交时每个用户 queued/running 数量上限 |
| `MAX_CONCURRENT_RESEARCH` | `2` | API 进程同步研究与异步提交共用的请求槽位，不是 Worker 并发数 |
| `RATE_LIMIT_PER_MINUTE` | `20` | 整个进程每分钟的研究请求上限，`0` 表示不限频率 |
| `FRONTEND_DIR` | `../frontend` | 静态文件目录；目录不可用时 API 仍可运行 |

MySQL 连接串格式示例：`research:your-password@tcp(127.0.0.1:3306)/research?charset=utf8mb4`。

## API

以下路径均以 Go 服务地址为基准。账号和任务查询接口仅在启用 MySQL 后注册。

| 方法与路径 | 作用 | 认证 |
| --- | --- | --- |
| `GET /ping` | 检查 Go 进程，返回 `{"message":"pong"}` | 无 |
| `GET /health` | 检查 MySQL、缓存、队列与异步功能开关 | 无 |
| `POST /api/auth/register` | 注册账号 | 无 |
| `POST /api/auth/login` | 登录并获取 Bearer token | 无 |
| `POST /api/research` | 执行问答或研究 | 可选 |
| `POST /api/tasks` | 创建后台任务，立即返回 202 和任务编号 | 必须 |
| `POST /api/tasks/:id/cancel` | 取消自己的异步任务 | 必须 |
| `GET /api/tasks/:id/attempts` | 查看各次执行及阶段记录 | 必须 |
| `GET /api/tasks/:id/events` | SSE 快照、阶段进度、重连补发和终态 | 必须且为任务所有者 |
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

### 同步提交研究（兼容接口）

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

### 异步提交、重试与取消

请求体与同步接口相同，路径改为 `POST /api/tasks`，必须登录。可以提供 `Idempotency-Key`（16～128 位字母、数字、下划线或连字符）；未提供时服务端生成。浏览器自动生成并在同一页面重试相同问题时复用。

```json
{"task_id":"32位任务编号","status":"queued","created":true,"task":{"id":"32位任务编号","status":"queued","async":true,"max_attempts":3}}
```

上面仅展示响应中的主要字段。正常返回 `202 Accepted`，响应头有 `Location` 和 `Idempotency-Key`。`202` 表示 MySQL 已保存任务，不表示 Worker 或模型已完成。同一用户同一键、相同请求复用任务（`created=false`），即使已经结束也不会重跑；同一键不同内容返回 `409`。未配置队列返回 `503 async_disabled`；配置过但 Redis 暂时不可达仍可接收并等待补投。

`GET /api/tasks/:id` 增加 `async`、`attempt`、`max_attempts`、`next_attempt_at`、`execution_started_at`。`started_at` 保留为提交时间；`execution_started_at` 是最近一次开始执行的时间。默认最多执行 3 次，瞬时连接失败、429、通用 5xx 与超时允许重试，退避 5 秒、10 秒。明确的搜索/工作流业务失败和无效输出不自动重试。每次 Go 调用预算为 chat 130 秒 / research 310 秒；整个任务含排队最长 30 分钟。

取消排队任务会清除 outbox，之后不能领取；取消执行中任务会撤销租约，Worker 通常在下一次 2 秒心跳时取消 Python HTTP 请求。Python 监听断连并取消本地协程，已经发给远端模型的请求仍可能计费。取消后迟到结果不能写成成功。重复取消返回原状态；成功/失败任务再取消返回 `409`。旧同步任务不能通过此接口取消。

`/attempts` 返回每次执行的成功/失败/取消、起止时间、错误和 `runs`。Worker 中断时可能来不及保存阶段数据。`/runs` 仍表示最终一次保存的阶段；两者都不是实时进度事件。

### V6 实时进度 SSE

启用 MySQL 后 `/health` 返回 `task_events=true`。API 与 Worker 启动均会执行新迁移 `003_task_events.sql`；升级时重启两个 Go 进程及 Python。没有新增环境变量。

```bash
curl -N http://127.0.0.1:8080/api/tasks/替换为任务编号/events \
  -H 'Authorization: Bearer 替换为token' \
  -H 'Last-Event-ID: 0'
```

省略 `Last-Event-ID` 时返回最新快照，然后跟随新事件；携带游标时先补发比该编号大的已保存事件，再发送一致性快照。每个任务独立从 1 递增，快照 ID 等于读取时的最后事件编号（没有旧事件时为 0）。

```text
id: 6
event: search_progress
data: {"status":"running","attempt":1,"max_attempts":3,"version":2,"progress":{"stage":"search","completed":1,"total":3}}

```

| 事件 | 含义 |
| --- | --- |
| `snapshot` | `{task, last_event_id, progress?}`，恢复完整任务视图 |
| `queued` / `running` | 入队或重试等待 / Worker 领取 |
| `planning` / `planned` | Planner 开始 / 计划已返回 |
| `search_progress` | 已结束搜索数量，包含成功、空结果与失败搜索 |
| `writing` / `answering` | 写作 / 快速问答，`completed` 为 0 或 1 |
| `succeeded` / `failed` / `cancelled` | 已提交到数据库的任务终态 |
| `stream_error` | 本次订阅读取失败，客户端应重连；不表示任务失败 |

业务事件携带 `status, attempt, max_attempts, version` 与可选 `progress/error_code/error_message`。`version` 是任务状态版本，进度事件不改变它；SSE 的 `id` 才是事件游标。快照与事件不传报告正文，成功后仍调用 `/report`。

生命周期事件与任务、报告在同一事务提交。Python 的 NDJSON 阶段帧由 Worker 校验、检查租约 token 后落库。重复进度不重复写入；同一次尝试不允许阶段倒退或数量减少。进度落库失败会终止该次上游调用并按 V5 规则有限重试，不能把未保存的事件当作成功。

SSE 每秒短查询一次 MySQL，不读取 V4 状态缓存，不在等待期间占用数据库连接。每 15 秒发送 `: heartbeat`；单连接最长 2 分钟或 JWT 到期。单进程最多 64 连接、单用户 4 连接，超限返回 429 / Retry-After。慢客户端单次写入预算 10 秒。断开订阅仅释放连接，不取消后台任务。

非法游标返回 400；大于现有最大编号返回 409，客户端清除游标重取快照；越权或任务不存在返回 404；无效凭据返回 401。重连带 JWT 请求头和 `Last-Event-ID`，不能把 token 放进查询参数。原生 EventSource 不用于这个 Bearer 方案。

当前按每连接轮询数据库，适用于此项目的小规模部署；尚无跨实例连接配额、事件归档/自动清理、Redis Pub/Sub 广播或模型逐 token 输出。升级前的任务只有已有状态，不补造历史进度。

### 查询历史

所有任务接口都按当前用户过滤；访问不存在或其他用户的记录统一返回 `404`。报告尚未生成时，报告接口也返回 `404`。

任务列表返回 `items`、`total`、`page`、`page_size`。分页范围为 `page=1～1000000`、`page_size=1～100`。阶段记录接口返回 `{"items":[...]}`，执行中的阶段不会实时写入或推送。

### 健康检查与错误

`/health` 中 MySQL 故障返回 `503`；Redis 故障仍返回 `200`，并将 `redis` 标记为 `unavailable`。未启用的数据服务标记为 `disabled`。增加的 `queue` 检查队列连接，`async_tasks` 只表示配置启用；二者均不证明 Worker 正在运行。该接口不检查 Agent 服务或模型的可用性。

| 状态码 | 常见原因 |
| --- | --- |
| `400` | 参数、请求格式或分页范围错误 |
| `401` | 缺少必要的登录凭据，或凭据无效、过期 |
| `404` | 任务或报告不存在，或不属于当前用户 |
| `409` | 邮箱已注册、幂等键与内容冲突，或尝试取消已完成任务 |
| `413` | 研究请求体超过大小限制 |
| `429` | 超过并发或频率限制，响应带 `Retry-After` |
| `502` | Agent 连接失败、执行失败或返回无效结果 |
| `503` | 数据服务不可用，或任务、报告保存失败 |
| `504` | Agent 执行或通信超时 |

## 数据存储与任务状态

MySQL 保存 `users`（用户）、`tasks`（任务）、`agent_runs`（阶段记录）、`reports`（报告及来源）。`schema_migrations` 记录迁移版本和校验和。

兼容的同步任务状态为 `running → succeeded / failed`。报告、阶段记录和最终状态在同一事务中提交，已结束的任务不能再次覆盖。服务每 30 秒检查过期任务：普通问答的任务期限为 150 秒，完整研究为 330 秒；过期仍为 `running` 的任务会标记为 `execution_interrupted`。重启后继续检查，但不会自动重新执行研究。

V5 另加 `task_jobs`、`task_outbox` 和 `task_attempts`，任务状态为 `queued → running → succeeded / failed / cancelled`，可重试失败回到 queued。创建任务、幂等记录、outbox 在同一个 MySQL 事务提交；独立 Worker 的维护循环负责发布。outbox 在任务被领取前保留并每 30 秒补投，队列消息丢失也可修复。

V6 新增 `task_events` 与 `task_event_heads`；生命周期事件随状态事务一起提交，真实阶段进度经过租约校验后写入。SSE 只读取已提交事件，浏览器断线补发与 API 重启恢复均以它们为依据。

Redis Stream 只传任务 ID；`XREADGROUP` 分发、`XAUTOCLAIM` 接管未确认消息、Lua 原子执行 `XACK + XDEL`。MySQL 行锁、30 秒租约与每次唯一 token 决定执行权，2 秒心跳续租。过期租约会重试或最终失败；旧 Worker 的 token 无法覆盖新结果。每次完成先提交 MySQL，再确认消息。只能保证最终报告幂等，不能保证第三方调用恰好一次；恢复会从整个研究流程重新执行。

Worker 与 API 的旧任务恢复逻辑分开，旧同步请求依然只标记中断失败。所有 Worker 停机时，恢复和到期扫描也暂停，重新上线后补处理。

用于缓存的 Redis 仅缓存单个任务状态，键格式为 `{CACHE_PREFIX}:task:v1:{user_id}:{task_id}`。排队/运行中状态缓存 5 秒，结束状态缓存 60 秒。更新先提交 MySQL，再按版本更新缓存，防止旧状态覆盖新状态。Redis 不可用时回退到 MySQL；任务列表、阶段记录和报告直接读取 MySQL。

迁移文件位于 [`internal/repository/migrations`](internal/repository/migrations)。新增结构变更应添加编号递增的 SQL 文件，不要修改已应用的迁移，否则校验和检查会拒绝启动。

## 目录结构

```text
backend/
├── cmd/worker/main.go   # Worker 配置、信号、启动入口
├── main.go              # 服务生命周期、依赖初始化与路由注册
├── config.go            # 字符串和整数环境变量解析
├── research.go          # 问答/研究请求、Python 调用、结果与失败落库
├── limits.go            # 请求大小、并发与频率限制
├── web.go               # 安全响应头与静态文件服务
├── internal/
│   ├── handler/         # 账号、认证、同步输入与异步任务接口
│   ├── agent/           # Python 客户端、错误分类与输出校验
│   ├── queue/           # Redis Streams 消息传递
│   ├── service/         # 任务缓存、Worker 并发/心跳/重试与恢复
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
TEST_QUEUE_REDIS_URL='redis://127.0.0.1:26380/0' \
go test -count=1 -race ./...
go vet ./...
```

测试会写入数据，使用测试专用实例或数据库。研究接口测试使用 Agent 替身，无需调用真实模型。该 Compose 将 MySQL 数据放在临时文件系统中，端口与开发服务不同；完成各模块测试后，从项目根目录执行 `docker compose -f deploy/compose.test.yaml down`。不要替换为开发或生产连接。

V5 的 `async_integration_test.go` 使用真实隔离 MySQL / Redis 和模拟 Python HTTP，验证幂等与权限、并发上限、重复投递、重试耗尽、取消、旧 Worker 隔离、停机恢复、Redis 丢消息、到期与终态竞争；`internal/agent/client_test.go` 验证异常响应分类和大小限制。

V6 增加 `events_integration_test.go`：事务回滚不产生成功事件、顺序与补发、重复进度、旧租约拒绝、重试阶段复位、实时 Flush、心跳、连接限额释放、所有者检查和 JWT 到期。`internal/agent/stream_test.go` 验证进度先于最终报告、截断流、非法事件、大小限制和数据库回调失败。测试均不调用付费模型。
