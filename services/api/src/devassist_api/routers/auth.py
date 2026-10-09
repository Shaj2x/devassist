"""Sign-in with a GitHub personal access token.

The token is checked against GitHub (`GET /user`), stored encrypted, and
exchanged for an opaque DevAssist session token. In dev mode there is no
sign-in: every request acts as the local `demo` user.
"""

from __future__ import annotations

from devassist_common.db import User
from fastapi import APIRouter, HTTPException, status
from sqlalchemy.dialects.postgresql import insert

from devassist_api.deps import DbDep, GitHubDep, RedisDep, SettingsDep, UserDep
from devassist_api.github import GitHubError
from devassist_api.schemas import Me, SessionOut, TokenLogin, UserOut
from devassist_api.security import TokenCipher, create_session, end_session

router = APIRouter(prefix="/v1/auth", tags=["auth"])


@router.post("/token", response_model=SessionOut)
async def login_with_token(
    body: TokenLogin, db: DbDep, redis: RedisDep, settings: SettingsDep, github: GitHubDep
) -> SessionOut:
    if settings.auth_mode != "token":
        raise HTTPException(status.HTTP_409_CONFLICT, "sign-in is disabled in dev auth mode")
    try:
        async with github(body.github_token) as gh:
            gh_user = await gh.get_user()
    except GitHubError as e:
        if e.status_code == 401:
            raise HTTPException(status.HTTP_401_UNAUTHORIZED, "GitHub rejected the token") from e
        raise HTTPException(status.HTTP_502_BAD_GATEWAY, str(e)) from e

    encrypted = TokenCipher(settings.token_encryption_key).encrypt(body.github_token)
    values = {
        "github_login": gh_user.login,
        "github_id": gh_user.id,
        "email": gh_user.email,
        "avatar_url": gh_user.avatar_url,
        "github_token_encrypted": encrypted,
    }
    stmt = (
        insert(User)
        .values(**values)
        .on_conflict_do_update(index_elements=[User.github_id], set_=values)
        .returning(User.id)
    )
    user_id = await db.scalar(stmt)
    await db.commit()
    assert user_id is not None
    token = await create_session(redis, user_id, settings.session_ttl_seconds)
    return SessionOut(
        session_token=token,
        user=UserOut(id=user_id, login=gh_user.login, avatar_url=gh_user.avatar_url),
    )


@router.get("/me", response_model=Me)
async def me(user: UserDep, settings: SettingsDep) -> Me:
    return Me(
        user=UserOut(id=user.id, login=user.login, avatar_url=user.avatar_url),
        auth_mode=settings.auth_mode,
        github_connected=bool(user.github_token),
    )


@router.post("/logout", status_code=status.HTTP_204_NO_CONTENT)
async def logout(user: UserDep, redis: RedisDep) -> None:
    if user.session_token:
        await end_session(redis, user.session_token)
