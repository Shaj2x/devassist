"""Jobs: create, inspect, follow live, and the human review decision.

POST /v1/jobs                      queue a task (publishes job.created)
GET  /v1/jobs                      list, newest first
GET  /v1/jobs/{id}                 plan, steps, patches + validations, review, PR
GET  /v1/jobs/{id}/steps/{step}    one agent call with full prompt/response
GET  /v1/jobs/{id}/events          server-sent events while the job runs
POST /v1/jobs/{id}/approve         open the pull request
POST /v1/jobs/{id}/reject
POST /v1/jobs/{id}/request-changes re-run the agents with reviewer feedback
"""

from __future__ import annotations

import json
import logging
import time
import uuid
from collections.abc import AsyncGenerator, AsyncIterator
from datetime import UTC, datetime
from typing import Annotated

from devassist_common.db import (
    AgentStep,
    Job,
    JobStatus,
    Patch,
    PatchStatus,
    PullRequest,
    Repository,
    RepoSnapshot,
    SnapshotStatus,
)
from devassist_common.events import EventType, JobCreated
from devassist_common.progress import events_channel, progress_key
from fastapi import APIRouter, HTTPException, Query, Request, status
from fastapi.responses import StreamingResponse
from redis.asyncio import Redis
from redis.asyncio.client import PubSub
from sqlalchemy import select, update
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import defer, selectinload

from devassist_api.config import Settings
from devassist_api.deps import (
    CurrentUser,
    DbDep,
    GitHubDep,
    PublisherDep,
    RedisDep,
    SettingsDep,
    UserDep,
)
from devassist_api.github import GitHubError
from devassist_api.pr import pull_request_body, pull_request_title
from devassist_api.routers.repos import get_owned_repo, is_github_repo
from devassist_api.schemas import (
    JobCreate,
    JobDetail,
    JobSummary,
    PatchOut,
    PullRequestOut,
    Reject,
    RequestChanges,
    StepDetail,
    StepSummary,
    ValidationOut,
)

log = logging.getLogger(__name__)
router = APIRouter(prefix="/v1/jobs", tags=["jobs"])

# Statuses after which nothing more happens without a human action.
SETTLED = {
    JobStatus.AWAITING_REVIEW,
    JobStatus.APPROVED,
    JobStatus.REJECTED,
    JobStatus.PR_OPENED,
    JobStatus.FAILED,
    JobStatus.CANCELLED,
}
KEEPALIVE_SECONDS = 15.0


# --- helpers ------------------------------------------------------------------


async def get_owned_job(db: AsyncSession, user: CurrentUser, job_id: uuid.UUID) -> Job:
    job = await db.scalar(
        select(Job)
        .join(Repository, Repository.id == Job.repo_id)
        .where(Job.id == job_id, Repository.owner_id == user.id)
        .options(selectinload(Job.repository), selectinload(Job.pull_request))
    )
    if job is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "job not found")
    return job


def summary(job: Job) -> JobSummary:
    return JobSummary(
        id=job.id,
        repo_id=job.repo_id,
        repo_name=job.repository.full_name,
        task=job.task,
        status=job.status.value,
        current_iteration=job.current_iteration,
        max_iterations=job.max_iterations,
        total_cost_usd=job.total_cost_usd,
        created_at=job.created_at,
        completed_at=job.completed_at,
        pr_url=job.pull_request.url if job.pull_request else None,
    )


