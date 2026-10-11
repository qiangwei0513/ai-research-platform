import asyncio
import logging

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, ConfigDict, Field

from research.research_agent import run_research


app = FastAPI(title="Research Agent Service")
logger = logging.getLogger(__name__)


# 规定客户端发来的 JSON 应该是什么格式
class ResearchRequest(BaseModel):
    model_config = ConfigDict(str_strip_whitespace=True)

    query: str = Field(min_length=1, max_length=5000)


# 规定我们返回的 JSON 是什么格式
class ResearchResponse(BaseModel):
    result: str


# 用来检查 HTTP 服务是否启动
@app.get("/health")
async def health():
    return {"status": "ok"}


# 接收问题，调用 Agent，返回答案
@app.post("/research", response_model=ResearchResponse)
async def research(request: ResearchRequest):
    try:
        answer = await asyncio.wait_for(
            run_research(request.query),
            timeout=120,
        )
    except TimeoutError:
        raise HTTPException(
            status_code=504,
            detail="研究请求超时，请稍后重试",
        )
    except Exception:
        logger.exception("研究请求执行失败")
        raise HTTPException(
            status_code=500,
            detail="研究请求执行失败，请查看服务端日志",
        )

    return ResearchResponse(result=answer)