from pydantic import BaseModel, Field
from typing import Literal

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

class SearchTaskResult(BaseModel):
    query: str
    reason: str
    status: Literal["success", "empty", "failed"]
    source_urls: list[str] = Field(default_factory=list)
    error: str | None = None


class SearchSource(BaseModel):
    title: str
    url: str
    snippets: list[str]
    queries: list[str]


class SearchBundle(BaseModel):
    goal: str
    status: Literal["completed", "partial", "empty", "failed"]
    tasks: list[SearchTaskResult]
    sources: list[SearchSource]