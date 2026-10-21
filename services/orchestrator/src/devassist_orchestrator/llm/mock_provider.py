"""A scripted provider for demos and tests: no API key, no network.

A script is a JSON file mapping each agent name to the list of responses it
gives, in order:

    {"planner": [{...plan...}], "coder": [{...}], "debugger": [{...}, {...}]}

Each call pops that agent's next response. Token counts are estimated from
text length so the dashboard's accounting has realistic shapes.

A script may describe the one job it solves under "_demo" (regex patterns
for the repository and the task). Jobs that do not match are refused up
front: a scripted answer to a different task would be nonsense, and on a
real repository approving it would open a pull request of sample code.
"""

from __future__ import annotations

import json
import re
from collections import defaultdict
from pathlib import Path
from typing import Any

from devassist_orchestrator.llm.base import LLMError, LLMRequest, LLMResponse

SCRIPTS_DIR = Path(__file__).resolve().parent.parent / "mock_scripts"


def load_script(name_or_path: str) -> dict[str, list[dict[str, Any]]]:
    path = Path(name_or_path)
    if not path.exists():
        path = SCRIPTS_DIR / f"{name_or_path}.json"
    data: dict[str, list[dict[str, Any]]] = json.loads(path.read_text())
    return data


class MockProvider:
    name = "mock"
    model = "mock"

    def __init__(self, script: dict[str, list[dict[str, Any]]]) -> None:
        self.demo: dict[str, str] = dict(script.get("_demo", {}))  # type: ignore[arg-type]
        self.script = {
            agent: list(responses) for agent, responses in script.items() if agent != "_demo"
        }
        self.calls: dict[str, int] = defaultdict(int)
        self.requests: list[LLMRequest] = []

    def refusal(self, repo: str, clone_url: str, task: str) -> str | None:
        """Why this script cannot run the job, or None if it can."""
        if not self.demo:
            return None
        repo_ok = re.search(self.demo.get("repo_pattern", ""), f"{repo} {clone_url}", re.I)
        task_ok = re.search(self.demo.get("task_pattern", ""), task, re.I)
        if repo_ok and task_ok:
            return None
        return (
            "Demo mode: no AI model is configured, so the scripted demo model only knows one "
            f'task: "{self.demo.get("task", "?")}" on {self.demo.get("repo", "the sample repo")}. '
            "To run your own tasks, set LLM_PROVIDER=anthropic and ANTHROPIC_API_KEY "
            "(or openai and OPENAI_API_KEY) in .env and restart."
        )

    async def complete(self, request: LLMRequest) -> LLMResponse:
        self.requests.append(request)
        queue = self.script.get(request.agent, [])
        if not queue:
            raise LLMError(f"mock script has no response left for agent {request.agent!r}")
        data = queue.pop(0)
        self.calls[request.agent] += 1
        text = json.dumps(data)
        prompt_chars = len(request.system) + sum(len(m.content) for m in request.messages)
        return LLMResponse(
            text=text,
            data=data,
            provider=self.name,
            model=self.model,
            input_tokens=prompt_chars // 4,
            output_tokens=len(text) // 4,
            latency_ms=5,
            stop_reason="end_turn",
        )

    async def aclose(self) -> None:
        return None
