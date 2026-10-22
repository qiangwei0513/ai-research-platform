import json

from agents import function_tool


# 第一版直接把笔记放在代码中，便于观察工具调用过程。
NOTES = [
    {
        "id": "note-001",
        "title": "项目架构",
        "content": (
            "Go 使用 Gin，在本机 8080 端口提供 POST /api/research。"
            "Python 使用 FastAPI，在本机 8000 端口提供 POST /research。"
            "Go 通过 HTTP 调用 Python，Python 使用 Agents SDK 调用模型。"
            "mode 默认为 chat，使用单个问答 Agent；mode=research 时运行 Planner、Search、Writer 研究工作流。"
        ),
    },
    {
        "id": "note-002",
        "title": "请求超时",
        "content": (
            "普通问答的 Python 超时为 120 秒，Go 超时为 130 秒。"
            "完整研究的 Python 总超时为 300 秒，Go 超时为 310 秒。"
            "Go 比 Python 多等待 10 秒，为通信和错误返回留出时间。"
        ),
    },
    {
        "id": "note-003",
        "title": "输入校验",
        "content": (
            "研究问题使用 JSON 中的 query 字段传入。"
            "去除首尾空白后，长度必须为 1～5000 个字符。"
            "Go 对无效输入返回 HTTP 400，Python 对校验失败返回 HTTP 422。"
        ),
    },
]


@function_tool
def search_notes(keyword: str) -> str:
    """按关键词搜索本项目的本地开发笔记，不进行互联网搜索。

    Args:
        keyword: 简短关键词，例如“架构”“超时”“校验”，不要传入整个问题。
    """
    keyword = keyword.strip().casefold()

    # 开发阶段打印日志，确认工具是否真的执行。
    # Do not log user-supplied search terms.

    matches = []

    if keyword:
        for note in NOTES:
            text = f"{note['title']} {note['content']}".casefold()
            if keyword in text:
                matches.append(note)

    print(f"[tool] search_notes 执行完成，找到 {len(matches)} 条笔记", flush=True)

    return json.dumps(
        {
            "keyword": keyword,
            "count": len(matches),
            "notes": matches,
        },
        ensure_ascii=False,
    )
