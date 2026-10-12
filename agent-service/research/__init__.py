from pathlib import Path

from dotenv import load_dotenv


# __file__ 是当前 __init__.py 的路径。
# 向上两层找到 agent-service 目录。
SERVICE_DIR = Path(__file__).resolve().parent.parent

load_dotenv(
    dotenv_path=SERVICE_DIR / ".env",
    override=False,
)