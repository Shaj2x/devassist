"""End-to-end through the public API, with every service running (make up).

    POST /v1/repos  -> repo.registered -> indexer -> repo.indexed
    POST /v1/jobs   -> job.created -> orchestrator (mock LLM) -> patch.generated
                    -> sandbox-runner -> validation.completed -> ... -> review
    POST /approve   -> approved (a local repo has nowhere to open a PR)

Only the model is scripted (LLM_PROVIDER=mock, datekit_fix_leap_year): the
Coder's first patch fails the sandbox tests, the Debugger fixes it.
"""

from __future__ import annotations

import asyncio
import json
import os
import time
import uuid
from typing import Any

import httpx
import pytest

pytestmark = pytest.mark.integration

API_URL = os.environ.get("API_URL", "http://localhost:8000")


async def wait_for(
    client: httpx.AsyncClient, path: str, done: Any, *, within: float, what: str
) -> dict[str, Any]:
    deadline = time.monotonic() + within
    while True:
        body: dict[str, Any] = (await client.get(path)).raise_for_status().json()
        if done(body):
            return body
        if time.monotonic() > deadline:
            pytest.fail(f"timed out waiting for {what}: {json.dumps(body)[:2000]}")
        await asyncio.sleep(1)


async def test_task_to_approved_patch_through_kafka() -> None:
    async with httpx.AsyncClient(base_url=API_URL, timeout=30) as client:
        me = await client.get("/v1/auth/me")
        if me.status_code != 200 or me.json()["auth_mode"] != "dev":
            pytest.skip("needs the stack running in dev auth mode")

        name = f"e2e/datekit-{uuid.uuid4().hex[:6]}"
        repo = (
            (
                await client.post(
                    "/v1/repos",
                    json={"full_name": name, "clone_url": "file:///sample-repos/datekit.git"},
                )
            )
            .raise_for_status()
            .json()
        )
        try:
            await run_job(client, repo)
        finally:
            await client.delete(f"/v1/repos/{repo['id']}")


async def run_job(client: httpx.AsyncClient, repo: dict[str, Any]) -> None:
    repo = await wait_for(
        client,
        f"/v1/repos/{repo['id']}",
        lambda r: (r["latest_snapshot"] or {}).get("status") in ("ready", "failed"),
        within=120,
        what="indexing",
    )
    assert repo["latest_snapshot"]["status"] == "ready", repo
    assert repo["latest_snapshot"]["chunk_count"] > 0

    job = (
        (
            await client.post(
                "/v1/jobs",
                json={"repo_id": repo["id"], "task": "Fix the failing test in datekit/calendar.py"},
            )
        )
        .raise_for_status()
        .json()
    )
    assert job["status"] == "queued"

    detail = await wait_for(
        client,
        f"/v1/jobs/{job['id']}",
        lambda j: j["status"] in ("awaiting_review", "failed"),
        within=600,
        what="the agent loop",
    )
    assert detail["status"] == "awaiting_review", detail.get("error")
    assert detail["base_commit_sha"] == repo["latest_snapshot"]["commit_sha"]

    agents = [s["agent"] for s in detail["steps"]]
    assert agents[0] == "planner" and agents[-1] == "reviewer"
    assert "debugger" in agents  # the first patch failed validation

    patches = detail["patches"]
    assert [p["status"] for p in patches] == ["superseded", "passed"]
    assert patches[0]["validations"][0]["tests_status"] == "failed"
    final = patches[-1]["validations"][0]
    assert final["status"] == "passed" and final["tests"]["failed"] == 0
    assert detail["review"]["risk_level"] in ("low", "medium", "high")
    assert detail["total_input_tokens"] > 0

    step = (await client.get(f"/v1/jobs/{job['id']}/steps/{detail['steps'][0]['id']}")).json()
    assert step["prompt"]["messages"] and step["parsed_output"]["summary"]

    events = await client.get(f"/v1/jobs/{job['id']}/events")
    assert events.text.startswith("event: end")

    approved = (await client.post(f"/v1/jobs/{job['id']}/approve")).raise_for_status().json()
    assert approved["status"] == "approved"
    assert approved["patches"][-1]["status"] == "approved"
