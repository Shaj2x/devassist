"""The agent framework: deliberately small.

An Agent is data (name, system prompt, output model, effort) plus a function
that renders its user prompt from the job context. `run` makes the call,
validates the JSON against the output model, retries once with the
validation error if it does not fit, records every call as an agent step,
and enforces the job's cost budget before each call.
"""

from __future__ import annotations

import json
import logging
from collections.abc import Callable
from dataclasses import dataclass, field
from decimal import Decimal
from typing import Any, Generic, Protocol, TypeVar

from devassist_common.db import AgentName
from pydantic import BaseModel, ValidationError

from devassist_orchestrator.llm import LLMError, LLMProvider, LLMRequest, LLMResponse, Message
from devassist_orchestrator.llm.base import Effort
from devassist_orchestrator.llm.schema import strict_schema

log = logging.getLogger(__name__)

T = TypeVar("T", bound=BaseModel)


class BudgetExceeded(Exception):
    pass


class StepRecorder(Protocol):
    """Persists agent steps (implemented by the job store)."""

    async def start_step(self, agent: AgentName, iteration: int, request: LLMRequest) -> str: ...

    async def finish_step(
        self,
        step_id: str,
        *,
        response: LLMResponse | None,
        parsed: dict[str, Any] | None,
        error: str | None,
    ) -> None: ...

    async def spent_usd(self) -> Decimal: ...


@dataclass
class AgentContext:
    """Everything an agent may see. Agents read from it; the workflow fills it."""

    task: str
    repo_name: str
    file_tree: list[str]
    iteration: int = 1
    snippets: str = ""  # rendered retrieval results
    files: dict[str, str] = field(default_factory=dict)  # path -> current content
    plan: dict[str, Any] | None = None
    diff: str = ""
    validation: str = ""  # rendered validation report
    history: list[str] = field(default_factory=list)  # earlier attempts, newest last


@dataclass(frozen=True)
class Agent(Generic[T]):
    name: AgentName
    system: str
    output: type[T]
    render: Callable[[AgentContext], str]
    effort: Effort = "high"

    async def run(
        self, llm: LLMProvider, recorder: StepRecorder, ctx: AgentContext, *, budget_usd: Decimal
    ) -> T:
        messages = [Message("user", self.render(ctx))]
        for attempt in (1, 2):
            if await recorder.spent_usd() >= budget_usd:
                raise BudgetExceeded(
                    f"job budget of ${budget_usd} reached before the {self.name} step"
                )
            request = LLMRequest(
                agent=self.name.value,
                system=self.system,
                messages=messages,
                schema=strict_schema(self.output),
                schema_name=self.output.__name__,
                effort=self.effort,
            )
            step_id = await recorder.start_step(self.name, ctx.iteration, request)
            try:
                response = await llm.complete(request)
            except LLMError as e:
                await recorder.finish_step(step_id, response=None, parsed=None, error=str(e))
                raise
            try:
                result = self.output.model_validate(response.data)
            except ValidationError as e:
                await recorder.finish_step(
                    step_id, response=response, parsed=None, error=f"invalid output: {e}"
                )
                if attempt == 2:
                    raise LLMError(f"{self.name} returned invalid output twice: {e}") from e
                # Show the model its answer and the error, and ask once more.
                messages = [
                    *messages,
                    Message("assistant", response.text),
                    Message("user", f"That output failed validation:\n{e}\nReturn corrected JSON."),
                ]
                continue
            await recorder.finish_step(
                step_id, response=response, parsed=result.model_dump(), error=None
            )
            log.info(
                "agent step finished",
                extra={
                    "agent": self.name.value,
                    "iteration": ctx.iteration,
                    "cost_usd": str(response.cost_usd),
                },
            )
            return result
        raise AssertionError("unreachable")


def render_json(data: Any) -> str:
    return json.dumps(data, indent=2, sort_keys=True)