async def job_detail(db: AsyncSession, redis: Redis, job: Job, settings: Settings) -> JobDetail:
    steps = await db.scalars(
        select(AgentStep)
        .where(AgentStep.job_id == job.id)
        .order_by(AgentStep.sequence)
        .options(defer(AgentStep.prompt), defer(AgentStep.response), defer(AgentStep.parsed_output))
    )
    patches = await db.scalars(
        select(Patch)
        .where(Patch.job_id == job.id)
        .order_by(Patch.iteration)
        .options(selectinload(Patch.validation_runs))
    )
    progress = None
    if job.status not in SETTLED:
        raw = await redis.get(progress_key(str(job.id)))
        progress = json.loads(raw) if raw else None
    return JobDetail(
        **summary(job).model_dump(),
        repo_is_github=is_github_repo(job.repository, settings),
        base_commit_sha=job.base_commit_sha,
        plan=job.plan,
        review=job.review,
        review_feedback=job.review_feedback,
        error=job.error,
        total_input_tokens=job.total_input_tokens,
        total_output_tokens=job.total_output_tokens,
        steps=[StepSummary.model_validate(s) for s in steps],
        patches=[
            PatchOut(
                **PatchOut.model_validate(p).model_dump(exclude={"validations"}),
                validations=[
                    ValidationOut.model_validate(v)
                    for v in sorted(p.validation_runs, key=lambda v: v.created_at)
                ],
            )
            for p in patches
        ],
        pull_request=PullRequestOut.model_validate(job.pull_request) if job.pull_request else None,
        progress=progress,
    )


async def final_patch(db: AsyncSession, job: Job) -> Patch:
    patch = await db.scalar(
        select(Patch)
        .where(Patch.job_id == job.id, Patch.status == PatchStatus.PASSED)
        .order_by(Patch.iteration.desc())
        .limit(1)
        .options(selectinload(Patch.validation_runs))
    )
    if patch is None:
        raise HTTPException(status.HTTP_409_CONFLICT, "job has no validated patch")
    return patch


async def transition(
    db: AsyncSession, job: Job, allowed: set[JobStatus], new: JobStatus, **values: object
) -> None:
    """Compare-and-set the job status so two reviewers cannot both act."""
    current = job.status
    result = await db.execute(
        update(Job)
        .where(Job.id == job.id, Job.status.in_(allowed))
        .values(status=new, updated_at=datetime.now(UTC), **values)
        .returning(Job.id)
    )
    if result.scalar() is None:
        await db.rollback()
        raise HTTPException(
            status.HTTP_409_CONFLICT, f"job is {current.value}; cannot move to {new.value}"
        )


async def reload(
    db: AsyncSession, redis: Redis, user: CurrentUser, job_id: uuid.UUID, settings: Settings
) -> JobDetail:
    db.expire_all()
    return await job_detail(db, redis, await get_owned_job(db, user, job_id), settings)


# --- create / read ------------------------------------------------------------


@router.post("", response_model=JobSummary, status_code=status.HTTP_202_ACCEPTED)
async def create_job(
    body: JobCreate, db: DbDep, user: UserDep, publisher: PublisherDep, settings: SettingsDep
) -> JobSummary:
    repo = await get_owned_repo(db, user, body.repo_id)
    snapshot = await db.scalar(
        select(RepoSnapshot)
        .where(RepoSnapshot.repo_id == repo.id, RepoSnapshot.status == SnapshotStatus.READY)
        .order_by(RepoSnapshot.created_at.desc())
        .limit(1)
    )
    if snapshot is None:
        raise HTTPException(
            status.HTTP_409_CONFLICT, "repository is not indexed yet; try again when it is ready"
        )
    job = Job(
        repo_id=repo.id,
        snapshot_id=snapshot.id,
        created_by=user.id,
        task=body.task.strip(),
        status=JobStatus.QUEUED,
        # Pin the commit now so retrieval, validation and the PR all agree.
        base_commit_sha=body.base_commit_sha or snapshot.commit_sha,
        max_iterations=body.max_iterations or settings.default_max_iterations,
    )
    db.add(job)
    await db.commit()
    await publish_job(publisher, job)
    log.info("job created", extra={"job_id": str(job.id), "repo": repo.full_name})
    job = await get_owned_job(db, user, job.id)
    return summary(job)


async def publish_job(publisher: PublisherDep, job: Job) -> None:
    await publisher.publish(
        EventType.JOB_CREATED,
        JobCreated(
            job_id=job.id,
            repo_id=job.repo_id,
            task=job.task,
            base_commit_sha=job.base_commit_sha,
            max_iterations=job.max_iterations,
        ),
        trace_id=str(job.id),
    )


