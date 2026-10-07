"""The agents' retrieval tool: semantic code search and symbol lookup,
backed by the indexer's HTTP API.

Retrieval failures degrade instead of failing the job: an agent with no
search results still has the file tree and the files the plan names.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from typing import Any, Protocol

import httpx

log = logging.getLogger(__name__)


@dataclass(frozen=True)
class CodeHit:
    file_path: str
    start_line: int
    end_line: int
    symbol: str
    kind: str
    content: str
    score: float

    def render(self) -> str:
        label = f" ({self.kind} {self.symbol})" if self.symbol else ""
        location = f"{self.file_path}:{self.start_line}-{self.end_line}"
        return f"### {location}{label}\n```\n{self.content}\n```"


class Retriever(Protocol):
    """What the workflow needs from code search."""

    async def search(self, repo_id: str, query: str, top_k: int = 8) -> list[CodeHit]: ...

    async def search_many(
        self, repo_id: str, queries: list[str], top_k: int = 8
    ) -> list[CodeHit]: ...


class RetrievalTool:
    def __init__(
        self, base_url: str, *, timeout: float = 15.0, client: httpx.AsyncClient | None = None
    ) -> None:
        self.client = client or httpx.AsyncClient(base_url=base_url, timeout=timeout)

    async def search(self, repo_id: str, query: str, top_k: int = 8) -> list[CodeHit]:
        try:
            resp = await self.client.post(
                "/v1/search", json={"repo_id": repo_id, "query": query, "top_k": top_k}
            )
            if resp.status_code == 404:
                log.warning(
                    "repository not indexed; continuing without search", extra={"repo_id": repo_id}
                )
                return []
            resp.raise_for_status()
        except httpx.HTTPError as e:
            log.warning("code search failed; continuing without it", extra={"error": str(e)})
            return []
        return [_hit(h) for h in resp.json().get("results", [])]

    async def search_many(self, repo_id: str, queries: list[str], top_k: int = 8) -> list[CodeHit]:
        """Run several queries and merge results, best score first, deduplicated."""
        seen: dict[tuple[str, int], CodeHit] = {}
        for q in queries:
            for hit in await self.search(repo_id, q, top_k):
                key = (hit.file_path, hit.start_line)
                if key not in seen or seen[key].score < hit.score:
                    seen[key] = hit
        return sorted(seen.values(), key=lambda h: h.score, reverse=True)[: top_k * 2]

    async def aclose(self) -> None:
        await self.client.aclose()


def _hit(raw: dict[str, Any]) -> CodeHit:
    return CodeHit(
        file_path=raw["file_path"],
        start_line=raw["start_line"],
        end_line=raw["end_line"],
        symbol=raw.get("symbol_name") or "",
        kind=raw.get("symbol_kind") or "",
        content=raw.get("content", ""),
        score=float(raw.get("score") or 0),
    )
