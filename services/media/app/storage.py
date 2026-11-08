"""Data directory layout and crash-safe writes.

Every file the service keeps is written to a temporary file in the same directory tree
and then renamed into place, so a crash mid-write leaves either the complete previous
file or nothing: a reader never sees a partial source, transcript, timeline, clip or
task record.
"""

from __future__ import annotations

import hashlib
import os
import uuid
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

SHA256_HEX_LENGTH = 64
_READ_CHUNK = 1 << 20


class DataDir:
    """The directories under the data root. Each kind of fact has its own directory."""

    def __init__(self, root: Path) -> None:
        self.root = root
        self.sources = root / "sources"
        self.transcripts = root / "transcripts"
        self.timelines = root / "timelines"
        self.clips = root / "clips"
        self.tasks = root / "tasks"
        self.tmp = root / "tmp"
        for directory in (
            self.sources,
            self.transcripts,
            self.timelines,
            self.clips,
            self.tasks,
            self.tmp,
        ):
            directory.mkdir(parents=True, exist_ok=True)

    def sweep_tmp(self) -> int:
        """Removes temporary files left by an earlier process. Returns how many."""
        removed = 0
        for entry in self.tmp.iterdir():
            if entry.is_dir():
                for child in entry.rglob("*"):
                    if child.is_file():
                        child.unlink()
                for child in sorted(entry.rglob("*"), reverse=True):
                    child.rmdir()
                entry.rmdir()
            else:
                entry.unlink()
            removed += 1
        return removed

    def temp_path(self, suffix: str = "") -> Path:
        return self.tmp / f"{uuid.uuid4().hex}{suffix}"

    @contextmanager
    def temp_dir(self) -> Iterator[Path]:
        """A scratch directory removed when the block ends, however it ends."""
        path = self.tmp / uuid.uuid4().hex
        path.mkdir()
        try:
            yield path
        finally:
            for child in sorted(path.rglob("*"), reverse=True):
                if child.is_file():
                    child.unlink()
                else:
                    child.rmdir()
            path.rmdir()

    @contextmanager
    def atomic_file(self, target: Path, suffix: str = "") -> Iterator[Path]:
        """Yields a temporary path; on normal exit it is renamed over target.

        On an exception the temporary file is removed and target is untouched.
        """
        temp = self.temp_path(suffix)
        try:
            yield temp
            if not temp.exists():
                raise FileNotFoundError(f"atomic write produced no file for {target.name}")
            os.replace(temp, target)
        finally:
            if temp.exists():
                temp.unlink()

    def write_bytes(self, target: Path, data: bytes) -> None:
        with self.atomic_file(target) as temp:
            with open(temp, "wb") as handle:
                handle.write(data)
                handle.flush()
                os.fsync(handle.fileno())

    def write_text(self, target: Path, text: str) -> None:
        self.write_bytes(target, text.encode("utf-8"))


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            chunk = handle.read(_READ_CHUNK)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def is_sha256_hex(value: str) -> bool:
    return len(value) == SHA256_HEX_LENGTH and all(c in "0123456789abcdef" for c in value)
