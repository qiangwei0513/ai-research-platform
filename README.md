# AI Research Platform

使用 Go + Gin 和 Python + OpenAI Agents SDK 构建的研究助手项目。

现已提供网页入口「知序」：无需登录，可选择深入研究或快速问答，查看报告、搜索计划和来源，复制或下载 Markdown。
前端使用 HTML / CSS / JavaScript，由 Go 同源提供，无需安装 Node.js。

- 本地使用：启动下方的 Python 与 Go 服务后，打开 **http://127.0.0.1:8080**。
- 页面功能、代码职责与修改方法：[前端说明](frontend/README.md)。
- Ubuntu / Debian 上线、子域名与现有网站共存：[部署说明](deploy/README.md)。

## 开发路线图：V0 → V9

进度更新日期：2026-10-14。路线沿用项目最初的 V0～V9 规划，V9 是本项目第一轮完整交付的终点。

图中的 `✓` 和下方清单的 `[x]` 表示已实现的目标，未勾选项为后续计划。

```mermaid
flowchart TB
    subgraph CORE["核心能力 · V0–V4"]
        direction LR
        V0("V0 ✓<br/>服务闭环")
        V1("V1 ✓<br/>工具搜索")
        V2("V2 ✓<br/>研究工作流")
        V3("V3<br/>MySQL")
        V4("V4<br/>Redis")
        V0 --> V1 --> V2 --> V3 --> V4
    end

    subgraph DELIVERY["产品交付 · V5–V9"]
        direction LR
        V5("V5<br/>异步任务")
        V6("V6<br/>SSE 与前端")
        V7("V7<br/>容器部署")
        V8("V8<br/>HTTPS 上线")
        V9("V9<br/>可靠性完善")
        V5 --> V6 --> V7 --> V8 --> V9
    end

    CORE --> DELIVERY

    style CORE fill:transparent,stroke:transparent
    style DELIVERY fill:transparent,stroke:transparent

    classDef done fill:#dcfce7,stroke:#15803d,color:#14532d;
    classDef planned fill:#f1f5f9,stroke:#64748b,color:#0f172a;
    class V0,V1,V2 done;
    class V3,V4,V5,V6,V7,V8,V9 planned;
```