@router.get("", response_model=list[JobSummary])
async def list_jobs(
    db: DbDep,
    user: UserDep,
    repo_id: uuid.UUID | None = None,
    job_status: Annotated[JobStatus | None, Query(alias="status")] = None,
    limit: Annotated[int, Query(ge=1, le=200)] = 50,
    offset: Annotated[int, Query(ge=0)] = 0,
) -> list[JobSummary]:
    stmt = (
        select(Job)
        .join(Repository, Repository.id == Job.repo_id)
        .where(Repository.owner_id == user.id)
        .options(selectinload(Job.repository), selectinload(Job.pull_request))
        .order_by(Job.created_at.desc())
        .limit(limit)
        .offset(offset)
    )
    if repo_id is not None:
        stmt = stmt.where(Job.repo_id == repo_id)
    if job_status is not None:
        stmt = stmt.where(Job.status == job_status)
    return [summary(j) for j in await db.scalars(stmt)]


@router.get("/{job_id}", response_model=JobDetail)
async def get_job(
    job_id: uuid.UUID, db: DbDep, redis: RedisDep, user: UserDep, settings: SettingsDep
) -> JobDetail:
    return await job_detail(db, redis, await get_owned_job(db, user, job_id), settings)


@router.get("/{job_id}/steps/{step_id}", response_model=StepDetail)
async def get_step(job_id: uuid.UUID, step_id: uuid.UUID, db: DbDep, user: UserDep) -> StepDetail:
    await get_owned_job(db, user, job_id)
    step = await db.scalar(
        select(AgentStep).where(AgentStep.id == step_id, AgentStep.job_id == job_id)
    )
    if step is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "step not found")
    return StepDetail.model_validate(step)


