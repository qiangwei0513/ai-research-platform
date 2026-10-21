# Python Agent 服务

FastAPI 接收 Go 转发的研究请求，使用 OpenAI Agents SDK 调用模型，Tavily 获取联网搜索片段，Redis 缓存搜索结果。本目录是直接运行的服务项目，统一从 `main:app` 启动。

## 本地运行

在项目根目录执行：

```bash
cd agent-service
test -f .env || cp .env.example .env
uv sync --locked
```

编辑 `.env`，填写已验证可用的模型 Key、完整 Base URL 和 Tavily Key，再启动：

```bash
uv run uvicorn main:app --reload --host 127.0.0.1 --port 8000
```

健康检查为 `http://127.0.0.1:8000/health`，接口文档为 `http://127.0.0.1:8000/docs`。网页和账号接口由 [Go 后端](../backend/README.md) 提供；Python 不管理用户、JWT 或 MySQL。

## 配置

| 变量 | 说明 |
| --- | --- |
| `OPENAI_API_KEY` | 模型服务的 API Key |
| `OPENAI_BASE_URL` | OpenAI 兼容服务的完整 Base URL；未设置时使用 SDK 默认地址 |
| `TAVILY_API_KEY` | Tavily 搜索 Key；与模型 Key 分开配置 |
| `OPENAI_AGENTS_DISABLE_TRACING` | 示例设为 `1`，关闭 SDK 的外部 tracing |
| `REDIS_URL` | 可选搜索缓存；本地开发为 `redis://127.0.0.1:16379/0` |
| `CACHE_PREFIX` | 默认 `arp`；用于区分不同环境的缓存键 |
| `SEARCH_CACHE_TTL_SECONDS` | 默认 `900`，允许 `0～86400`；`0` 关闭缓存 |

当前各 Agent 的模型名称为 `deepseek-flash`，在 `research/*_agent.py` 中定义，模型服务需支持该名称。`research/__init__.py` 按文件位置加载本目录 `.env`，不依赖终端工作目录；已有进程环境变量优先。修改配置后重启服务。

检查配置是否非空，不输出密钥：

```bash
uv run python -c 'import research; import os; print({name: bool(os.getenv(name)) for name in ["OPENAI_API_KEY", "OPENAI_BASE_URL", "TAVILY_API_KEY"]})'
```

这不验证凭据有效性。`/health` 检查进程和 Redis，不调用模型或 Tavily；缺少搜索 Key 时联网搜索失败，本地笔记不受影响。

## 工作流与输出

```text
POST /research
├── chat     → Research Assistant → 按需调用 search_notes / search_web → 回答
└── research → Planner → Searcher 并行运行 Search Agent → Writer → 带来源的报告
```

请求为 `{query, mode, fresh}`。`query` 必填，去掉首尾空白后为 1～5000 个字符；`mode` 默认 `chat`，也可传 `research`；`fresh` 默认 `false`。

| 返回字段 | 内容 |
| --- | --- |
| `result` | 问答文本或 Markdown 研究报告 |
| `mode` | 实际执行模式 |
| `runs` | 阶段名称、状态、开始 / 结束时间和错误码 |
| `plan` | 深入研究的目标与搜索任务 |
| `search` | 搜索状态、摘要、失败信息、实际来源与缓存元数据 |

Planner 和 Writer 使用结构化输出；Searcher 是 Python 并发调度函数，不是独立 Agent。每个 Search Agent 都有独立工具记录，资料由代码传递。普通问答只要求返回非空文本；深入研究另外验证正文、来源编号及引用，不把空报告或全失败搜索当作正常结果。

