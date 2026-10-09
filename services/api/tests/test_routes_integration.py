"""Route tests against the real Postgres and Redis from `make up`.

Kafka is replaced by a recording publisher and GitHub by an httpx
MockTransport, so these exercise the SQL, the status transitions and the
response shapes without network or agents. What the orchestrator would
write (patches, validation runs, final status) is inserted directly.
"""

from __future__ import annotations

import json
import os
import uuid
from collections.abc import AsyncIterator
from typing import Any

import httpx
import pytest
from cryptography.fernet import Fernet
from devassist_api.config import Settings
from devassist_api.github import GitHubClient
from devassist_api.main import create_app
from devassist_api.routers.jobs import progress_stream
from devassist_common.db import (
    Job,
    JobStatus,
    Patch,
    PatchStatus,
    Repository,
    RepoSnapshot,
    SnapshotStatus,
    User,
    ValidationRun,
    ValidationStatus,
)
from devassist_common.db.session import create_engine, create_session_factory
from devassist_common.events import EventType
from devassist_common.progress import events_channel, progress_key
from fastapi import FastAPI
from redis.asyncio import Redis
from sqlalchemy import delete, or_, select, update
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

pytestmark = pytest.mark.integration

DATABASE_URL = os.environ.get(
    "DATABASE_URL", "postgresql://devassist:devassist@localhost:5432/devassist"
)
REDIS_URL = os.environ.get("REDIS_URL", "redis://localhost:6379/0")


class RecordingPublisher:
    def __init__(self) -> None:
        self.events: list[tuple[EventType, dict[str, Any]]] = []

    async def publish(self, event_type: EventType, payload: Any, *, trace_id: str) -> None:
        self.events.append((event_type, payload.model_dump(mode="json")))


@pytest.fixture
async def infra() -> AsyncIterator[tuple[async_sessionmaker[AsyncSession], Redis]]:
    engine = create_engine(DATABASE_URL)
    redis = Redis.from_url(REDIS_URL)
    sessions = create_session_factory(engine)
    yield sessions, redis
    # Leave the dev database as we found it (jobs and snapshots cascade).
    async with sessions() as s:
        await s.execute(
            delete(Repository).where(
                or_(Repository.full_name.like("test/local-%"), Repository.full_name == "octo/app")
            )
        )
        await s.execute(delete(User).where(User.github_login == "octo"))
        await s.commit()
    await redis.aclose()
    await engine.dispose()


def build_app(
    infra: tuple[async_sessionmaker[AsyncSession], Redis], fake_github: Any, **overrides: Any
) -> tuple[FastAPI, RecordingPublisher]:
    settings = Settings(_env_file=None, run_consumers=False, github_token="tok", **overrides)  # noqa: S106
    app = create_app(settings)
    publisher = RecordingPublisher()
    app.state.sessions, app.state.redis = infra
    app.state.publisher = publisher
    app.state.github_factory = lambda token: GitHubClient(
        token, transport=httpx.MockTransport(fake_github)
    )
    return app, publisher


@pytest.fixture
async def api(
    infra: tuple[async_sessionmaker[AsyncSession], Redis], fake_github: Any
) -> AsyncIterator[tuple[httpx.AsyncClient, RecordingPublisher]]:
    app, publisher = build_app(infra, fake_github)
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        yield client, publisher


async def mark_indexed(sessions: async_sessionmaker[AsyncSession], repo_id: str) -> str:
    sha = uuid.uuid4().hex + "abcdefgh"
    async with sessions() as s:
        s.add(
            RepoSnapshot(
                repo_id=uuid.UUID(repo_id),
                commit_sha=sha,
                status=SnapshotStatus.READY,
                file_count=3,
                chunk_count=9,
            )
        )
        await s.commit()
    return sha


async def finish_job(sessions: async_sessionmaker[AsyncSession], job_id: str) -> str:
    """Write what the orchestrator writes when a job reaches review."""
    async with sessions() as s:
        failed = Patch(
            job_id=uuid.UUID(job_id), iteration=1, diff="-", status=PatchStatus.SUPERSEDED
        )
        patch = Patch(
            job_id=uuid.UUID(job_id),
            iteration=2,
            diff="--- a/x.py\n+++ b/x.py\n",
            files={"x.py": "print('fixed')\n", "gone.py": None},
            status=PatchStatus.PASSED,
            files_changed=2,
            additions=1,
            deletions=4,
        )
        s.add_all([failed, patch])
        await s.flush()
        s.add(
            ValidationRun(
                patch_id=patch.id,
                status=ValidationStatus.PASSED,
                tests={"status": "passed", "summary": "22 passed"},
                security={"status": "passed"},
                static_analysis={"status": "passed"},
            )
        )
        await s.execute(
            update(Job)
            .where(Job.id == uuid.UUID(job_id))
            .values(
                status=JobStatus.AWAITING_REVIEW,
                current_iteration=2,
                plan={"summary": "fix x", "steps": ["edit x.py"]},
                review={"summary": "looks right", "risk_level": "low", "concerns": []},
            )
        )
        await s.commit()
        return str(patch.id)


