"""Shared fixtures for the API tests."""

from __future__ import annotations

import json
from typing import Any

import httpx
import pytest


class FakeGitHub:
    """Records requests and answers the Git Data API calls a PR needs."""

    def __init__(self) -> None:
        self.calls: list[tuple[str, str, Any]] = []

    def __call__(self, request: httpx.Request) -> httpx.Response:
        body: Any = json.loads(request.content) if request.content else None
        self.calls.append((request.method, request.url.path, body))
        path, method = request.url.path, request.method
        if path == "/user":
            return httpx.Response(200, json={"login": "octo", "id": 7, "email": None})
        if path == "/repos/octo/app" and method == "GET":
            return httpx.Response(
                200,
                json={
                    "full_name": "octo/app",
                    "default_branch": "trunk",
                    "clone_url": "https://github.com/octo/app.git",
                    "private": True,
                },
            )
        if path.startswith("/repos/octo/app/git/commits/"):
            return httpx.Response(200, json={"sha": "base", "tree": {"sha": "tree0"}})
        if path.endswith("/git/blobs"):
            return httpx.Response(201, json={"sha": f"blob-{len(self.calls)}"})
        if path.endswith("/git/trees"):
            return httpx.Response(201, json={"sha": "tree1"})
        if path.endswith("/git/commits"):
            return httpx.Response(201, json={"sha": "commit1"})
        if path.endswith("/git/refs"):
            return httpx.Response(201, json={"ref": body["ref"]})
        if path.endswith("/pulls"):
            return httpx.Response(
                201, json={"number": 12, "html_url": "https://github.com/octo/app/pull/12"}
            )
        return httpx.Response(404, json={"message": "Not Found"})


@pytest.fixture
def fake_github() -> FakeGitHub:
    return FakeGitHub()
