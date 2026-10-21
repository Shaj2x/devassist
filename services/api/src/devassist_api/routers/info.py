"""What this deployment can do, for the dashboard: whether a real model is
configured, and in demo mode which sample task the scripted model knows."""

from __future__ import annotations

from fastapi import APIRouter
from pydantic import BaseModel

from devassist_api.deps import SettingsDep

router = APIRouter(prefix="/v1", tags=["info"])

# Matches services/orchestrator/.../mock_scripts/datekit_fix_leap_year.json.
DEMO_REPO = "demo/datekit"
DEMO_CLONE_URL = "file:///sample-repos/datekit.git"
DEMO_TASK = "Fix the failing test in datekit/calendar.py"


class Demo(BaseModel):
    repo: str
    clone_url: str
    task: str


class Info(BaseModel):
    llm_provider: str
    demo_mode: bool
    # Present when the scripted model is in use and local repos can be added.
    demo: Demo | None


@router.get("/info", response_model=Info)
async def info(settings: SettingsDep) -> Info:
    demo_mode = settings.llm_provider == "mock"
    demo = (
        Demo(repo=DEMO_REPO, clone_url=DEMO_CLONE_URL, task=DEMO_TASK)
        if demo_mode and settings.local_repos_allowed
        else None
    )
    return Info(llm_provider=settings.llm_provider, demo_mode=demo_mode, demo=demo)