async def new_local_repo(client: httpx.AsyncClient) -> dict[str, Any]:
    name = f"test/local-{uuid.uuid4().hex[:8]}"
    resp = await client.post(
        "/v1/repos",
        json={"full_name": name, "clone_url": "file:///sample-repos/datekit.git"},
    )
    assert resp.status_code == 201, resp.text
    repo: dict[str, Any] = resp.json()
    return repo


async def test_register_local_repo_publishes_and_lists(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
) -> None:
    client, publisher = api
    repo = await new_local_repo(client)
    assert repo["is_github"] is False and repo["latest_snapshot"] is None
    assert publisher.events[-1][0] == EventType.REPO_REGISTERED
    assert publisher.events[-1][1]["repo_id"] == repo["id"]

    listed = (await client.get("/v1/repos")).json()
    assert repo["id"] in {r["id"] for r in listed}

    assert (await client.post(f"/v1/repos/{repo['id']}/reindex")).status_code == 202
    assert len(publisher.events) == 2
    assert (await client.delete(f"/v1/repos/{repo['id']}")).status_code == 204
    assert (await client.get(f"/v1/repos/{repo['id']}")).status_code == 404


async def test_register_github_repo_uses_github_metadata(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
) -> None:
    client, _ = api
    resp = await client.post("/v1/repos", json={"full_name": "octo/app"})
    assert resp.status_code == 201, resp.text
    repo = resp.json()
    assert repo["is_github"] is True
    assert repo["default_branch"] == "trunk"
    assert repo["clone_url"] == "https://github.com/octo/app.git"

    missing = await client.post("/v1/repos", json={"full_name": "octo/missing"})
    assert missing.status_code == 404


async def test_job_requires_an_indexed_repo(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
) -> None:
    client, _ = api
    repo = await new_local_repo(client)
    resp = await client.post("/v1/jobs", json={"repo_id": repo["id"], "task": "Fix it"})
    assert resp.status_code == 409


async def test_job_lifecycle_on_local_repo(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
    infra: tuple[async_sessionmaker[AsyncSession], Redis],
) -> None:
    client, publisher = api
    sessions, redis = infra
    repo = await new_local_repo(client)
    sha = await mark_indexed(sessions, repo["id"])

    resp = await client.post(
        "/v1/jobs", json={"repo_id": repo["id"], "task": "Fix the failing test"}
    )
    assert resp.status_code == 202, resp.text
    job = resp.json()
    assert job["status"] == "queued" and job["max_iterations"] == 3
    event_type, payload = publisher.events[-1]
    assert event_type == EventType.JOB_CREATED
    assert payload["job_id"] == job["id"] and payload["base_commit_sha"] == sha

    # While running, the detail carries the live progress document.
    await redis.set(progress_key(job["id"]), json.dumps({"status": "coding"}), ex=60)
    detail = (await client.get(f"/v1/jobs/{job['id']}")).json()
    assert detail["progress"] == {"status": "coding"}
    assert detail["base_commit_sha"] == sha

    # Approving before the agents finish is refused.
    assert (await client.post(f"/v1/jobs/{job['id']}/approve")).status_code == 409

    patch_id = await finish_job(sessions, job["id"])
    detail = (await client.get(f"/v1/jobs/{job['id']}")).json()
    assert detail["status"] == "awaiting_review"
    assert [p["iteration"] for p in detail["patches"]] == [1, 2]
    assert detail["patches"][1]["validations"][0]["tests"]["summary"] == "22 passed"
    assert detail["progress"] is None

    approved = await client.post(f"/v1/jobs/{job['id']}/approve")
    assert approved.status_code == 200, approved.text
    assert approved.json()["status"] == "approved"  # local repo: no PR
    assert approved.json()["pull_request"] is None
    async with sessions() as s:
        patch = await s.get(Patch, uuid.UUID(patch_id))
        assert patch is not None and patch.status == PatchStatus.APPROVED

    again = await client.post(f"/v1/jobs/{job['id']}/approve")
    assert again.status_code == 409

    listed = (await client.get("/v1/jobs", params={"repo_id": repo["id"]})).json()
    assert [j["id"] for j in listed] == [job["id"]]
    assert listed[0]["repo_name"] == repo["full_name"]
    filtered = (await client.get("/v1/jobs", params={"status": "queued"})).json()
    assert job["id"] not in {j["id"] for j in filtered}


