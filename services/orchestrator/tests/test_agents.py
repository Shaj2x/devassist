from __future__ import annotations

from decimal import Decimal
from typing import Any

import pytest
from devassist_orchestrator.agents import PLANNER, AgentContext, BudgetExceeded
from devassist_orchestrator.agents.base import StepRecorder
from devassist_orchestrator.llm import LLMError
from devassist_orchestrator.llm.mock_provider import MockProvider, load_script

CTX = AgentContext(task="fix the bug", repo_name="o/r", file_tree=["a.py"])
GOOD_PLAN = {
    "summary": "s",
    "files_to_change": [{"path": "a.py", "change": "c"}],
    "steps": ["x"],
    "risks": [],
    "test_strategy": "t",
    "search_queries": ["q"],
}


async def test_agent_records_step_and_returns_validated_output(store: Any) -> None:
    plan = await PLANNER.run(
        MockProvider({"planner": [GOOD_PLAN]}), store, CTX, budget_usd=Decimal(1)
    )
    assert plan.files_to_change[0].path == "a.py"
    assert store.agents() == ["planner"] and store.steps[0]["error"] is None
    request = store.steps[0]["request"]
    assert "fix the bug" in request.messages[0].content and "a.py" in request.messages[0].content


async def test_invalid_output_gets_one_repair_attempt(store: Any) -> None:
    llm = MockProvider({"planner": [{"summary": "missing everything"}, GOOD_PLAN]})
    await PLANNER.run(llm, store, CTX, budget_usd=Decimal(1))
    assert len(store.steps) == 2 and "invalid output" in store.steps[0]["error"]
    retry = store.steps[1]["request"].messages
    assert retry[-1].role == "user" and "failed validation" in retry[-1].content


async def test_two_invalid_outputs_fail(store: StepRecorder) -> None:
    llm = MockProvider({"planner": [{"bad": 1}, {"bad": 2}]})
    with pytest.raises(LLMError, match="invalid output twice"):
        await PLANNER.run(llm, store, CTX, budget_usd=Decimal(1))


async def test_budget_is_checked_before_calling(store: Any) -> None:
    store.cost = Decimal("5")
    llm = MockProvider({"planner": [GOOD_PLAN]})
    with pytest.raises(BudgetExceeded):
        await PLANNER.run(llm, store, CTX, budget_usd=Decimal("5"))
    assert llm.calls["planner"] == 0


def test_bundled_mock_script_loads() -> None:
    script = load_script("datekit_fix_leap_year")
    assert set(script) == {"planner", "coder", "tester", "debugger", "reviewer"}
