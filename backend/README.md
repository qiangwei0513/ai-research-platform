# Go 后端阅读索引

启动步骤统一放在[项目 README](../README.md#本地启动完整功能)。先读同步请求，再读账号、数据库与异步任务。

## 文件职责

| 文件或目录 | 学习内容 |
| --- | --- |
| [main.go](main.go) | 初始化数据库/缓存/队列，装配 handler，注册路由，优雅退出 |
| [research.go](research.go) | `handleResearch`：参数、HTTP 调用、超时、结果校验与同步落库 |
| [config.go](config.go) / [limits.go](limits.go) | 配置默认值、并发槽与请求频率 |
| [web.go](web.go) | 静态文件、安全响应头与文件访问边界 |
| [handler/accounts.go](internal/handler/accounts.go) | bcrypt、JWT、注册登录、任务所有者校验 |
| [repository/mysql.go](internal/repository/mysql.go) | 连接、SQL、迁移与报告完成事务 |
| [repository/migrations/](internal/repository/migrations/) | 001 用户/任务/报告，002 后台任务，003 事件，004 观测与用量 |
| [service/tasks.go](internal/service/tasks.go) | 任务缓存、Lua 版本比较、回源、进度落库 |
| [handler/async_tasks.go](internal/handler/async_tasks.go) | 创建、取消、尝试记录、幂等键与配额错误 |
| [repository/jobs.go](internal/repository/jobs.go) / [outbox.go](internal/repository/outbox.go) | 状态流转、行锁、租约、投递补偿与重试 |
| [queue/streams.go](internal/queue/streams.go) | 投递、消费、接管、确认与删除消息 |
| [cmd/worker/main.go](cmd/worker/main.go) → [service/worker.go](internal/service/worker.go) | 后台进程启动，维护循环与消费循环 |
| [agent/client.go](internal/agent/client.go) | Worker 调用 Python，解析 NDJSON 进度与最终结果 |
| [handler/events.go](internal/handler/events.go) / [repository/events.go](internal/repository/events.go) | SSE 生命周期、持久化事件、快照、补发与心跳 |
| [observe/observe.go](internal/observe/observe.go) | request/task ID、结构化日志、低基数指标 |
| [repository/observability.go](internal/repository/observability.go) / [handler/observability.go](internal/handler/observability.go) | 用户日额度、按尝试用量、Worker 心跳和指标权限 |
| [model/](internal/model/) | 业务结构体、JSON 字段与用量校验 |

## 两条主调用链

同步：`main.go → handleResearch → Python /research → TaskService.Finish → Store.Finish`。匿名调用不保存账号历史。

异步：`CreateTask → Store.CreateAsyncTask（任务 + outbox 同事务）→ Worker.maintain → Redis Streams → Worker.process → agent.Client.Execute → Store.CompleteJob`。领取、续租和完成都受数据库状态与租约约束。

`web.go` 只负责网页文件，业务主线在 handler、service、repository 与 Worker 中。不要把“Go 入口只有一个 main”理解为“所有功能都在 main.go”。

## 接口速查

| 接口 | 用途 |
| --- | --- |
| `GET /ping` / `GET /health` | 进程与依赖状态 |
| `POST /api/auth/register` / `POST /api/auth/login` | 注册、获取 JWT |
| `POST /api/research` | 同步问答或研究，登录可选 |
| `POST /api/tasks` | 创建自己的后台任务，返回 202 |
| `GET /api/tasks` / `GET /api/tasks/:id` | 本人任务列表与详情 |
| `GET /api/tasks/:id/report` / `runs` / `attempts` / `usage` | 报告、阶段、执行尝试、用量 |
| `GET /api/tasks/:id/events` | 本人 SSE 事件，支持 Last-Event-ID |
| `POST /api/tasks/:id/cancel` | 取消自己的后台任务 |
| `GET /internal/metrics` | 独立 METRICS_TOKEN 保护的指标，未配置时关闭 |

登录接口返回的 `access_token` 放在 `Authorization: Bearer <token>` 请求头中。task ID 不是权限；读取、取消和事件订阅都要校验当前用户。

## 阅读时要能解释

1. 为什么不能在等待模型时一直持有数据库事务？
2. 为什么任务完成、报告保存和终态事件要一起提交？
3. 数据库成功、队列投递失败时，如何避免任务丢失？
4. 两个 Worker 同时收到同一任务，谁有执行权？
5. 缓存回填遇到状态更新，如何避免旧结果覆盖新结果？
6. 取消、超时、崩溃后为什么仍可能产生模型费用？

保留原有业务包结构，方便逐条调用链学习。自动测试已按学习版要求移除；编译可执行 `go build ./...`，静态检查可执行 `go vet ./...`。
