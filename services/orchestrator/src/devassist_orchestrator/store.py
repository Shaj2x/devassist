"""Postgres persistence for a job run, plus live progress in Redis.

Every agent call becomes an `agent_steps` row (prompt, response, parsed
output, tokens, cost, latency) and job totals are updated in the same
transaction. After each change a small progress document is written to
Redis (`devassist:job:<id>:progress`) and published on
`devassist:job:<id>:events`, which the API turns into the dashboard's live
timeline without polling Postgres.
"""

from __future__ import annotations

import json
import logging
import uuid
from datetime import UTC, datetime
from decimal import Decimal
from typing import Any

from devassist_common.db import (
    AgentName,
    AgentStep,
    AgentStepStatus,
    CheckStatus,
    Job,
    JobStatus,
    Patch,
    PatchStatus,
    ValidationRun,
    ValidationStatus,
)
from devassist_common.events import ValidationCompleted
from redis.asyncio import Redis
from sqlalchemy import func, select, update
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

from devassist_orchestrator.llm import LLMRequest, LLMResponse
from devassist_orchestrator.tools.workspace import DiffStats

log = logging.getLogger(__name__)

TERMINAL = {JobStatus.AWAITING_REVIEW, JobStatus.FAILED, JobStatus.CANCELLED}
PROGRESS_TTL_SECONDS = 24 * 3600


def progress_key(job_id: str) -> str:
    return f"devassist:job:{job_id}:progress"


def events_channel(job_id: str) -> str:
    return f"devassist:job:{job_id}:events"


