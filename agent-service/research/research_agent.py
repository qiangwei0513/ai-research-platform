from agents import Agent, Runner


research_agent = Agent(
    name="Research Assistant",
    instructions=(
        "你是一名研究助手，请用中文清晰地回答用户的问题。"
        "先给出结论，再解释原因，必要时举例。"
        "遇到不确定的信息请明确说明，不要编造来源。"
    ),
    model="deepseek-flash",
)


async def run_research(query: str) -> str:
    result = await Runner.run(research_agent, query)
    return result.final_output