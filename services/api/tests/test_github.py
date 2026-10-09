from __future__ import annotations

from typing import Any

import httpx
import pytest
from devassist_api.github import GitHubClient, GitHubError


@pytest.fixture
def fake(fake_github: Any) -> Any:
    return fake_github


def client(fake: Any) -> GitHubClient:
    return GitHubClient("tok", transport=httpx.MockTransport(fake))


async def test_get_user_and_repo(fake: Any) -> None:
    async with client(fake) as gh:
        user = await gh.get_user()
        repo = await gh.get_repo("octo/app")
    assert user.login == "octo" and user.id == 7
    assert repo.default_branch == "trunk" and repo.private


async def test_create_pull_request_builds_commit_on_base(fake: Any) -> None:
    async with client(fake) as gh:
        pr = await gh.create_pull_request(
            "octo/app",
            base_branch="trunk",
            base_commit="base",
            branch="devassist/job-1",
            title="Fix it",
            body="body",
            files={"b.py": "print(2)\n", "a.py": "print(1)\n", "old.py": None},
        )
    assert pr.number == 12 and pr.branch == "devassist/job-1"

    tree = next(body for m, p, body in fake.calls if p.endswith("/git/trees"))
    assert tree["base_tree"] == "tree0"
    entries = {e["path"]: e["sha"] for e in tree["tree"]}
    assert entries["old.py"] is None  # deletion
    assert entries["a.py"].startswith("blob-") and entries["b.py"].startswith("blob-")
    # Only changed, non-deleted files are uploaded as blobs.
    assert sum(1 for _, p, _ in fake.calls if p.endswith("/git/blobs")) == 2

    commit = next(body for m, p, body in fake.calls if p.endswith("/git/commits") and m == "POST")
    assert commit == {"message": "Fix it", "tree": "tree1", "parents": ["base"]}
    ref = next(body for m, p, body in fake.calls if p.endswith("/git/refs"))
    assert ref == {"ref": "refs/heads/devassist/job-1", "sha": "commit1"}
    pull = next(body for m, p, body in fake.calls if p.endswith("/pulls"))
    assert pull["head"] == "devassist/job-1" and pull["base"] == "trunk"


async def test_errors_carry_status_and_message(fake: Any) -> None:
    async with client(fake) as gh:
        with pytest.raises(GitHubError) as err:
            await gh.get_repo("octo/missing")
    assert err.value.status_code == 404
    assert "Not Found" in str(err.value)


async def test_sends_token_and_api_version() -> None:
    seen: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        return httpx.Response(200, json={"login": "x", "id": 1})

    async with GitHubClient("secret", transport=httpx.MockTransport(handler)) as gh:
        await gh.get_user()
    assert seen[0].headers["authorization"] == "Bearer secret"
    assert seen[0].headers["x-github-api-version"] == "2022-11-28"
