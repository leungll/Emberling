"""Crash-safe writes: an interrupted write leaves nothing behind."""

from __future__ import annotations

import hashlib
from pathlib import Path

import pytest

from app.storage import DataDir, sha256_text


def test_interrupted_write_leaves_no_partial_file(tmp_path: Path) -> None:
    data = DataDir(tmp_path)
    target = data.timelines / "abc.json"

    with pytest.raises(RuntimeError, match="disk went away"):
        with data.atomic_file(target) as temp:
            temp.write_bytes(b'{"half":')
            raise RuntimeError("disk went away")

    assert not target.exists()
    assert list(data.tmp.iterdir()) == []


def test_interrupted_write_keeps_the_previous_file(tmp_path: Path) -> None:
    data = DataDir(tmp_path)
    target = data.tasks / "t.json"
    data.write_text(target, "complete")

    with pytest.raises(RuntimeError):
        with data.atomic_file(target) as temp:
            temp.write_text("part")
            raise RuntimeError("crash")

    assert target.read_text() == "complete"


def test_write_text_is_visible_only_when_complete(tmp_path: Path) -> None:
    data = DataDir(tmp_path)
    target = data.clips / "x.cues.json"
    data.write_text(target, "payload")
    assert target.read_text() == "payload"
    assert list(data.tmp.iterdir()) == []


def test_sweep_tmp_removes_leftovers_from_an_earlier_process(tmp_path: Path) -> None:
    data = DataDir(tmp_path)
    (data.tmp / "stale.upload").write_bytes(b"x")
    (data.tmp / "work").mkdir()
    (data.tmp / "work" / "clip.mp4").write_bytes(b"y")

    assert DataDir(tmp_path).sweep_tmp() == 2
    assert list(data.tmp.iterdir()) == []


def test_sha256_text_hashes_utf8_bytes() -> None:
    assert sha256_text("héllo") == hashlib.sha256("héllo".encode("utf-8")).hexdigest()
