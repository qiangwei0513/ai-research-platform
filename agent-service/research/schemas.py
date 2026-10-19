from pydantic import BaseModel, Field
from typing import Literal
from datetime import datetime

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
    executed_queries: list[str] = Field(default_factory=list)
    summary: str | None = None
    error: str | None = None
    cache_status: str | None = None
    fetched_at: str | None = None


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

class ReportSection(BaseModel):
    text: str = Field(
        description="本节正文，使用中文纯文本，不包含链接或引用编号"
    )
    source_ids: list[int] = Field(
        description="支持本节内容的来源编号；无依据时用空列表"
    )


class ReportDraft(BaseModel):
    conclusion: ReportSection
    analysis: ReportSection
    applicability: ReportSection
    limitations: ReportSection


class AgentRun(BaseModel):
    stage: str
    status: Literal["running", "succeeded", "failed", "cancelled"]
    started_at: datetime
    finished_at: datetime
    error_code: str = ""