部分搜索失败但还有资料时返回 `200`，`search.status=partial` 并注明缺口。搜索全失败 / 无可用资料等阶段错误返回 `502`，阶段或整体超时返回 `504`，`detail` 包含 `code`、`stage`、`message`、`runs`。普通问答的一般异常返回 `500`，由 Go 转为 `502`。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `main.py` | HTTP 输入输出、模式选择、问答预算、错误响应 |
| `research/research_agent.py` | 普通问答 Agent 与工具使用规则 |
| `research/planner_agent.py` | 研究目标与搜索任务规划 |
| `research/search_agent.py` | 调用搜索工具并生成摘要 |
| `research/searcher.py` | 并行调度、来源去重、汇总部分失败 |
| `research/writer_agent.py` | 生成报告草稿、检查来源编号、排版引用 |
| `research/workflow.py` | 规划 → 搜索 → 写作、阶段记录与错误处理 |
| `research/schemas.py` | 计划、来源、报告、阶段记录的 Pydantic 结构 |
| `research/web_search.py` / `tools.py` | Tavily 搜索 / 内置项目笔记搜索 |
| `research/cache.py` | 缓存键、TTL、实时绕过与 Redis 故障降级 |
| `research/timeouts.py` | 各阶段与完整流程的时间预算 |
| `research/demos/` | 按阶段运行的命令行演示 |
| `tests/` | 搜索缓存和工作流记录回归测试 |

## 搜索缓存

缓存保存 Tavily 返回的标题、URL 和片段，不保存模型摘要或最终报告。键包括搜索参数与规范化查询的摘要，查询仅去掉首尾空白并做 Unicode NFC 规范化，保留大小写和内部空格。空结果和失败不进入缓存。