async def test_request_changes_and_reject(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
    infra: tuple[async_sessionmaker[AsyncSession], Redis],
) -> None:
    client, publisher = api
    sessions, _ = infra
    repo = await new_local_repo(client)
    await mark_indexed(sessions, repo["id"])
    job = (await client.post("/v1/jobs", json={"repo_id": repo["id"], "task": "Do x"})).json()
    await finish_job(sessions, job["id"])

    resp = await client.post(
        f"/v1/jobs/{job['id']}/request-changes", json={"feedback": "also handle 1900"}
    )
    assert resp.status_code == 200, resp.text
    assert resp.json()["status"] == "changes_requested"
    assert resp.json()["review_feedback"] == "also handle 1900"
    assert publisher.events[-1][0] == EventType.JOB_CREATED
    assert publisher.events[-1][1]["job_id"] == job["id"]

    # Not reviewable while the agents re-run.
    assert (await client.post(f"/v1/jobs/{job['id']}/reject", json={})).status_code == 409
    async with sessions() as s:
        await s.execute(
            update(Job)
            .where(Job.id == uuid.UUID(job["id"]))
            .values(status=JobStatus.AWAITING_REVIEW)
        )
        await s.commit()
    rejected = await client.post(f"/v1/jobs/{job['id']}/reject", json={"reason": "not needed"})
    assert rejected.status_code == 200
    assert rejected.json()["status"] == "rejected"


async def test_approve_github_job_opens_pull_request(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
    infra: tuple[async_sessionmaker[AsyncSession], Redis],
    fake_github: Any,
) -> None:
    client, _ = api
    sessions, _ = infra
    repo = (await client.post("/v1/repos", json={"full_name": "octo/app"})).json()
    sha = await mark_indexed(sessions, repo["id"])
    job = (
        await client.post("/v1/jobs", json={"repo_id": repo["id"], "task": "Fix x\nmore"})
    ).json()
    await finish_job(sessions, job["id"])

    resp = await client.post(f"/v1/jobs/{job['id']}/approve")
    assert resp.status_code == 200, resp.text
    detail = resp.json()
    assert detail["status"] == "pr_opened"
    assert detail["pull_request"]["number"] == 12
    assert detail["pull_request"]["branch_name"] == f"devassist/job-{job['id'][:8]}-v2"
    assert detail["pr_url"] == "https://github.com/octo/app/pull/12"

    commit = next(b for m, p, b in fake_github.calls if p.endswith("/git/commits") and m == "POST")
    assert commit["parents"] == [sha] and commit["message"] == "Fix x"
    tree = next(b for _, p, b in fake_github.calls if p.endswith("/git/trees"))
    assert {e["path"]: e["sha"] is None for e in tree["tree"]} == {"gone.py": True, "x.py": False}
    pull = next(b for _, p, b in fake_github.calls if p.endswith("/pulls"))
    assert pull["base"] == "trunk" and "22 passed" in pull["body"]


async def test_failed_pull_request_returns_job_to_review(
    infra: tuple[async_sessionmaker[AsyncSession], Redis], fake_github: Any
) -> None:
    def broken(request: httpx.Request) -> httpx.Response:
        if request.url.path.endswith("/pulls"):
            return httpx.Response(422, json={"message": "Validation Failed"})
        response: httpx.Response = fake_github(request)
        return response

    app, _ = build_app(infra, broken)
    sessions, _ = infra
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://test"
    ) as client:
        repo = (await client.post("/v1/repos", json={"full_name": "octo/app"})).json()
        await mark_indexed(sessions, repo["id"])
        job = (await client.post("/v1/jobs", json={"repo_id": repo["id"], "task": "Fix"})).json()
        await finish_job(sessions, job["id"])
        resp = await client.post(f"/v1/jobs/{job['id']}/approve")
        assert resp.status_code == 502
        detail = (await client.get(f"/v1/jobs/{job['id']}")).json()
    assert detail["status"] == "awaiting_review"
    assert "Validation Failed" in detail["error"]
    assert detail["patches"][-1]["status"] == "passed"