def _clean(value: Any) -> Any:
    """Postgres text and JSONB reject NUL characters; drop them."""
    if isinstance(value, str):
        return value.replace("\x00", "")
    if isinstance(value, dict):
        return {k: _clean(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_clean(v) for v in value]
    return value


class SqlJobStore:
    def __init__(
        self,
        sessions: async_sessionmaker[AsyncSession],
        redis: Redis | None,
        job_id: str,
        *,
        provider: str,
        model: str,
    ) -> None:
        self.sessions = sessions
        self.redis = redis
        self.job_id = uuid.UUID(job_id)
        self.provider, self.model = provider, model
        self._sequence: int | None = None
        self._progress: dict[str, Any] = {"job_id": job_id}

    # --- StepRecorder -------------------------------------------------------------

    async def start_step(self, agent: AgentName, iteration: int, request: LLMRequest) -> str:
        async with self.sessions() as s:
            if self._sequence is None:
                current = await s.scalar(
                    select(func.max(AgentStep.sequence)).where(AgentStep.job_id == self.job_id)
                )
                self._sequence = current or 0
            self._sequence += 1
            step = AgentStep(
                job_id=self.job_id,
                iteration=iteration,
                sequence=self._sequence,
                agent=agent,
                status=AgentStepStatus.RUNNING,
                provider=self.provider,
                model=self.model,
                prompt=_clean(
                    {
                        "system": request.system,
                        "effort": request.effort,
                        "schema": request.schema_name,
                        "messages": [
                            {"role": m.role, "content": m.content} for m in request.messages
                        ],
                    }
                ),
            )
            s.add(step)
            await s.commit()
            step_id = str(step.id)
        await self._publish(current_agent=agent.value, current_step=step_id, iteration=iteration)
        return step_id

    async def finish_step(
        self,
        step_id: str,
        *,
        response: LLMResponse | None,
        parsed: dict[str, Any] | None,
        error: str | None,
    ) -> None:
        values: dict[str, Any] = {
            "status": AgentStepStatus.FAILED if error else AgentStepStatus.SUCCEEDED,
            "error": error,
            "parsed_output": parsed,
            "finished_at": datetime.now(UTC),
        }
        if response is not None:
            values.update(
                response=response.text,
                model=response.model,
                latency_ms=response.latency_ms,
                input_tokens=response.input_tokens
                + response.cache_read_tokens
                + response.cache_write_tokens,
                output_tokens=response.output_tokens,
                cost_usd=response.cost_usd,
            )
        async with self.sessions() as s:
            await s.execute(
                update(AgentStep).where(AgentStep.id == uuid.UUID(step_id)).values(**_clean(values))
            )
            if response is not None:
                # Increment in SQL so concurrent writers cannot lose updates.
                await s.execute(
                    update(Job)
                    .where(Job.id == self.job_id)
                    .values(
                        total_input_tokens=Job.total_input_tokens + values["input_tokens"],
                        total_output_tokens=Job.total_output_tokens + response.output_tokens,
                        total_cost_usd=Job.total_cost_usd + response.cost_usd,
                    )
                )
            await s.commit()
        await self._publish(
            current_step=None, last_step=step_id, last_step_status=values["status"].value
        )

    async def spent_usd(self) -> Decimal:
        async with self.sessions() as s:
            value = await s.scalar(select(Job.total_cost_usd).where(Job.id == self.job_id))
        return Decimal(value or 0)

    # --- JobStore -----------------------------------------------------------------

    async def set_status(
        self, status: JobStatus, *, iteration: int | None = None, error: str | None = None
    ) -> None:
        values: dict[str, Any] = {"status": status, "updated_at": datetime.now(UTC)}
        if iteration is not None:
            values["current_iteration"] = iteration
        if error is not None:
            values["error"] = error
        if status in TERMINAL:
            values["completed_at"] = datetime.now(UTC)
        async with self.sessions() as s:
            await s.execute(update(Job).where(Job.id == self.job_id).values(**values))
            await s.commit()
        extra = {"iteration": iteration} if iteration is not None else {}
        await self._publish(status=status.value, error=error, **extra)

    async def save_plan(self, plan: dict[str, Any]) -> None:
        async with self.sessions() as s:
            await s.execute(update(Job).where(Job.id == self.job_id).values(plan=plan))
            await s.commit()

    async def save_patch(self, iteration: int, diff: str, stats: DiffStats) -> str:
        async with self.sessions() as s:
            # Earlier patches that did not pass are superseded by this one.
            await s.execute(
                update(Patch)
                .where(Patch.job_id == self.job_id, Patch.status != PatchStatus.PASSED)
                .values(status=PatchStatus.SUPERSEDED)
            )
            patch = Patch(
                job_id=self.job_id,
                iteration=iteration,
                diff=diff,
                status=PatchStatus.VALIDATING,
                files_changed=stats.files_changed,
                additions=stats.additions,
                deletions=stats.deletions,
            )
            s.add(patch)
            await s.commit()
            patch_id = str(patch.id)
        await self._publish(patch_id=patch_id, iteration=iteration)
        return patch_id

    async def record_validation(self, patch_id: str, result: ValidationCompleted) -> None:
        async with self.sessions() as s:
            s.add(
                ValidationRun(
                    patch_id=uuid.UUID(patch_id),
                    status=ValidationStatus(result.status),
                    tests_status=CheckStatus(result.tests.status),
                    security_status=CheckStatus(result.security.status),
                    static_status=CheckStatus(result.static_analysis.status),
                    tests=result.tests.model_dump(mode="json"),
                    security=result.security.model_dump(mode="json"),
                    static_analysis=result.static_analysis.model_dump(mode="json"),
                    error=result.error,
                    duration_ms=result.duration_ms,
                    finished_at=datetime.now(UTC),
                )
            )
            patch_status = PatchStatus.PASSED if result.status == "passed" else PatchStatus.FAILED
            await s.execute(
                update(Patch).where(Patch.id == uuid.UUID(patch_id)).values(status=patch_status)
            )
            await s.commit()
        await self._publish(validation=result.status)

    async def save_review(self, review: dict[str, Any]) -> None:
        async with self.sessions() as s:
            await s.execute(
                update(Job)
                .where(Job.id == self.job_id)
                .values(review=review, reviewer_summary=review.get("summary"))
            )
            await s.commit()

    # --- live progress ------------------------------------------------------------

    async def _publish(self, **changes: Any) -> None:
        if self.redis is None:
            return
        self._progress.update(changes, updated_at=datetime.now(UTC).isoformat())
        payload = json.dumps(self._progress, default=str)
        try:
            await self.redis.set(progress_key(str(self.job_id)), payload, ex=PROGRESS_TTL_SECONDS)
            await self.redis.publish(events_channel(str(self.job_id)), payload)
        except Exception as e:  # progress is best-effort; Postgres is the record
            log.warning("could not publish job progress", extra={"error": str(e)})
