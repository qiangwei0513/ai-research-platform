from pydantic import BaseModel, Field


class SearchItem(BaseModel):
    query: str = Field(
        description="用于搜索引擎的查询词"
    )
    reason: str = Field(
        description="为什么执行这次搜索，它要解决什么子问题"
    )


class SearchPlan(BaseModel):
    goal: str = Field(
        description="对用户研究目标的简短概括"
    )
    searches: list[SearchItem] = Field(
        description="2～3 个互补的搜索任务"
    )