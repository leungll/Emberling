"""Transcript records, addressed by the hash of their material text.

A transcript is cached per source hash and model name: transcribing the same video with
the same model again returns the stored record. The cache index lives beside the
records so a lookup never has to scan them.
"""

from __future__ import annotations

import json
from pathlib import Path

from .storage import DataDir, sha256_text
from .transcribe import Transcript


class TranscriptStore:
    def __init__(self, data: DataDir) -> None:
        self._data = data
        self._cache_dir = data.transcripts / "cache"
        self._cache_dir.mkdir(exist_ok=True)

    def record_path(self, transcript_sha256: str) -> Path:
        return self._data.transcripts / f"{transcript_sha256}.json"

    def _cache_path(self, source_sha256: str, model: str) -> Path:
        directory = self._cache_dir / source_sha256
        directory.mkdir(exist_ok=True)
        return directory / f"{model}.json"

    def save(self, transcript: Transcript) -> str:
        """Stores the record and the cache entry; returns the transcript hash."""
        transcript_sha256 = sha256_text(transcript.material)
        self._data.write_text(
            self.record_path(transcript_sha256), json.dumps(transcript.to_dict(), sort_keys=True)
        )
        self._data.write_text(
            self._cache_path(transcript.sourceSha256, transcript.model),
            json.dumps({"transcriptSha256": transcript_sha256}),
        )
        return transcript_sha256

    def load(self, transcript_sha256: str) -> Transcript | None:
        path = self.record_path(transcript_sha256)
        if not path.exists():
            return None
        return Transcript.from_dict(json.loads(path.read_text("utf-8")))

    def cached_sha256(self, source_sha256: str, model: str) -> str | None:
        path = self._cache_path(source_sha256, model)
        if not path.exists():
            return None
        transcript_sha256 = json.loads(path.read_text("utf-8"))["transcriptSha256"]
        if not self.record_path(transcript_sha256).exists():
            return None
        return transcript_sha256
