"""Redis keys for live job progress, shared by the orchestrator (writer) and
the API (reader, server-sent events).

- `devassist:job:<id>:progress`  latest progress document (JSON), 24 h TTL
- `devassist:job:<id>:events`    pub/sub channel, one message per change
- `devassist:events`             pub/sub channel for coarse notifications
                                 (repo indexed, job finished) that list
                                 views use to refresh
"""

from __future__ import annotations

NOTIFICATIONS_CHANNEL = "devassist:events"


def progress_key(job_id: str) -> str:
    return f"devassist:job:{job_id}:progress"


def events_channel(job_id: str) -> str:
    return f"devassist:job:{job_id}:events"
