FROM ghcr.io/astral-sh/uv:0.13.0 AS uv

FROM python:3.12-slim-bookworm AS dependencies
COPY --from=uv /uv /usr/local/bin/uv
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
COPY agent-service/pyproject.toml agent-service/uv.lock ./
# 服务直接由 main.py 启动，uv 只安装锁定的运行依赖。
RUN uv sync --frozen --no-dev --no-install-project

FROM python:3.12-slim-bookworm
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
WORKDIR /app
RUN groupadd --gid 10001 app && useradd --uid 10001 --gid app --no-create-home app
COPY --from=dependencies /app/.venv /app/.venv
COPY agent-service/main.py ./main.py
COPY agent-service/research ./research
USER app
EXPOSE 8000
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
