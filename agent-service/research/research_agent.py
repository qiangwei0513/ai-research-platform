from agents import Agent, Runner

from research.tools import search_notes


research_agent = Agent(
    name="Research Assistant",
    instructions=(
        "你是一名研究助手，请用中文清晰地回答用户的问题。"
        "涉及本项目的架构、配置、开发笔记时，必须先调用 search_notes 查询。"
        "搜索时提取简短关键词，例如“架构”“超时”“校验”。"
        "引用笔记内容时，注明工具实际返回的笔记编号，例如 [note-002]。"
        "如果没有找到相关笔记，明确说明未找到，不要编造项目配置或笔记编号。"
        "如果用户要求只根据笔记回答，就不要用一般知识补充缺失信息。"
        "其他一般性问题可以直接回答。"
        "本工具只能搜索本地笔记，不要声称进行了互联网搜索。"
    ),
    model="deepseek-flash",
    tools=[search_notes],
)


async def run_research(query: str) -> str:
    result = await Runner.run(
        research_agent,
        query,
        max_turns=6,
    )
    return result.final_output