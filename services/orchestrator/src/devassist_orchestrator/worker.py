"""The orchestrator's background worker.

Phase 1: an idle loop that proves the process lifecycle (start, run, shut
down cleanly on SIGTERM). Phase 4 replaces the body with the agent state
machine, and Phase 5 feeds it from the `job.created` topic.
"""

from __future__ import annotations

import asyncio
import logging

log = logging.getLogger(__name__)


class Worker:
    def __init__(self, idle_interval: float = 5.0) -> None:
        self.idle_interval = idle_interval
        self.running = False

    async def run(self, stop: asyncio.Event) -> None:
        self.running = True
        log.info("worker started")
        try:
            while not stop.is_set():
                try:
                    await asyncio.wait_for(stop.wait(), timeout=self.idle_interval)
                except TimeoutError:
                    continue
        finally:
            self.running = False
            log.info("worker stopped")
