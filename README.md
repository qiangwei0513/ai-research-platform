# AI Research Platform

使用 Go + Gin 和 Python + OpenAI Agents SDK 构建的研究助手项目。

## 当前进度

已完成 V0：

- Go 提供 POST /api/research 接口。
- Go 通过 HTTP 调用 Python Agent 服务。
- Python 执行 Agent，并返回模型回答。
- 支持问题校验、请求超时和服务异常处理。

已增加本地笔记搜索工具：

- Agent 可以调用 search_notes 查询项目笔记。
- 回答可引用实际检索到的笔记编号。
- 开发终端会打印工具执行日志。
- 本地笔记使用关键词匹配。

已增加联网搜索工具：

- Agent 可以调用 `search_web`，通过 Tavily Search API 搜索公开网页。
- 返回网页标题、来源 URL 和内容片段，供 Agent 组织回答并附上来源链接。
- 每次搜索最多返回 3 条结果，每条内容片段最多保留 1500 个字符。
- 开发终端通过 `[tool]` 显示工具执行情况，通过 `[source]` 显示来源链接。

## 请求流程

```text
客户端
  → Go Gin（8080）
  → Python FastAPI（8000）
  → Agent / 模型 → 按需调用本地笔记搜索或联网搜索工具
  → 返回回答
```

## 项目结构

```text
ai-research-platform/
├── backend/
│   ├── main.go
│   ├── go.mod
│   └── go.sum
├── agent-service/
│   ├── .env                  # 本地配置，不提交到 Git
│   ├── main.py
│   ├── research/
│   │   ├── __init__.py
│   │   ├── research_agent.py
│   │   ├── tools.py          # 本地笔记搜索
│   │   └── web_search.py     # Tavily 联网搜索
│   ├── pyproject.toml
│   └── uv.lock
├── .gitignore
└── README.md
```

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

修改 Go 代码后，需要重新启动 Go 服务。

## 发起研究请求

```bash
curl -i --max-time 140 \
  -X POST http://127.0.0.1:8080/api/research \
  -H 'Content-Type: application/json' \
  -d '{"query":"请解释 Redis 缓存穿透，并举一个例子。"}'
```

成功响应示例：

```json
{
  "result": "模型生成的回答"
}
```

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

## 联网搜索的当前限制

- 当前使用 Tavily 的 `basic` 搜索，只读取返回的内容片段，不主动打开来源网页或抓取全文。
- 每条片段还会截取前 1500 个字符，因此可能缺少完整上下文；有来源链接不代表已阅读或核验全文。
- Agent 根据片段生成回答，引用准确性仍需核对；当前没有自动验证结论与来源是否一致。
- 搜索结果可能为空，也不保证覆盖所有相关资料或最新信息。
- 单次搜索操作设置了 25 秒期限，仍受整个 Agent 执行的 120 秒超时限制。
- 搜索工具异常会向外抛出，当前由 Python 接口返回 500，再由 Go 转为 502；具体原因查看 Python 日志。

## Go 接口状态码

| 状态码 | 含义                                       |
| ------ | ------------------------------------------ |
| 200    | 成功返回答案                               |
| 400    | 请求格式错误或问题不符合要求               |
| 500    | Go 内部构造请求失败                        |
| 502    | Agent 服务连接失败、执行失败或返回无效结果 |
| 504    | Agent 执行或通信超时                       |

问题去除首尾空白后，长度应为 1～5000 个字符。

Python Agent 执行超时为 120 秒，Go HTTP 客户端超时为 130 秒。

## 后续计划

- 增加网页全文读取和来源核验。
- 增加 Planner / Search / Writer 工作流。
- 增加任务与报告持久化。
- 增加异步任务和执行进度展示。