完整目标与验收方式见文末的[阶段目标与验收](#阶段目标与验收)。
图中的前后关系表示主线学习顺序：基础校验、超时和错误处理从 V0 开始，后续每一阶段继续完善。
为了先分享给他人使用，已提前补充最小网页和容器部署配置。V3 暂未开始；V6 的实时进度、登录和历史列表，以及 V7/V8 的完整部署目标仍按后续清单推进。

## 请求流程

HTTP 接口通过 `mode` 选择流程；不传时默认使用 `chat`，兼容原有调用：

```text
浏览器（Go 提供 frontend/ 静态页面）或 API 客户端
  → Go Gin（8080）
  → Python FastAPI（8000）
  ├─ mode=chat     → Research Assistant → 按需调用工具 → 返回回答
  └─ mode=research → 统一研究流程 → 返回报告、计划和搜索资料
```

完整研究通过 `workflow.py` 编排，HTTP 与命令行共用：

```text
研究问题 → Planner Agent → Searcher 并行调度 Search Agent → Writer Agent → Markdown 报告
                                            ↓
                              search_web 工具 → Tavily → 搜索片段与摘要
```

这是由 Python 代码按固定顺序编排的多 Agent 工作流，各模块通过结构化数据协作。

| 模块         | 实现方式        | 职责                                                     |
| ------------ | --------------- | -------------------------------------------------------- |
| Planner      | Agent，调用模型 | 生成研究目标和 2～3 个互补的搜索任务                     |
| Search Agent | Agent，调用模型 | 调用`search_web` 工具搜索 Tavily，并根据片段生成摘要   |
| Searcher     | Python 异步函数 | 并行运行 Search Agent、限制并发、按 URL 去重并记录失败   |
| Writer       | Agent，调用模型 | 根据资料生成结构化报告草稿，由 Python 检查来源编号并排版 |

研究工作流包含 Planner、Search、Writer 三种 Agent 角色。Searcher 为每个计划任务运行一次
Search Agent，各次运行有独立的工具结果记录，由代码传递资料，无需 Agent 之间互相聊天。
`Research Assistant` 用于普通问答；完整研究由 `mode="research"` 显式触发。

## 项目结构

```text
ai-research-platform/
├── backend/
│   ├── main.go
│   ├── web.go               # 静态页面、安全响应头、请求大小与公开服务配额
│   ├── go.mod
│   └── go.sum
├── agent-service/
│   ├── .env                  # 本地配置，不提交到 Git
│   ├── main.py
│   ├── research/
│   │   ├── __init__.py
│   │   ├── research_agent.py # 当前 HTTP 接口使用的问答 Agent
│   │   ├── schemas.py        # 搜索计划、资料集合与报告结构
│   │   ├── planner_agent.py  # 研究规划 Agent
│   │   ├── search_agent.py   # 调用搜索工具并总结资料的 Agent
│   │   ├── writer_agent.py   # 写作 Agent、来源编号校验与报告排版
│   │   ├── searcher.py       # 并行调度 Search Agent、去重与资料汇总
│   │   ├── workflow.py       # 统一研究流程、阶段日志、失败与超时处理
│   │   ├── timeouts.py       # Python 各阶段和整体时间预算
│   │   ├── tools.py          # 本地笔记搜索
│   │   ├── web_search.py     # Tavily 联网搜索工具
│   │   └── demos/            # 手动运行的演示与验收入口
│   │       ├── __init__.py
│   │       ├── planner_agent_demo.py # 单独验证 Planner Agent
│   │       ├── search_agent_demo.py  # 单独验证 Search Agent
│   │       ├── search_plan_demo.py   # 规划 → 并行搜索
│   │       └── report_demo.py        # 规划 → 搜索 → 写作
│   ├── pyproject.toml
│   └── uv.lock
├── frontend/
│   ├── index.html           # 网页结构
│   ├── assets/              # 样式、交互脚本、SVG、本地 Markdown 依赖
│   └── README.md            # 页面功能与代码说明
├── deploy/                  # Dockerfile、Caddy、环境示例与部署指南
├── compose.yaml             # Python + Go + Caddy
├── .dockerignore
├── .gitignore
└── README.md
```

Agent 定义统一放在 `*_agent.py` 中；`searcher.py` 是并行调度与汇总模块，不是独立 Agent。
手动演示集中在 `research/demos/`。
在 `agent-service` 目录使用以下命令运行演示（会调用相应的模型或搜索服务）：

| 验证范围       | 命令                                                              |
| -------------- | ----------------------------------------------------------------- |
| Planner Agent  | `uv run python -m research.demos.planner_agent_demo "研究问题"` |
| Search Agent   | `uv run python -m research.demos.search_agent_demo "搜索词"`    |
| 规划与并行搜索 | `uv run python -m research.demos.search_plan_demo "研究问题"`   |
| 完整报告流程   | `uv run python -m research.demos.report_demo "研究问题"`        |

## 开发环境

- Go：版本要求见 backend/go.mod。
- Python：版本要求见 agent-service/pyproject.toml。
- uv：用于安装 Python 依赖和运行服务。
- 可用的模型服务配置。

## 配置模型与搜索服务

在 [Tavily 控制台](https://app.tavily.com) 获取搜索 API Key。它与模型服务的 API Key 分别配置。

在 `agent-service/.env` 中填写以下配置，将占位文字替换为已验证可用的值：

```dotenv
OPENAI_API_KEY="你的模型服务 API Key"
OPENAI_BASE_URL="你已验证可用的完整模型服务 Base URL"
TAVILY_API_KEY="你的 Tavily API Key"
```

`research/__init__.py` 会通过 `python-dotenv` 加载该文件。启动服务或导入
`research.web_search` 等模块时都会触发加载，无需每次新开终端后重新 `export`。
配置文件路径根据代码文件的位置确定，不依赖终端当前目录。

已有环境变量优先于 `.env` 中的值（`override=False`）。如果旧终端的配置需要改为使用
`.env`，先执行 `unset OPENAI_API_KEY OPENAI_BASE_URL TAVILY_API_KEY`，再启动服务。
修改 `.env` 后需重新启动 Python 服务。

`.env` 已被根目录 `.gitignore` 忽略，不要将真实密钥写入代码、README 或提交到 Git。
缺少 `TAVILY_API_KEY` 时，联网搜索会失败；本地笔记搜索不需要这个 Key。

安装依赖后，在 `agent-service` 目录执行以下命令检查配置是否存在，输出不会显示密钥：

```bash
uv run python -c 'import research; import os; print({name: bool(os.getenv(name)) for name in ["OPENAI_API_KEY", "OPENAI_BASE_URL", "TAVILY_API_KEY"]})'
```

该检查只确认变量非空，不验证密钥或服务地址是否有效。

## 启动 Python 服务

在项目根目录打开终端：

```bash
cd agent-service
uv sync --locked
uv run uvicorn main:app --reload --host 127.0.0.1 --port 8000
```

健康检查：http://127.0.0.1:8000/health

接口文档：http://127.0.0.1:8000/docs

## 启动 Go 服务

在项目根目录另开一个终端：

```bash
cd backend
go mod download
go run .
```

健康检查：http://127.0.0.1:8080/ping

网页入口：http://127.0.0.1:8080 。使用 `go run .` 编译该目录全部 Go 文件。
不要双击 `frontend/index.html`；页面需要 Go 提供同源 API。

修改 Go 代码后，需要重新启动 Go 服务。

Go 可通过环境变量配置监听地址和 Python 接口，默认值与以上启动命令一致：

```bash
GO_ADDR=127.0.0.1:8080 AGENT_SERVICE_URL=http://127.0.0.1:8000/research go run .
```

### 网页与公开访问配置

| 环境变量 | 默认值 | 用途 |
|---|---|---|
| `FRONTEND_DIR` | `../frontend` | 相对于 Go 进程启动目录的静态页面路径 |
| `MAX_CONCURRENT_RESEARCH` | `2` | 整个 Go 进程同时处理的问答和研究总数 |
| `RATE_LIMIT_PER_MINUTE` | `20` | 所有访客共享的每分钟请求次数；`0` 关闭频率限制 |

超过并发或频率限制返回 429，页面会根据 `Retry-After` 提示稍后重试；不排队，也不自动重试。
单个 JSON 请求体限制为 64 KiB。静态服务只公开首页和允许类型的 `/assets/*`，不公开项目配置、源代码或目录列表。

页面默认选择深入研究，但 API 不传 `mode` 时仍默认普通问答。等待页面仅显示实际耗时；目前没有 SSE 实时阶段进度。
页面结果不持久化，刷新后消失。完整功能与边界见[前端说明](frontend/README.md)。

## 发起研究请求

普通问答（省略 `mode` 或指定 `"mode":"chat"`）：

```bash
curl -i --max-time 140 \
  -X POST http://127.0.0.1:8080/api/research \
  -H 'Content-Type: application/json' \
  -d '{"query":"请解释 Redis 缓存穿透，并举一个例子。"}'
```

成功响应示例：

```json
{
  "result": "模型生成的回答",
  "mode": "chat"
}
```

完整研究（显式指定 `"mode":"research"`）：

```bash
curl -i --max-time 320 \
  -X POST http://127.0.0.1:8080/api/research \
  -H 'Content-Type: application/json' \
  -d '{"query":"研究 Go context 的取消传播机制，以官方文档为依据。","mode":"research"}'
```

研究模式的成功响应包含以下字段：

| 字段       | 内容                                                   |
| ---------- | ------------------------------------------------------ |
| `result` | 带引用的 Markdown 报告，保留原有答案字段               |
| `mode`   | `research`                                           |
| `plan`   | 研究目标和搜索任务                                     |
| `search` | 搜索状态、每个任务的摘要与失败信息、去重后的来源和片段 |

部分搜索失败但仍有可用资料时，HTTP 返回 200，`search.status` 为 `partial`，报告保留失败说明。
全部搜索失败返回 502，`code` 为 `search_failed`；没有可用片段返回 502，`code` 为 `no_sources`。
阶段或整体超时返回 504，`code` 为 `research_timeout`，`stage` 标明超时时所处阶段。
其他研究阶段错误返回 502；不把空报告或搜索失败当作正常报告返回。

研究流程会打印带 `run` 编号的 `[workflow]` 日志，记录各阶段的开始、结束、耗时，
以及失败时的错误类型或错误码。该编号用于本次 Python 流程日志关联，尚未贯通 Go 和后续任务存储。

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
Search Agent 每个任务最多调用一次 Tavily，最多生成 1200 字符摘要。
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
- 单次搜索操作设置了 25 秒期限；普通问答受 120 秒总预算限制，完整研究受 300 秒总预算限制。
- 普通问答中的搜索工具异常会由 Python 接口返回 500，再由 Go 转为 502；完整研究由 Searcher 记录各任务失败，按剩余资料决定是否继续写作。

## Go 接口状态码

| 状态码 | 含义                                       |
| ------ | ------------------------------------------ |
| 200    | 成功返回答案                               |
| 400    | 请求格式错误、问题不符合要求或 mode 无效   |
| 413    | JSON 请求体超过 64 KiB                    |
| 429    | 全局并发或每分钟请求数已达上限             |
| 500    | Go 内部构造请求失败                        |
| 502    | Agent 服务连接失败、执行失败或返回无效结果 |
| 504    | Agent 执行或通信超时                       |

问题去除首尾空白后，长度应为 1～5000 个字符。

| 模式                 | Python 总预算 | Go 等待上限 | 建议 curl 上限 |
| -------------------- | ------------- | ----------- | -------------- |
| 普通问答`chat`     | 120 秒        | 130 秒      | 140 秒         |
| 完整研究`research` | 300 秒        | 310 秒      | 320 秒         |

## 服务器部署

最小上线配置使用 Docker Compose 运行 Python、Go 和 Caddy；Go 同时提供网页与 API。
默认只公开 80、443，Caddy 负责 HTTPS，模型密钥仅注入 Python 容器。

具体步骤见 [Ubuntu / Debian 部署指南](deploy/README.md)，示例子域名为 `agent.luananwei.me`。
如果个人简历已占用服务器的 80/443 端口，指南提供复用现有 Nginx / Caddy / 面板代理的方案，无需替换根域名简历。

当前没有用户登录、历史保存、后台队列和任务恢复。所有访客共享你配置的模型与搜索额度；全局限流用于控制同时运行的工作量，不提供每日费用上限。

## 阶段目标与验收

以下列出各阶段的目标、任务与验收标准，具体进度通过任务清单和路线图展示。

### V0：最小服务闭环

**目标：** 用户提交问题，Go 调用 Python，Python 调用模型，最终返回答案。

- [X] 初始化独立项目、Go 模块和 Python 依赖环境。
- [X] Gin 提供 `GET /ping` 和 `POST /api/research`。
- [X] FastAPI 提供 `GET /health` 和 `POST /research`。
- [X] 使用 `Agent + Runner` 执行模型调用，返回 `{"result": "..."}`。
- [X] 校验输入，处理服务不可用、无效响应与请求超时。
- [X] 添加 README、Git 忽略规则，提交代码并推送到 GitHub。

**验收：** 从 Go 的 8080 端口提交问题可得到真实回答；空白问题返回 400；停止 Python 后 Go 返回 502 且自身保持运行。

**学习重点：** HTTP 服务与客户端、JSON 编解码、异步函数、环境变量、Git 基础。

### V1：工具调用与联网搜索

**目标：** Agent 能获取外部信息，使用实际返回的资料组织回答。

- [X] 用 `function_tool` 封装 `search_notes`，搜索本地项目笔记。
- [X] 接入 Tavily，提供 `search_web` 和可独立调用的 `fetch_web_results`。
- [X] 返回标题、URL 和内容片段；每次最多 3 条，每条最多 1500 个字符。
- [X] 在提示词中区分本地笔记搜索、联网搜索及引用规则。
- [X] 打印工具执行与来源日志，处理搜索超时、认证失败和网络异常。
- [X] 自动加载 `agent-service/.env`，配置模型与搜索服务的 Key。
- [X] 记录搜索片段的限制，提供单独搜索与完整接口测试命令。

**验收：** 本地笔记问题可引用笔记编号；联网问题触发真实搜索并返回来源链接；能从日志核对工具执行和来源。
本阶段不包含网页全文读取，也不代表已经自动验证引用与结论的一致性。

**学习重点：** 函数工具、模型与工具之间的数据传递、第三方 API、异步 HTTP、来源追溯。

### V2：Planner → 并行搜索 → Writer

**目标：** 对复杂研究问题先规划，再收集多组资料，最后生成有依据的报告。

#### V2.1：生成结构化搜索计划

- [X] 定义 `SearchItem` 与 `SearchPlan`。
- [X] Planner 输出研究目标、2～3 个搜索词及其目的。
- [X] 检查空内容、任务数量、完全重复的搜索词，并设置规划超时。
- [X] 提供 `python -m research.demos.planner_agent_demo` 独立验证入口。
- [X] 确认多个研究问题都能生成结构正确、搜索角度互补的计划。

Planner 已接入完整研究流程，也可在 `agent-service` 目录独立验证：

```bash
uv run python -m research.demos.planner_agent_demo "你的研究问题"
```

#### V2.2：并行搜索与资料汇总

- [X] 复用 `fetch_web_results`，按照计划执行每个搜索任务。
- [X] 新增 Search Agent，调用 Tavily 函数工具并生成摘要；Searcher 使用异步并发并限制同时运行数量。
- [X] 保留每次搜索的查询词、搜索目的、来源标题、URL 和片段。
- [X] 按 URL 去重；相同来源保留与不同搜索任务的关联。
- [X] 区分搜索成功、结果为空和执行失败；部分失败可继续，全失败明确返回失败。
- [X] 为 Writer 提供结构统一的资料集合与失败说明。
- [X] 保存实际工具结果、实际查询词与摘要；校验工具确实执行，并提供单个 Search Agent 验证入口。

```Shell
uv run python -m research.demos.search_plan_demo "研究问题"
```

#### V2.3：生成带引用的报告

- [X] 新增 Writer Agent，输入研究问题、计划、资料与搜索失败信息。
- [X] 输出包含结论、分析、适用条件、局限和来源的 Markdown 报告。
- [X] 资料不足时明确说明；不把搜索失败表述成“没有相关资料”。
- [X] 校验报告的来源编号，并使用实际资料中的 URL 生成链接；语义上是否支持结论仍需单独评估。
- [X] 控制输入资料长度与输出篇幅，避免无界增长。
- [X] 提供 `python -m research.demos.report_demo`，串联规划、搜索与写作。

运行方式与限制见[生成带引用的研究报告](#生成带引用的研究报告)。

#### V2.4：接入服务并验收

- [X] 用统一流程函数串联规划、搜索和写作，并接入 HTTP 研究入口。
- [X] 明确普通问答与完整研究的入口或模式，避免每个简单问题都执行全部步骤。
- [X] 重新分配各阶段超时，并协调 Python 总超时与 Go 客户端超时。
- [X] 记录各阶段开始、结束、耗时与失败原因。
- [X] 自动化测试验证完整成功、部分搜索失败、全部搜索失败和整体超时。
- [X] 通过 Go → Python 发起真实研究请求，返回计划、资料与带引用的报告。

**验收：** 一次完整研究请求能输出计划、执行多项搜索并生成有来源的报告；发生部分失败时报告说明资料缺口，全部失败时不编造报告。

**学习重点：** 结构化输出、工作流编排、异步并发、失败隔离、资料管理与引用校验。

### V3：MySQL 持久化与用户体系

**目标：** 研究记录可以长期保存，用户能查询自己的任务和报告。

- [ ] 设计 `users`、`tasks`、`agent_runs`、`reports` 等表及关联关系；`messages` 在需要多轮会话时再引入。
- [ ] 添加数据库配置、版本化迁移与连接检查。
- [ ] 将 Go 代码逐步拆分为 handler、service、repository、model。
- [ ] 保存研究问题、执行状态、开始与结束时间、阶段记录、报告及来源。
- [ ] 提供任务列表、任务详情和报告查询接口，支持分页。
- [ ] 增加注册、登录、密码哈希、JWT 校验和资源归属检查。
- [ ] 为任务状态与报告保存设计事务边界，避免出现“成功但报告未保存”。

**验收：** 服务重启后历史仍可查询；用户只能读取自己的研究记录；失败任务也有可查询的状态与原因。

**学习重点：** 数据建模、SQL、事务、迁移、分层设计、认证和权限控制。

### V4：Redis 缓存与状态读取

**目标：** 减少重复搜索与高频数据库查询，为异步任务阶段准备基础设施。

- [ ] 接入 Redis 客户端、连接配置与可用性检查。
- [ ] 缓存适合复用的搜索结果，缓存键包含规范化查询词和影响结果的搜索参数。
- [ ] 为缓存设置 TTL，明确时效性问题何时绕过缓存；不缓存执行失败作为正常结果。
- [ ] 缓存高频读取的任务状态，明确 MySQL 是持久化状态的最终依据。
- [ ] 设计更新或失效策略，防止缓存持续显示旧的任务状态。
- [ ] Redis 不可用时，缓存功能可回退到数据库或原始搜索，并记录降级情况。

**验收：** 相同查询在缓存有效期内可复用结果；过期后重新获取；关闭 Redis 不导致历史记录丢失。

**学习重点：** 缓存键、TTL、缓存一致性、命中率与降级。任务队列在 V5 实现。

### V5：异步任务与 Worker

**目标：** 创建研究任务后立即返回任务 ID，后台持续执行，客户端按需查询结果。

- [ ] 新增 `POST /api/tasks`、`GET /api/tasks/:id` 等接口；任务归属沿用 V3 的权限校验。
- [ ] 建立 `queued → running → succeeded / failed / cancelled` 状态流转。
- [ ] 基于 Redis Streams 或成熟队列方案实现投递、消费和确认。
- [ ] 处理数据库写入与入队之间的失败，例如通过 outbox 或定期补偿避免漏投任务。
- [ ] 将 Worker 与 HTTP 请求生命周期分开，限制执行并发数。
- [ ] 实现幂等处理、有限重试、任务超时、失败记录及 Worker 中断后的恢复。
- [ ] 支持查询或取消任务，并明确排队中与执行中的取消行为。

**验收：** 创建任务快速返回 ID；关闭客户端不影响后台执行；重复投递不会重复保存报告；Worker 重启后任务可恢复或明确失败。

**学习重点：** 队列、消费者、状态机、幂等性、重试、任务恢复。Redis 此时承担队列职责，需配置持久化与恢复策略。

### V6：SSE 执行进度与最小前端

**目标：** 用户可以在网页提交研究、查看进度、阅读报告和历史记录。

- [ ] 提供任务事件接口，例如 `GET /api/tasks/:id/events`，检查访问权限。
- [ ] 定义排队、规划完成、搜索进度、写作、成功、失败和取消等事件。
- [ ] 将运行状态展示与最终报告区分，使用阶段和已完成任务数表达进度。
- [ ] 实现 SSE 心跳、断线重连、事件序号和终态通知；重连后能恢复当前状态。
- [ ] 在 JWT 认证方案下明确 SSE 的认证方式，避免把长期凭据放进 URL。
- [ ] 完成登录、研究提交、任务列表、任务详情、报告与来源链接等页面。
- [X] 安全渲染 Markdown，提供加载、空结果、失败和重试提示。

**验收：** 从浏览器完成登录、创建研究、观察进度和阅读报告；刷新或断网重连后能恢复状态；不能订阅其他用户的任务。

**学习重点：** SSE、事件设计、前后端联调、连接生命周期和状态恢复。

### V7：Docker Compose 本地部署

**目标：** 在具备容器环境的机器上，按照文档统一启动整个项目。

- [ ] 为 Go、Python、Worker 和前端构建镜像，按实际进程划分服务。
- [ ] Compose 管理应用、MySQL、Redis、内部网络和持久化卷。
- [X] 将服务地址、监听地址和端口改为配置项；容器间通过服务名访问。
- [ ] 配置健康检查、启动依赖及应用自身的连接重试。
- [X] 提供不含真实密钥的配置模板，避免将 `.env` 和凭据打入镜像。
- [ ] 写明数据库迁移、启动、停止、日志查看和数据备份步骤。

**验收：** 新环境按 README 配置后可启动完整系统；应用容器重建后，数据库和报告仍然保留。

**学习重点：** 镜像、容器网络、服务发现、数据卷和多服务配置。

### V8：Nginx、HTTPS 与服务器部署

**目标：** 通过自己的域名访问应用，完成一次真实部署。

- [ ] 准备服务器、域名解析及应用配置。
- [ ] 使用 Nginx 提供前端并反向代理 Go API。
- [ ] 配置 HTTPS、证书续期和 HTTP 到 HTTPS 跳转。
- [ ] 为 SSE 配置适合的代理缓冲与连接超时策略。
- [ ] 限制 MySQL、Redis 和 Python 内部接口的公网访问。
- [ ] 建立部署、迁移、备份、恢复和回滚步骤。

**验收：** 域名 HTTPS 可访问；登录、创建任务、进度与报告均可用；重启服务不丢失历史；SSE 不被代理长时间缓冲。

**学习重点：** 反向代理、TLS、域名、网络边界和部署运维。

### V9：可靠性与可观测性完善

**目标：** 让系统具备可解释、可维护、可演示的工程质量。

- [ ] 对用户请求、搜索调用和 Worker 执行设置限流、并发上限与资源预算。
- [ ] 统一各层超时，区分可重试和不可重试错误，采用有限次数的退避重试。
- [ ] 使用 request ID、task ID 串联 Go、Python、队列和阶段日志，避免记录密钥。
- [ ] 记录成功率、延迟、队列积压、缓存命中和可获得的模型或搜索用量指标。
- [ ] 按需接入 tracing；第三方模型与追踪服务分别配置认证，追踪异常不能影响业务。
- [ ] 完成关键接口与流程测试，覆盖鉴权、幂等、超时、部分失败和恢复。
- [ ] 建立固定研究问题集，评估资料相关性、引用支持程度和报告质量。
- [ ] 做适量并发测试和故障演练，检查恢复、取消及备份可用性。
- [ ] 完成架构说明、配置模板、接口示例、部署手册和演示流程。

**验收：** 关键流程测试通过；第三方服务异常时任务有明确结果；能凭 task ID 定位失败阶段；具备可重复的部署与恢复步骤。

**学习重点：** 限流、可观测性、故障恢复、性能验证和 Agent 质量评估。

## 首轮交付完成标准

- [ ] 用户能在 HTTPS 网页中登录、提交研究并获得任务 ID。
- [ ] 后台能完成规划、资料搜索和带来源的报告写作。
- [ ] 页面能显示执行进度，并查询历史任务与报告。
- [ ] 用户之间的数据隔离有效，配置和密钥不进入公开代码或日志。
- [ ] 服务重启、搜索失败或模型超时都有明确处理，任务不会无期限停留在执行中。
- [ ] 新环境能按文档启动项目，部署、备份和恢复流程可重复执行。

网页全文读取与更严格的来源语义核验属于后续增强项，可在 V2 主流程稳定后单独推进；当前报告必须持续说明仅使用搜索片段的限制。
