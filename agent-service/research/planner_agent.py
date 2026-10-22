import asyncio

from agents import Agent, Runner

from research.schemas import SearchPlan
from research.timeouts import PLANNER_TIMEOUT
from research.telemetry import run_agent


planner_agent = Agent(
    name="Research Planner",
    instructions=(
        "你负责把用户的研究问题转换成网页搜索计划。\n"
        "请遵守以下要求：\n"
        "1. 用中文简要概括研究目标，填写 goal。\n"
        "2. 生成 2～3 个互补的搜索任务。\n"
        "3. 每个任务包含 query 和 reason。\n"
        "4. query 应适合搜索引擎，可根据资料特点使用中文或英文。\n"
        "5. reason 用中文说明这次搜索要解决的子问题。\n"
        "6. 技术问题优先寻找官方文档或一手资料。\n"
        "7. 避免只是换一种措辞重复搜索同一个问题。\n"
        "8. 你当前只负责规划，不执行搜索，也不编造搜索结果。\n"
    ),
    model="deepseek-flash",
    output_type=SearchPlan,
)


async def create_plan(query: str) -> SearchPlan:
    query = query.strip()

    if not query:
        raise ValueError("研究问题不能为空")

    print("[planner] 开始生成搜索计划", flush=True)

    async with asyncio.timeout(PLANNER_TIMEOUT):
        result = await run_agent(
            planner_agent,
            query,
            max_turns=3,
        )

    # 确认 SDK 返回的是 SearchPlan 对象。
    plan = result.final_output_as(
        SearchPlan,
        raise_if_incorrect_type=True,
    )

    # 检查业务规则：格式正确，还需要内容符合要求。
    if not plan.goal.strip():
        raise ValueError("Planner 返回了空的研究目标")

    if not 2 <= len(plan.searches) <= 3:
        raise ValueError("搜索计划必须包含 2～3 个任务")

    for item in plan.searches:
        if not item.query.strip() or not item.reason.strip():
            raise ValueError("搜索词和搜索原因不能为空")

    queries = [
        item.query.strip().casefold()
        for item in plan.searches
    ]

    if len(set(queries)) != len(queries):
        raise ValueError("搜索计划包含重复的搜索词")

    print(
        f"[planner] 规划完成，共 {len(plan.searches)} 个搜索任务",
        flush=True,
    )

    return plan