async def test_step_detail_includes_prompt(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
    infra: tuple[async_sessionmaker[AsyncSession], Redis],
) -> None:
    from devassist_common.db import AgentName, AgentStep, AgentStepStatus

    client, _ = api
    sessions, _ = infra
    repo = await new_local_repo(client)
    await mark_indexed(sessions, repo["id"])
    job = (await client.post("/v1/jobs", json={"repo_id": repo["id"], "task": "Do y"})).json()
    async with sessions() as s:
        step = AgentStep(
            job_id=uuid.UUID(job["id"]),
            iteration=1,
            sequence=1,
            agent=AgentName.PLANNER,
            status=AgentStepStatus.SUCCEEDED,
            prompt={"system": "plan", "messages": []},
            response='{"summary": "s"}',
            parsed_output={"summary": "s"},
            input_tokens=10,
            output_tokens=5,
        )
        s.add(step)
        await s.commit()
        step_id = str(step.id)

    detail = (await client.get(f"/v1/jobs/{job['id']}")).json()
    assert detail["steps"][0]["agent"] == "planner"
    assert "prompt" not in detail["steps"][0]
    full = (await client.get(f"/v1/jobs/{job['id']}/steps/{step_id}")).json()
    assert full["prompt"]["system"] == "plan" and full["parsed_output"] == {"summary": "s"}
    other = await client.get(f"/v1/jobs/{job['id']}/steps/{uuid.uuid4()}")
    assert other.status_code == 404


async def test_settled_job_event_stream_ends_immediately(
    api: tuple[httpx.AsyncClient, RecordingPublisher],
    infra: tuple[async_sessionmaker[AsyncSession], Redis],
) -> None:
    client, _ = api
    sessions, _ = infra
    repo = await new_local_repo(client)
    await mark_indexed(sessions, repo["id"])
    job = (await client.post("/v1/jobs", json={"repo_id": repo["id"], "task": "Do z"})).json()
    await finish_job(sessions, job["id"])
    resp = await client.get(f"/v1/jobs/{job['id']}/events")
    assert resp.headers["content-type"].startswith("text/event-stream")
    assert resp.text.startswith("event: end\n")


async def test_live_event_stream_relays_progress(
    infra: tuple[async_sessionmaker[AsyncSession], Redis],
) -> None:
    _, redis = infra
    job_id = str(uuid.uuid4())
    await redis.set(progress_key(job_id), json.dumps({"status": "planning"}), ex=60)

    class Connected:
        async def is_disconnected(self) -> bool:
            return False

    stream = progress_stream(redis, job_id, Connected(), settled=False)  # type: ignore[arg-type]
    assert await anext(stream) == 'event: progress\ndata: {"status": "planning"}\n\n'
    await redis.publish(events_channel(job_id), json.dumps({"status": "coding"}))
    assert await anext(stream) == 'event: progress\ndata: {"status": "coding"}\n\n'
    await redis.publish(events_channel(job_id), json.dumps({"status": "awaiting_review"}))
    assert "awaiting_review" in await anext(stream)
    assert (await anext(stream)).startswith("event: end")
    await stream.aclose()


async def test_token_mode_sign_in(
    infra: tuple[async_sessionmaker[AsyncSession], Redis], fake_github: Any
) -> None:
    key = Fernet.generate_key().decode()
    app, _ = build_app(infra, fake_github, auth_mode="token", token_encryption_key=key)
    sessions, _ = infra
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://test"
    ) as client:
        assert (await client.get("/v1/repos")).status_code == 401
        login = await client.post("/v1/auth/token", json={"github_token": "ghp_x"})
        assert login.status_code == 200, login.text
        token = login.json()["session_token"]
        headers = {"Authorization": f"Bearer {token}"}
        me = (await client.get("/v1/auth/me", headers=headers)).json()
        assert me["user"]["login"] == "octo" and me["github_connected"] is True
        assert me["auth_mode"] == "token"
        # Local clone URLs are refused outside dev mode.
        local = await client.post(
            "/v1/repos",
            json={"full_name": "x/y", "clone_url": "file:///etc"},
            headers=headers,
        )
        assert local.status_code == 403
        assert (await client.post("/v1/auth/logout", headers=headers)).status_code == 204
        assert (await client.get("/v1/auth/me", headers=headers)).status_code == 401

    async with sessions() as s:
        user = await s.scalar(select(User).where(User.github_login == "octo"))
        assert user is not None and user.github_token_encrypted is not None
        assert b"ghp_x" not in user.github_token_encrypted
