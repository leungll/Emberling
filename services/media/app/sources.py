"""Source videos, stored by content hash.

A source is identified by the SHA-256 of its bytes, so uploading the same file twice
yields the same reference. The crop centre is the one per-source rendering setting; the
most recent upload that states it wins, and a timeline records the value it was
resolved with so an earlier timeline still renders the same clip.
"""

from __future__ import annotations

import json
import shutil
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import BinaryIO

from .ffmpeg import FFmpeg, FFmpegError
from .storage import DataDir, sha256_file

SOURCE_ID_PREFIX = "src_"
SOURCE_ID_HASH_CHARS = 32
DEFAULT_CROP_CENTER_X = 0.5

# Container names ffprobe reports, mapped to the media type the reference carries.
_MEDIA_TYPES = {
    "matroska,webm": "video/webm",
    "mov,mp4,m4a,3gp,3g2,mj2": "video/mp4",
}


class SourceRejected(ValueError):
    """The uploaded file is not a usable source video."""


@dataclass(frozen=True)
class SourceRecord:
    sourceId: str
    sha256: str
    durationMs: int
    mediaType: str
    cropCenterX: float

    def to_dict(self) -> dict:
        return asdict(self)


def source_id_for(sha256: str) -> str:
    return SOURCE_ID_PREFIX + sha256[:SOURCE_ID_HASH_CHARS]


class SourceStore:
    def __init__(self, data: DataDir, ffmpeg: FFmpeg) -> None:
        self._data = data
        self._ffmpeg = ffmpeg

    def content_path(self, sha256: str) -> Path:
        return self._data.sources / sha256

    def _record_path(self, sha256: str) -> Path:
        return self._data.sources / f"{sha256}.json"

    def save_upload(
        self,
        stream: BinaryIO,
        declared_media_type: str | None,
        crop_center_x: float | None,
    ) -> SourceRecord:
        """Stores an uploaded video and returns its reference, reusing an existing one."""
        temp = self._data.temp_path(".upload")
        try:
            with open(temp, "wb") as handle:
                shutil.copyfileobj(stream, handle)
            sha256 = sha256_file(temp)
            existing = self.get_by_sha256(sha256)
            if existing is not None:
                record = existing
                if crop_center_x is not None and crop_center_x != existing.cropCenterX:
                    record = SourceRecord(**{**existing.to_dict(), "cropCenterX": crop_center_x})
                    self._write_record(record)
                # The temporary content is discarded: the stored bytes are identical.
                return record
            try:
                probe = self._ffmpeg.probe(temp)
            except FFmpegError as exc:
                raise SourceRejected(f"file is not a decodable video: {exc}") from exc
            if probe.video is None or probe.audio is None:
                raise SourceRejected("source must contain one video stream and one audio stream")
            record = SourceRecord(
                sourceId=source_id_for(sha256),
                sha256=sha256,
                durationMs=probe.duration_ms,
                mediaType=_media_type(probe.format_name, declared_media_type),
                cropCenterX=DEFAULT_CROP_CENTER_X if crop_center_x is None else crop_center_x,
            )
            # Content first, record second: a record always points at complete bytes.
            temp.replace(self.content_path(sha256))
            self._write_record(record)
            return record
        finally:
            if temp.exists():
                temp.unlink()

    def get_by_sha256(self, sha256: str) -> SourceRecord | None:
        path = self._record_path(sha256)
        if not path.exists():
            return None
        return SourceRecord(**json.loads(path.read_text("utf-8")))

    def get(self, source_id: str) -> SourceRecord | None:
        if not source_id.startswith(SOURCE_ID_PREFIX):
            return None
        prefix = source_id[len(SOURCE_ID_PREFIX) :]
        if len(prefix) != SOURCE_ID_HASH_CHARS:
            return None
        for path in self._data.sources.glob(f"{prefix}*.json"):
            record = SourceRecord(**json.loads(path.read_text("utf-8")))
            if record.sourceId == source_id:
                return record
        return None

    def _write_record(self, record: SourceRecord) -> None:
        self._data.write_text(
            self._record_path(record.sha256), json.dumps(record.to_dict(), sort_keys=True)
        )


def _media_type(format_name: str, declared: str | None) -> str:
    if format_name in _MEDIA_TYPES:
        return _MEDIA_TYPES[format_name]
    if declared and declared.startswith("video/"):
        return declared
    return "video/" + format_name.split(",")[0]
