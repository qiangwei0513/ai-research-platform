
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
- 当前使用关键词匹配，尚未接入互联网搜索。

## 请求流程

客户端
  → Go Gin（8080）
  → Python FastAPI（8000）
  → Agent / 模型
  → 返回回答

## 项目结构

```text
ai-research-platform/
├── backend/
│   ├── main.go
│   ├── go.mod
│   └── go.sum
├── agent-service/
│   ├── main.py
│   ├── research/
│   │   ├── __init__.py
│   │   └── research_agent.py
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

启动 Python 服务前，需要在同一终端配置已经验证可用的
模型服务环境变量。不要在仓库中保存真实 API Key。

当前代码没有主动加载 .env 文件，仅创建 .env 文件不会自动生效。

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

- 增加工具调用。
- 接入搜索能力。
- 增加 Planner / Search / Writer 工作流。
- 增加任务与报告持久化。
- 增加异步任务和执行进度展示。
