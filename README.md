# AI Research Platform · 知序（学习版）

这是基于 V0～V9 业务实现整理的学习项目：Go / Gin 负责 API、用户、数据和后台任务，Python / OpenAI Agents SDK 负责问答与研究，原生 JavaScript 负责网页交互。

当前保留注册登录、研究与问答、MySQL 持久化、Redis 缓存、异步 Worker、取消与恢复、SSE 进度、用量与监控。学习版移除了测试、CI、评估脚本、演示脚本、生产部署工具和镜像归档，保留完整业务实现与必要运行配置。

原规划的工程功能已实现。此前真实评估中 20 题有 17 题生成报告、15 题通过机械检查，仍有报告质量改进空间；最后一轮公网浏览器和生产重启复核尚未完成。学习版不表示具备生产高可用或百分之百正确的回答。

## 先看哪里

**先读一条同步请求，再学数据库与异步任务。不要一上来通读整个项目。**

| 顺序 | 入口 | 这一轮要理解什么 |
| --- | --- | --- |
| 1 | [backend/main.go](backend/main.go) → [research.go](backend/research.go) → [agent-service/main.py](agent-service/main.py) | HTTP 路由、请求校验、Go 调用 Python、返回结果与超时 |
| 2 | [accounts.go](backend/internal/handler/accounts.go) → [mysql.go](backend/internal/repository/mysql.go) → [001_initial.sql](backend/internal/repository/migrations/001_initial.sql) | bcrypt、JWT、资源归属、SQL、事务与迁移 |
| 3 | [service/tasks.go](backend/internal/service/tasks.go) → [research/cache.py](agent-service/research/cache.py) | 缓存键、TTL、回源、一致性；两处缓存分别缓存什么 |
| 4 | [async_tasks.go](backend/internal/handler/async_tasks.go) → [jobs.go](backend/internal/repository/jobs.go) → [service/worker.go](backend/internal/service/worker.go) | 状态机、outbox、Redis Streams、幂等、租约、重试与取消 |
| 5 | [handler/events.go](backend/internal/handler/events.go) → [events.js](frontend/assets/events.js) | 持久化事件、SSE、游标补发与前端恢复 |
| 6 | [workflow.py](agent-service/research/workflow.py) → [searcher.py](agent-service/research/searcher.py) → [writer_agent.py](agent-service/research/writer_agent.py) | Planner → 并行搜索 → Writer、结构化输出与引用校验 |
| 7 | [limits.go](backend/limits.go) → [observability.go](backend/internal/repository/observability.go) → [telemetry.py](agent-service/research/telemetry.py) | 并发、日额度、关联日志、指标和用量边界 |

详细阅读路线和 V3～V9 面试资料保存在项目旁边的 `/Users/luananwei/web/项目文档`，从 `00-代码学习路线.md` 开始。模块索引见 [Go 后端](backend/README.md)、[Python Agent](agent-service/README.md)、[前端](frontend/README.md)。

## 目录

```text
ai-research-platform/
├── backend/                   # Go API 与独立 Worker
│   ├── main.go                # API 入口、依赖装配与路由
│   ├── research.go            # 同步请求链路
│   ├── cmd/worker/main.go     # Worker 入口
│   └── internal/
│       ├── handler/           # HTTP、鉴权、SSE、监控接口
│       ├── service/           # 缓存、任务协调与 Worker
│       ├── repository/        # SQL、事务、编号迁移
│       ├── queue/             # Redis Streams
│       ├── agent/             # 调用 Python、读取流式响应
│       ├── model/             # 业务结构与接口数据
│       └── observe/           # 关联日志与进程指标
├── agent-service/             # FastAPI 与 Agent 工作流
│   ├── main.py
│   └── research/
├── frontend/                  # HTML / CSS / JavaScript，无构建步骤
│   ├── index.html
│   └── assets/
├── compose.yaml               # 仅启动本机 MySQL 和两套 Redis
├── .gitignore
└── README.md
```

`go.mod/go.sum` 与 `pyproject.toml/uv.lock` 固定依赖；SQL 迁移、前端 vendor 库与许可证是运行必需内容。真实 `.env` 已保留但不提交 Git，虚拟环境按需重新生成。

## 本地启动完整功能

需要 Go 1.26.4+、Python 3.12+、uv、Docker 与 Compose。前端无需 Node.js。Go、Python、Worker 分别在三个终端运行，便于直接观察和调试代码。

### 1. 启动 MySQL 和 Redis

在项目根目录执行：

```bash
cd /Users/luananwei/web/ai-research-platform
docker compose up -d --wait
```

MySQL 为 `127.0.0.1:13306`，缓存 Redis 为 `16379`，队列 Redis 为 `16380`。Compose 保留原开发项目名 `ai-research-dev-data` 和数据卷；内置密码仅用于本机。停止时使用 `docker compose stop`，保留数据库。

### 2. 终端一：Python Agent

```bash
cd /Users/luananwei/web/ai-research-platform/agent-service
test -f .env || cp .env.example .env
uv sync --locked
uv run uvicorn main:app --reload --host 127.0.0.1 --port 8000
```

第一次运行前在 `.env` 填写模型 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`TAVILY_API_KEY`。启用搜索缓存时设置 `REDIS_URL=redis://127.0.0.1:16379/0`。Python 自动读取该文件；已有配置不需要重新填写。

### 3. 终端二：Go API

```bash
cd /Users/luananwei/web/ai-research-platform/backend
test -f .env || cp .env.example .env
set -a
. ./.env
set +a
go run .
```

首次使用示例配置时，把 `JWT_SECRET` 占位值替换为 `openssl rand -hex 32` 生成的随机值。已有密钥保持不变；配置中的开发数据库地址与根 Compose 一致。Go 自动执行尚未应用的 SQL 迁移，不要修改已执行迁移的内容。

### 4. 终端三：Go Worker

```bash
cd /Users/luananwei/web/ai-research-platform/backend
set -a
. ./.env
set +a
go run ./cmd/worker
```

API 和 Worker 共用数据库、队列地址与前缀。登录后的研究由 Worker 执行；只启动 API 会出现任务已创建但一直排队的情况。

### 5. 打开网页

访问 [http://127.0.0.1:8080](http://127.0.0.1:8080)，在右上角注册或登录。网站账号与数据库密码无关。注册不包含邮箱验证。

```bash
curl http://127.0.0.1:8000/health
curl http://127.0.0.1:8080/health
```

完整启用后 Go 应有 `mysql/redis/queue=ok`、`async_tasks=true`、`task_events=true`。健康接口不调用模型；实际问答与研究会产生上游用量。结束学习时在三个终端按 Ctrl+C，再从项目根目录执行 `docker compose stop`。

## 学习时先记住的边界

- 匿名 `POST /api/research` 同步等待；登录网页使用 `POST /api/tasks`，先返回 202 和任务编号。
- `chat` 按需使用工具；`research` 经过规划、搜索和写作，有结构与引用校验，区别不只是超时。
- MySQL 保存最终事实；缓存 Redis 可以丢失，队列 Redis 使用 AOF 和禁止淘汰策略。
- 任务可能重复调用上游，租约和事务保证旧执行者不能覆盖新结果，不保证第三方调用恰好一次。
- 报告目前基于搜索片段，URL 校验不代表结论已被逐句证实。

学习期先吃透现有调用链与失败分支，再考虑扩展功能。生产服务器与本地学习副本是独立的，本次本地清理没有改动服务器。