@router.get("/{job_id}/events")
async def job_events(
    job_id: uuid.UUID, request: Request, db: DbDep, redis: RedisDep, user: UserDep
) -> StreamingResponse:
    """Server-sent events: `progress` on every change, `end` once settled."""
    job = await get_owned_job(db, user, job_id)
    settled = job.status in SETTLED
    return StreamingResponse(
        progress_stream(redis, str(job_id), request, settled=settled),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


def _text(raw: bytes | str) -> str:
    return raw.decode() if isinstance(raw, bytes) else raw


async def relay(pubsub: PubSub, request: Request) -> AsyncIterator[str | None]:
    """Yield pub/sub messages until the client disconnects, and None every
    KEEPALIVE_SECONDS of silence so proxies keep the connection open."""
    last = time.monotonic()
    while not await request.is_disconnected():
        # Returns None on timeout and for the (ignored) subscribe confirmation.
        msg = await pubsub.get_message(ignore_subscribe_messages=True, timeout=1.0)
        if msg is not None:
            last = time.monotonic()
            yield _text(msg["data"])
        elif time.monotonic() - last >= KEEPALIVE_SECONDS:
            last = time.monotonic()
            yield None


def sse(event: str, data: str) -> str:
    return f"event: {event}\ndata: {data}\n\n"


async def progress_stream(
    redis: Redis, job_id: str, request: Request, *, settled: bool
) -> AsyncGenerator[str, None]:
    if settled:
        yield sse("end", json.dumps({"job_id": job_id}))
        return
    pubsub = redis.pubsub()
    await pubsub.subscribe(events_channel(job_id))
    try:
        # Subscribe first, then send the snapshot, so no change is missed.
        snapshot = await redis.get(progress_key(job_id))
        if snapshot:
            yield sse("progress", _text(snapshot))
        async for data in relay(pubsub, request):
            if data is None:
                yield ": keepalive\n\n"
                continue
            yield sse("progress", data)
            if json.loads(data).get("status") in {s.value for s in SETTLED}:
                yield sse("end", json.dumps({"job_id": job_id}))
                return
    finally:
        await pubsub.unsubscribe()
        await pubsub.aclose()  # type: ignore[no-untyped-call]


# --- review decision ----------------------------------------------------------


@router.post("/{job_id}/approve", response_model=JobDetail)
async def approve_job(
    job_id: uuid.UUID,
    db: DbDep,
    redis: RedisDep,
    user: UserDep,
    settings: SettingsDep,
    github: GitHubDep,
) -> JobDetail:
    job = await get_owned_job(db, user, job_id)
    patch = await final_patch(db, job)
    repo = job.repository
    to_github = is_github_repo(repo, settings)
    if to_github:
        if not user.github_token:
            raise HTTPException(
                status.HTTP_400_BAD_REQUEST, "a GitHub token is needed to open a pull request"
            )
        if not patch.files or not job.base_commit_sha:
            raise HTTPException(status.HTTP_409_CONFLICT, "patch has no file contents to commit")

    await transition(db, job, {JobStatus.AWAITING_REVIEW}, JobStatus.APPROVED)
    await db.execute(update(Patch).where(Patch.id == patch.id).values(status=PatchStatus.APPROVED))
    await db.commit()
    if not to_github:
        # Local and sample repositories have nowhere to open a PR.
        return await reload(db, redis, user, job_id, settings)

    branch = f"devassist/job-{job.id.hex[:8]}-v{patch.iteration}"
    assert patch.files is not None and job.base_commit_sha is not None
    try:
        async with github(user.github_token) as gh:
            pr = await gh.create_pull_request(
                repo.full_name,
                base_branch=repo.default_branch,
                base_commit=job.base_commit_sha,
                branch=branch,
                title=pull_request_title(job.task),
                body=pull_request_body(job, patch),
                files=patch.files,
            )
    except GitHubError as e:
        # Undo the approval so the reviewer can retry.
        await db.execute(
            update(Job)
            .where(Job.id == job.id)
            .values(status=JobStatus.AWAITING_REVIEW, error=f"opening the PR failed: {e}")
        )
        await db.execute(
            update(Patch).where(Patch.id == patch.id).values(status=PatchStatus.PASSED)
        )
        await db.commit()
        raise HTTPException(status.HTTP_502_BAD_GATEWAY, str(e)) from e

    db.add(
        PullRequest(
            job_id=job.id,
            patch_id=patch.id,
            repo_id=repo.id,
            number=pr.number,
            url=pr.url,
            branch_name=pr.branch,
        )
    )
    await db.execute(
        update(Job)
        .where(Job.id == job.id)
        .values(status=JobStatus.PR_OPENED, error=None, completed_at=datetime.now(UTC))
    )
    await db.commit()
    log.info("pull request opened", extra={"job_id": str(job.id), "url": pr.url})
    return await reload(db, redis, user, job_id, settings)


@router.post("/{job_id}/reject", response_model=JobDetail)
async def reject_job(
    job_id: uuid.UUID,
    body: Reject,
    db: DbDep,
    redis: RedisDep,
    user: UserDep,
    settings: SettingsDep,
) -> JobDetail:
    job = await get_owned_job(db, user, job_id)
    await transition(
        db,
        job,
        {JobStatus.AWAITING_REVIEW, JobStatus.FAILED},
        JobStatus.REJECTED,
        review_feedback=body.reason,
        completed_at=datetime.now(UTC),
    )
    await db.execute(
        update(Patch)
        .where(Patch.job_id == job.id, Patch.status == PatchStatus.PASSED)
        .values(status=PatchStatus.REJECTED)
    )
    await db.commit()
    return await reload(db, redis, user, job_id, settings)


@router.post("/{job_id}/request-changes", response_model=JobDetail)
async def request_changes(
    job_id: uuid.UUID,
    body: RequestChanges,
    db: DbDep,
    redis: RedisDep,
    user: UserDep,
    publisher: PublisherDep,
    settings: SettingsDep,
) -> JobDetail:
    """Send the job back to the agents. Patch numbering continues, and the
    feedback is appended to the task the Planner and Coder see."""
    job = await get_owned_job(db, user, job_id)
    await transition(
        db,
        job,
        {JobStatus.AWAITING_REVIEW, JobStatus.FAILED},
        JobStatus.CHANGES_REQUESTED,
        review_feedback=body.feedback.strip(),
        error=None,
        completed_at=None,
    )
    await db.commit()
    await publish_job(publisher, job)
    return await reload(db, redis, user, job_id, settings)