`fresh=true` 或识别到「最新、今天、实时」等时效关键词时绕过缓存；结果保留 `cache_status`、`fetched_at` 供网页展示。Redis 不可用时仍直接搜索，读写各有最多 250 ms 预算；任务状态缓存属于 Go，规则见 [后端说明](../backend/README.md#数据存储与任务状态)。

## 手动演示

以下命令在 `agent-service` 目录执行，会调用真实模型或搜索服务：

| 验证范围 | 命令 |
| --- | --- |
| Planner | `uv run python -m research.demos.planner_agent_demo "研究问题"` |
| Search Agent | `uv run python -m research.demos.search_agent_demo "搜索词"` |
| 规划与并行搜索 | `uv run python -m research.demos.search_plan_demo "研究问题"` |
| 完整报告 | `uv run python -m research.demos.report_demo "研究问题"` |

命令行演示无需启动 Go 或 FastAPI，只向终端输出结果，不会保存到账号历史。要保存报告，请登录网页或使用带 Bearer token 的 Go API。

## 测试联网搜索

启动两个服务后，通过 Go 接口提交明确要求联网的问题：

```bash
curl -i --max-time 140 \
  -X POST http://127.0.0.1:8080/api/research \
  -H 'Content-Type: application/json' \
  -d '{"query":"请联网查找 Go 官方文档，解释 context 如何取消 HTTP 请求，并提供来源链接。"}'
```

检查 Python 终端是否出现 `search_web` 的执行日志和 `[source]` 链接，再核对回答中
的来源是否来自实际搜索结果。仅看到回答或 HTTP 200，不能证明发生了联网搜索。

也可以在 `agent-service` 目录单独测试搜索 API，跳过 Go 和模型调用：

```bash
uv run python -c 'import asyncio, json; from research.web_search import fetch_web_results; result = asyncio.run(fetch_web_results("site:go.dev context cancellation")); print(json.dumps(result, ensure_ascii=False, indent=2))'
```

## 单独测试 Search Agent

在 `agent-service` 目录执行：

```bash
uv run python -m research.demos.search_agent_demo "site:go.dev context cancellation"
```

`search_web` 使用 `@function_tool` 将现有 Tavily 搜索封装为模型可调用的函数工具。
Search Agent 接收搜索词与目的，可微调搜索词，然后调用工具并生成简短摘要。
这条路径使用自己的 Tavily 配置，不依赖模型服务支持 OpenAI 托管的 `WebSearchTool`。

成功时会看到 `[search_agent]`、`[tool] search_web` 和 `[source]` 日志，最终 JSON 包含：

- `task.query` / `reason`：原始计划中的搜索词和目的。
- `task.executed_queries`：工具实际执行的搜索词。
- `task.summary`：模型根据搜索片段生成的摘要。
- `sources`：工具实际返回的标题、URL 和原始片段。

每次运行使用独立的 `WebSearchContext` 保存真实工具结果，来源信息不从模型摘要中提取。
模型没有实际完成工具调用时，任务会失败；工具返回空结果时记为 `empty`，摘要置空。
Search Agent 每个任务最多调用一次搜索工具，最多生成 1200 字符摘要；V4 缓存命中时不会再次调用 Tavily。
当前模型服务的思考模式不支持 `tool_choice="required"`，因此使用 `auto`，
再由代码检查工具是否真正执行；仅返回一段回答不算搜索成功。

相较直接调用搜索 API，现在每个搜索任务还需要模型决定工具调用并生成摘要，因此耗时和模型用量会增加。
完整规划与并行搜索仍可通过 `python -m research.demos.search_plan_demo "研究问题"` 验证。

## 生成带引用的研究报告

完成依赖安装和 `.env` 配置后，在项目根目录执行：

```bash
cd agent-service
uv run python -m research.demos.report_demo "研究蝴蝶效应"
```

该命令直接调用模型与 Tavily，无需启动 Go 或 FastAPI。终端依次显示 `[planner]`、
`[searcher]`、`[search_agent]`、`[writer]` 日志，最后输出包含结论、分析、适用条件、局限与资料缺口、
搜索状态说明和来源列表的 Markdown 报告。当前报告输出到终端，尚未自动保存或入库。

Writer 接收原始问题、搜索计划、资料以及搜索失败信息，输出 `ReportDraft`。
搜索任务中的 `summary` 作为辅助摘要传给 Writer，原始搜索片段仍用于核对事实与引用；摘要不作为独立来源。
每节包含正文和 `source_ids`；Python 校验编号后，使用实际搜索结果中的 URL 生成引用链接。
出现未知来源编号、完全没有引用、空正文或正文超长时，会拒绝返回报告。
来源编号检查通过，只代表引用来源存在，不代表来源在语义上支持该节所有结论。

部分搜索失败时，只要还有可用片段，就继续写作，并在报告中记录失败；
全部搜索失败或没有可用片段时，停止写作。失败表示本次未能完成检索，不表示不存在相关资料。

当前运行限制：

| 项目         | 限制                                                                                                                      |
| ------------ | ------------------------------------------------------------------------------------------------------------------------- |
| Planner      | 45 秒超时，生成 2～3 个搜索任务                                                                                           |
| Searcher     | 每次研究默认最多同时运行 2 个 Search Agent                                                                                |
| Search Agent | 每任务 90 秒（包括模型调用和搜索），`max_turns=3`，`max_tokens=10000`；工具最多调用一次，单次 Tavily 搜索仍限时 25 秒 |
| 完整搜索阶段 | 185 秒，覆盖最多 3 个任务、并发 2 时的两轮执行                                                                            |
| Writer 资料  | 最多 9 个来源，每个来源最多 2 个片段，每段最多 900 字符                                                                   |
| Writer 输入  | 序列化后的完整输入最多 30000 字符，超出则报错                                                                             |
| Writer 输出  | `max_tokens=10000`；提示词要求每节不超过 1000 字，程序检查每节最多 1500 字符                                            |
| Writer 调用  | 60 秒超时，`max_turns=1`                                                                                                |

HTTP 和命令行完整研究共用 300 秒整体预算，到期后取消正在等待的阶段和搜索任务。
该预算覆盖规划、排队、搜索和写作；不是每个阶段都可以额外使用 300 秒。
token 数和汉字数不同。若出现 `response.incomplete` 且原因为 `max_output_tokens`，
表示模型输出触及 token 上限；可缩短报告要求，或在模型服务支持的范围内调整 Writer 的输出预算。

## 联网搜索的当前限制

- 当前使用 Tavily 的 `basic` 搜索，只读取返回的内容片段，不主动打开来源网页或抓取全文。
- 搜索层每条片段截取前 1500 个字符，Writer 输入还会进一步截取为每段最多 900 字符，因此可能缺少完整上下文；有来源链接不代表已阅读或核验全文。
- Agent 根据片段生成回答，引用准确性仍需核对；当前没有自动验证结论与来源是否一致。
- 搜索结果可能为空，也不保证覆盖所有相关资料或最新信息。
- 搜索结果默认可缓存 15 分钟；时效性问题自动跳过缓存，也可通过 `fresh: true` 明确绕过。命中只复用片段，不复用模型摘要或报告。
- Tavily 请求设置了 25 秒期限，缓存读写另各有最多 250 ms 的预算；普通问答受 120 秒总预算限制，完整研究受 300 秒总预算限制。
- 普通问答中的搜索工具异常会由 Python 接口返回 500，再由 Go 转为 502；完整研究由 Searcher 记录各任务失败，按剩余资料决定是否继续写作。

## 测试

在 `agent-service` 目录执行：

```bash
uv sync --locked
uv run python -m pytest -q
```

自动测试模拟模型和 Tavily，不消耗真实 API 用量。未提供 `TEST_REDIS_URL` 时跳过依赖真实 Redis 的缓存用例；完整运行时，先从项目根目录启动独立测试服务，再指定测试端口：

```bash
docker compose -f deploy/compose.test.yaml up -d --wait
cd agent-service
TEST_REDIS_URL=redis://127.0.0.1:26379/0 uv run python -m pytest -q
```

完成所有模块测试后，从项目根目录执行 `docker compose -f deploy/compose.test.yaml down`。该测试配置不挂载开发数据库的数据卷，不要把测试连接改为开发或生产数据库。

## V5 Worker 调用与取消

Go Worker 复用 `POST /research`，通过 `X-Task-ID` 携带任务标识（用于串联调用，Python 本身不做任务持久化或队列消费）。FastAPI 路由将实际执行与 ASGI 断连监听并行等待：调用方断开后取消本地工作流协程，并回收监听任务。数据库终态和重试仍由 Go 决定；第三方已经接受的模型请求可能继续产生用量。

`tests/test_workflow_records.py` 新增断连和超时测试，确认 Agent 子协程会被取消，普通 HTTP 成功响应也能正确结束监听。

## V6 实时阶段传输

V6 在同一个 `POST /research` 增加 `Accept: application/x-ndjson` 协商。不带该请求头的同步调用仍返回原 JSON；新版 Worker 接收逐行进度，旧 JSON 上游也仍兼容，但没有实时阶段帧。

NDJSON 一行是一个完整 JSON，类型为 `progress`、`heartbeat`、`result` 或 `error`。例如：

```json
{"type":"progress","data":{"stage":"search","completed":1,"total":3}}
```

`result.data` 使用原研究响应结构；`error` 带 `status` 和原 `detail`。开始流后 HTTP 已是 200，所以执行失败通过终止帧表达；200 本身不能证明研究成功。每 5 秒空闲心跳；队列最多 16 帧，慢读取会产生背压；调用方断开时取消并回收 producer。

`research/progress.py` 用 ContextVar 隔离并发请求的回调。Planner、Writer 和 Chat 在实际开始/完成时发出 0/1 进度；Searcher 在每项搜索成功、空结果或失败结束后递增计数，取消中的搜索不算完成。并发搜索使用锁保证计数与入队顺序一致。只有 Go Worker 保存报告后才产生面向网页的 `succeeded` 事件；Python 的 Writer 完成不等于最终入库。

`tests/test_streaming.py` 覆盖阶段顺序、部分搜索失败、并发上下文隔离、终止错误、背压，以及 ASGI 2.3/2.4 断连后的协程清理。这些测试模拟上游，不验证真实模型质量。
