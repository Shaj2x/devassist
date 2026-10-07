from __future__ import annotations

import asyncio
from pathlib import Path

import pytest
from devassist_orchestrator.tools.workspace import (
    FileEdit,
    Workspace,
    WorkspaceError,
    diff_fingerprint,
)


async def test_edits_become_a_git_applicable_diff(repo_url: str, tmp_path: Path) -> None:
    ws = await Workspace.checkout(repo_url, work_dir=str(tmp_path / "work"))
    assert await ws.files() == ["app/math.py", "tests/test_math.py"]

    ws.apply(
        [
            FileEdit(
                path="app/math.py", action="write", content="def add(a, b):\n    return a + b"
            ),
            FileEdit(path="app/util.py", action="write", content="X = 1\n"),
            FileEdit(path="tests/test_math.py", action="delete", content=""),
        ]
    )
    diff = await ws.diff()
    stats = await ws.stats()
    assert "+    return a + b" in diff and "new file mode" in diff and "deleted file mode" in diff
    assert (stats.files_changed, stats.additions) == (3, 2)
    assert ws.read("app/math.py", None) == "def add(a, b):\n    return a + b\n"  # newline added

    # The diff must apply cleanly to a fresh checkout of the base commit.
    fresh = await Workspace.checkout(repo_url, work_dir=str(tmp_path / "work"))
    check = await asyncio.create_subprocess_exec(
        "git", "apply", "--check", "-", cwd=fresh.root, stdin=asyncio.subprocess.PIPE
    )
    await check.communicate(diff.encode())
    assert check.returncode == 0
    ws.cleanup()
    fresh.cleanup()


@pytest.mark.parametrize("path", ["/etc/passwd", "../outside.py", "a/../../x", ".git/config", ""])
async def test_unsafe_paths_are_rejected_before_any_write(
    repo_url: str, tmp_path: Path, path: str
) -> None:
    ws = await Workspace.checkout(repo_url, work_dir=str(tmp_path / "work"))
    with pytest.raises(WorkspaceError):
        ws.apply(
            [
                FileEdit(path="app/ok.py", action="write", content="x"),
                FileEdit(path=path, action="write", content="x"),
            ]
        )
    assert not (ws.root / "app" / "ok.py").exists()


async def test_binary_files_are_not_read(repo_url: str, tmp_path: Path) -> None:
    ws = await Workspace.checkout(repo_url, work_dir=str(tmp_path / "work"))
    (ws.root / "blob.bin").write_bytes(b"\x00\x01\x02")
    assert ws.read("blob.bin") is None and ws.exists("blob.bin")


async def test_bad_checkout(tmp_path: Path) -> None:
    with pytest.raises(WorkspaceError):
        await Workspace.checkout("file:///nope", work_dir=str(tmp_path))
    assert diff_fingerprint("a") != diff_fingerprint("b")
