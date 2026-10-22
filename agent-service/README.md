# Python Agent 阅读索引

启动与模型配置见[项目 README](../README.md#本地启动完整功能)。程序从 `main.py` 进入，普通问答与完整研究复用底层工具，但执行流程不同。

## 主流程

```text
POST /research
  → RequestTelemetry：关联 ID、执行槽、请求级用量
  → execute_research
      ├── chat：run_research → Research Assistant → 按需使用工具
      └── research：run_research_workflow
                      → Planner
                      → 并行 Search Agent + Tavily
                      → Writer → 结构校验与引用链接
  → JSON 结果，或面向 Worker 的 NDJSON 进度与结果流
```

| 文件 | 职责与阅读重点 |
| --- | --- |
| [main.py](main.py) | FastAPI 数据校验、模式分发、错误映射、断连取消 |
| [research/research_agent.py](research/research_agent.py) | 普通问答 Agent 与工具注册 |
| [research/tools.py](research/tools.py) | 本地笔记与联网工具，区分模型生成和真实工具结果 |
| [research/schemas.py](research/schemas.py) | Planner、搜索资料、报告、阶段记录的数据结构 |
| [research/workflow.py](research/workflow.py) | 编排规划/搜索/写作，收集成功与失败阶段 |
| [research/planner_agent.py](research/planner_agent.py) | 搜索目标、2～3 项搜索计划与校验 |
| [research/searcher.py](research/searcher.py) | 并行调度、部分失败处理、来源去重 |
| [research/search_agent.py](research/search_agent.py) | 真实工具调用、保存来源与搜索摘要 |
| [research/web_search.py](research/web_search.py) | Tavily HTTP、次数/频率/并发限制、有界重试、响应大小限制 |
| [research/writer_agent.py](research/writer_agent.py) | `prepare_sources → write_report → render_report`，来源编号与真实 URL 绑定 |
| [research/cache.py](research/cache.py) | Redis 搜索缓存、TTL、fresh 绕过、缓存故障回源 |
| [research/timeouts.py](research/timeouts.py) | 阶段和整体时间预算 |
| [research/progress.py](research/progress.py) | ContextVar 隔离每次执行的进度回调 |
| [research/streaming.py](research/streaming.py) | NDJSON、有界缓冲队列、心跳和断连回收 |
| [research/telemetry.py](research/telemetry.py) | 模型配置、用量汇总、JSON 日志、请求执行槽与可选 tracing |
| [research/__init__.py](research/__init__.py) | 按文件位置加载 `.env` |

## 配置与边界

依赖由 `pyproject.toml` 和 `uv.lock` 管理，使用 `uv sync --locked` 安装。真实 `.env` 不提交 Git。示例默认模型名为 `deepseek-flash`，可通过 `RESEARCH_MODEL` 改成服务商实际支持的模型。

普通问答总预算 120 秒，完整研究 300 秒；Go 分别等待 130 / 310 秒。搜索缓存默认 900 秒，`fresh=true` 可绕过。每次 Python 执行的搜索调用预算包含网络重试。

用量缺失使用 `null`，取消和超时可能拿不到完整用量；不能把应用观测数据当作供应商账单。Tracing 默认关闭，开启时需要独立追踪密钥；不需要为了学习工作流先配置追踪服务。

深入研究检查报告结构、来源编号和 URL，但没有读取网页全文，也没有自动证明每个结论都被来源支持。搜索部分失败会保留资料缺口，全部失败不会编造成功报告。

学习版只保留实际请求会经过的模块；独立 demos、自动测试和批量评估代码已移除。你已熟悉 Agent 编排，可以优先关注 Go/Python 之间的错误、流式进度、取消和结果协议。
