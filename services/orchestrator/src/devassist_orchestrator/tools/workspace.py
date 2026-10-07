"""A git checkout that agents edit through whole-file writes.

Agents never write diffs. They return the full new content of each file they
change, `apply` writes it, and `diff` asks git for the unified diff against
the base commit. Model-written diffs fail to apply on a miscounted line;
whole-file edits plus git cannot produce a malformed patch.
"""

from __future__ import annotations

import asyncio
import base64
import hashlib
import os
import shutil
import tempfile
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Literal

from pydantic import BaseModel, Field


class FileEdit(BaseModel):
    path: str = Field(description="Repository-relative path, forward slashes")
    action: Literal["write", "delete"] = Field(description="write = create or replace the file")
    content: str = Field(description="Full new file content for write; empty for delete")


@dataclass(frozen=True)
class DiffStats:
    files_changed: int
    additions: int
    deletions: int


class WorkspaceError(Exception):
    pass


class Workspace:
    def __init__(self, root: Path, commit_sha: str) -> None:
        self.root = root
        self.commit_sha = commit_sha

    @classmethod
    async def checkout(
        cls,
        clone_url: str,
        *,
        work_dir: str,
        commit_sha: str | None = None,
        branch: str = "main",
        github_token: str = "",
    ) -> Workspace:
        if clone_url.startswith("-") or (commit_sha and not commit_sha.isalnum()):
            raise WorkspaceError("invalid clone URL or commit")
        root = await asyncio.to_thread(_make_job_dir, work_dir)
        auth: list[str] = []
        if github_token and clone_url.startswith("https://github.com/"):
            basic = base64.b64encode(f"x-access-token:{github_token}".encode()).decode()
            auth = ["-c", f"http.https://github.com/.extraheader=Authorization: Basic {basic}"]
        ref = commit_sha or branch
        try:
            await _git(root, "init", "--quiet")
            await _git(root, "remote", "add", "origin", clone_url)
            await _git(root, *auth, "fetch", "--quiet", "--depth", "1", "--no-tags", "origin", ref)
            await _git(root, "checkout", "--quiet", "--detach", "FETCH_HEAD")
            sha = (await _git(root, "rev-parse", "HEAD")).strip()
        except WorkspaceError:
            shutil.rmtree(root, ignore_errors=True)
            raise
        return cls(root, sha)

    def cleanup(self) -> None:
        shutil.rmtree(self.root, ignore_errors=True)

    # --- reading -------------------------------------------------------------

    async def files(self) -> list[str]:
        out = await _git(self.root, "ls-files", "--cached", "--others", "--exclude-standard")
        return sorted(line for line in out.splitlines() if line)

    def read(self, path: str, max_chars: int | None = None) -> str | None:
        target = self._resolve(path)
        if not target.is_file():
            return None
        raw = target.read_bytes()
        if b"\x00" in raw[:8000]:
            return None  # binary file: nothing an agent can usefully read
        text = raw.decode(errors="replace")
        if max_chars is not None and len(text) > max_chars:
            return text[:max_chars] + f"\n... [truncated: file is {len(text)} characters]"
        return text

    def exists(self, path: str) -> bool:
        return self._resolve(path).exists()

    # --- writing -------------------------------------------------------------

    def apply(self, edits: list[FileEdit]) -> list[str]:
        """Apply edits; returns the paths touched. All paths are validated
        before anything is written, so a bad edit changes nothing."""
        targets = [(edit, self._resolve(edit.path)) for edit in edits]
        for edit, target in targets:
            if edit.action == "write":
                target.parent.mkdir(parents=True, exist_ok=True)
                content = (
                    edit.content
                    if edit.content.endswith("\n") or not edit.content
                    else edit.content + "\n"
                )
                target.write_text(content)
            elif target.exists():
                target.unlink()
        return [edit.path for edit, _ in targets]

    async def diff(self) -> str:
        """Unified diff of the working tree against the base commit,
        including new and deleted files."""
        await _git(self.root, "add", "--all")
        return await _git(self.root, "diff", "--cached", "--no-color", "--no-ext-diff", "HEAD")

    async def stats(self) -> DiffStats:
        out = await _git(self.root, "diff", "--cached", "--numstat", "HEAD")
        files = add = delete = 0
        for line in out.splitlines():
            parts = line.split("\t")
            if len(parts) == 3:
                files += 1
                add += int(parts[0]) if parts[0].isdigit() else 0
                delete += int(parts[1]) if parts[1].isdigit() else 0
        return DiffStats(files, add, delete)

    def _resolve(self, path: str) -> Path:
        """Map a repo-relative path to disk, refusing anything that could
        escape the checkout or touch git internals."""
        p = PurePosixPath(path.strip())
        if (
            not path.strip()
            or p.is_absolute()
            or ".." in p.parts
            or (p.parts and p.parts[0] == ".git")
        ):
            raise WorkspaceError(f"refusing unsafe path {path!r}")
        target = (self.root / Path(*p.parts)).resolve()
        if not target.is_relative_to(self.root.resolve()):
            raise WorkspaceError(f"refusing path outside the repository: {path!r}")
        return target


def _make_job_dir(work_dir: str) -> Path:
    Path(work_dir).mkdir(parents=True, exist_ok=True)
    return Path(tempfile.mkdtemp(prefix="job-", dir=work_dir))


def diff_fingerprint(diff: str) -> str:
    """Stable identity of a patch, for "the agent is going in circles" checks."""
    return hashlib.sha256(diff.encode()).hexdigest()


async def _git(cwd: Path, *args: str) -> str:
    env = {
        **os.environ,
        "GIT_TERMINAL_PROMPT": "0",
        "GIT_AUTHOR_NAME": "DevAssist",
        "GIT_AUTHOR_EMAIL": "devassist@localhost",
        "GIT_COMMITTER_NAME": "DevAssist",
        "GIT_COMMITTER_EMAIL": "devassist@localhost",
    }
    proc = await asyncio.create_subprocess_exec(
        "git",
        *args,
        cwd=cwd,
        env=env,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    stdout, stderr = await proc.communicate()
    if proc.returncode != 0:
        name = next((a for a in args if not a.startswith("-") and "=" not in a), args[0])
        raise WorkspaceError(f"git {name} failed: {stderr.decode().strip()}")
    return stdout.decode()
