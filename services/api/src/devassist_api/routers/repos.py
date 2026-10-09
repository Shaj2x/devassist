"""Repositories: register, list, reindex, delete.

Registering (or reindexing) publishes `repo.registered`; the indexer clones,
chunks and embeds the default branch and answers with `repo.indexed`.
"""

from __future__ import annotations

import uuid

from devassist_common.db import Repository, RepoSnapshot
from devassist_common.events import EventType, RepoRegistered
from fastapi import APIRouter, HTTPException, Response, status
from sqlalchemy import delete, select
from sqlalchemy.dialects.postgresql import distinct_on, insert
from sqlalchemy.ext.asyncio import AsyncSession

from devassist_api.config import Settings
from devassist_api.deps import (
    CurrentUser,
    DbDep,
    GitHubDep,
    PublisherDep,
    SettingsDep,
    UserDep,
    github_web_url,
)
from devassist_api.github import GitHubError
from devassist_api.schemas import RepoCreate, RepoOut, SnapshotOut

router = APIRouter(prefix="/v1/repos", tags=["repositories"])


def is_github_repo(repo: Repository, settings: Settings) -> bool:
    return repo.clone_url.startswith(github_web_url(settings.github_api_url) + "/")


async def latest_snapshots(
    db: AsyncSession, repo_ids: list[uuid.UUID]
) -> dict[uuid.UUID, RepoSnapshot]:
    if not repo_ids:
        return {}
    rows = await db.scalars(
        select(RepoSnapshot)
        .where(RepoSnapshot.repo_id.in_(repo_ids))
        .order_by(RepoSnapshot.repo_id, RepoSnapshot.created_at.desc())
        .ext(distinct_on(RepoSnapshot.repo_id))
    )
    return {s.repo_id: s for s in rows}


def repo_out(repo: Repository, snapshot: RepoSnapshot | None, settings: Settings) -> RepoOut:
    return RepoOut(
        id=repo.id,
        full_name=repo.full_name,
        clone_url=repo.clone_url,
        default_branch=repo.default_branch,
        config=repo.config or {},
        is_github=is_github_repo(repo, settings),
        created_at=repo.created_at,
        latest_snapshot=SnapshotOut.model_validate(snapshot) if snapshot else None,
    )


async def get_owned_repo(db: AsyncSession, user: CurrentUser, repo_id: uuid.UUID) -> Repository:
    repo = await db.scalar(
        select(Repository).where(Repository.id == repo_id, Repository.owner_id == user.id)
    )
    if repo is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "repository not found")
    return repo


async def publish_registered(publisher: PublisherDep, repo: Repository) -> None:
    await publisher.publish(
        EventType.REPO_REGISTERED,
        RepoRegistered(
            repo_id=repo.id,
            full_name=repo.full_name,
            clone_url=repo.clone_url,
            default_branch=repo.default_branch,
        ),
        trace_id=str(repo.id),
    )


@router.post("", response_model=RepoOut, status_code=status.HTTP_201_CREATED)
async def register_repo(
    body: RepoCreate,
    db: DbDep,
    user: UserDep,
    publisher: PublisherDep,
    settings: SettingsDep,
    github: GitHubDep,
) -> RepoOut:
    if body.clone_url is not None:
        if not settings.local_repos_allowed:
            raise HTTPException(
                status.HTTP_403_FORBIDDEN, "only GitHub repositories can be registered here"
            )
        clone_url, branch = body.clone_url, body.default_branch or "main"
    else:
        try:
            async with github(user.github_token) as gh:
                gh_repo = await gh.get_repo(body.full_name)
        except GitHubError as e:
            if e.status_code == 404:
                raise HTTPException(
                    status.HTTP_404_NOT_FOUND,
                    f"{body.full_name} not found on GitHub (or the token cannot see it)",
                ) from e
            raise HTTPException(status.HTTP_502_BAD_GATEWAY, str(e)) from e
        clone_url = gh_repo.clone_url
        branch = body.default_branch or gh_repo.default_branch

    config = body.config.model_dump(exclude_none=True)
    values = {
        "owner_id": user.id,
        "full_name": body.full_name,
        "clone_url": clone_url,
        "default_branch": branch,
        "config": config,
    }
    stmt = (
        insert(Repository)
        .values(**values)
        .on_conflict_do_update(
            index_elements=[Repository.owner_id, Repository.full_name],
            set_={"clone_url": clone_url, "default_branch": branch, "config": config},
        )
        .returning(Repository.id)
    )
    repo_id = await db.scalar(stmt)
    await db.commit()
    repo = await db.get(Repository, repo_id, populate_existing=True)
    assert repo is not None
    await publish_registered(publisher, repo)
    snapshots = await latest_snapshots(db, [repo.id])
    return repo_out(repo, snapshots.get(repo.id), settings)


@router.get("", response_model=list[RepoOut])
async def list_repos(db: DbDep, user: UserDep, settings: SettingsDep) -> list[RepoOut]:
    repos = list(
        await db.scalars(
            select(Repository)
            .where(Repository.owner_id == user.id)
            .order_by(Repository.created_at.desc())
        )
    )
    snapshots = await latest_snapshots(db, [r.id for r in repos])
    return [repo_out(r, snapshots.get(r.id), settings) for r in repos]


@router.get("/{repo_id}", response_model=RepoOut)
async def get_repo(repo_id: uuid.UUID, db: DbDep, user: UserDep, settings: SettingsDep) -> RepoOut:
    repo = await get_owned_repo(db, user, repo_id)
    snapshots = await latest_snapshots(db, [repo.id])
    return repo_out(repo, snapshots.get(repo.id), settings)


@router.post("/{repo_id}/reindex", status_code=status.HTTP_202_ACCEPTED)
async def reindex_repo(
    repo_id: uuid.UUID, db: DbDep, user: UserDep, publisher: PublisherDep
) -> dict[str, str]:
    repo = await get_owned_repo(db, user, repo_id)
    await publish_registered(publisher, repo)
    return {"status": "queued"}


@router.delete("/{repo_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_repo(repo_id: uuid.UUID, db: DbDep, user: UserDep) -> Response:
    repo = await get_owned_repo(db, user, repo_id)
    # Snapshots, chunks and jobs go with it (ON DELETE CASCADE).
    await db.execute(delete(Repository).where(Repository.id == repo.id))
    await db.commit()
    return Response(status_code=status.HTTP_204_NO_CONTENT)
