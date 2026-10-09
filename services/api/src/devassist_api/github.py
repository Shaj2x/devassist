"""A small GitHub REST client: who am I, repo metadata, and opening a PR.

Pull requests are built with the Git Data API (blobs -> tree -> commit ->
branch -> PR), so the API service needs no git binary and no clone: the
orchestrator stored the final content of every changed file on the patch.
The new commit's parent is the commit the agents worked from, so the PR
shows exactly the validated change even if the base branch has moved on.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

import httpx


class GitHubError(Exception):
    def __init__(self, message: str, status_code: int | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code


@dataclass(frozen=True)
class GitHubUser:
    login: str
    id: int
    email: str | None
    avatar_url: str | None


@dataclass(frozen=True)
class GitHubRepo:
    full_name: str
    default_branch: str
    clone_url: str
    private: bool


@dataclass(frozen=True)
class PullRequestRef:
    number: int
    url: str
    branch: str


class GitHubClient:
    def __init__(
        self,
        token: str,
        *,
        base_url: str = "https://api.github.com",
        transport: httpx.AsyncBaseTransport | None = None,
        timeout: float = 20.0,
    ) -> None:
        self._client = httpx.AsyncClient(
            base_url=base_url,
            transport=transport,
            timeout=timeout,
            headers={
                "Authorization": f"Bearer {token}",
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "DevAssist",
            },
        )

    async def __aenter__(self) -> GitHubClient:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self._client.aclose()

    async def _request(self, method: str, path: str, **kwargs: Any) -> Any:
        try:
            resp = await self._client.request(method, path, **kwargs)
        except httpx.HTTPError as e:
            raise GitHubError(f"could not reach GitHub: {e}") from e
        if resp.status_code >= 400:
            message = (
                resp.json().get("message", resp.text)
                if resp.headers.get("content-type", "").startswith("application/json")
                else resp.text
            )
            raise GitHubError(
                f"GitHub {method} {path} failed ({resp.status_code}): {message}", resp.status_code
            )
        return resp.json() if resp.content else None

    async def get_user(self) -> GitHubUser:
        data = await self._request("GET", "/user")
        return GitHubUser(
            login=data["login"],
            id=data["id"],
            email=data.get("email"),
            avatar_url=data.get("avatar_url"),
        )

    async def get_repo(self, full_name: str) -> GitHubRepo:
        data = await self._request("GET", f"/repos/{full_name}")
        return GitHubRepo(
            full_name=data["full_name"],
            default_branch=data["default_branch"],
            clone_url=data["clone_url"],
            private=data["private"],
        )

    async def create_pull_request(
        self,
        full_name: str,
        *,
        base_branch: str,
        base_commit: str,
        branch: str,
        title: str,
        body: str,
        files: dict[str, str | None],
    ) -> PullRequestRef:
        repo = f"/repos/{full_name}"
        commit = await self._request("GET", f"{repo}/git/commits/{base_commit}")
        tree: list[dict[str, Any]] = []
        for path, content in sorted(files.items()):
            if content is None:  # deletion
                tree.append({"path": path, "mode": "100644", "type": "blob", "sha": None})
                continue
            blob = await self._request(
                "POST", f"{repo}/git/blobs", json={"content": content, "encoding": "utf-8"}
            )
            tree.append({"path": path, "mode": "100644", "type": "blob", "sha": blob["sha"]})
        new_tree = await self._request(
            "POST", f"{repo}/git/trees", json={"base_tree": commit["tree"]["sha"], "tree": tree}
        )
        new_commit = await self._request(
            "POST",
            f"{repo}/git/commits",
            json={"message": title, "tree": new_tree["sha"], "parents": [base_commit]},
        )
        await self._request(
            "POST",
            f"{repo}/git/refs",
            json={"ref": f"refs/heads/{branch}", "sha": new_commit["sha"]},
        )
        pr = await self._request(
            "POST",
            f"{repo}/pulls",
            json={"title": title, "head": branch, "base": base_branch, "body": body},
        )
        return PullRequestRef(number=pr["number"], url=pr["html_url"], branch=branch)
