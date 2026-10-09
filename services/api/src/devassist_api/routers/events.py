"""A server-sent event stream of coarse notifications (a repository finished
indexing, a job finished) so list views refresh without polling."""

from __future__ import annotations

from collections.abc import AsyncIterator

from devassist_common.progress import NOTIFICATIONS_CHANNEL
from fastapi import APIRouter, Request
from fastapi.responses import StreamingResponse
from redis.asyncio import Redis

from devassist_api.deps import RedisDep, UserDep
from devassist_api.routers.jobs import relay, sse

router = APIRouter(prefix="/v1", tags=["events"])


@router.get("/events")
async def notifications(request: Request, redis: RedisDep, user: UserDep) -> StreamingResponse:
    return StreamingResponse(
        notification_stream(redis, request, str(user.id)),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


async def notification_stream(redis: Redis, request: Request, user_id: str) -> AsyncIterator[str]:
    pubsub = redis.pubsub()
    await pubsub.subscribe(NOTIFICATIONS_CHANNEL)
    try:
        async for data in relay(pubsub, request):
            # Only ids and statuses travel on this channel; the dashboard
            # refetches through the authorised endpoints.
            yield ": keepalive\n\n" if data is None else sse("notification", data)
    finally:
        await pubsub.unsubscribe()
        await pubsub.aclose()  # type: ignore[no-untyped-call]
