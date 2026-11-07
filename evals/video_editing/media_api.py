"""Client for the media service the demo uses to store source videos and clips."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import httpx


class MediaError(RuntimeError):
    pass


class MediaClient:
    def __init__(self, base_url: str, *, timeout: float = 600.0, client: httpx.Client | None = None) -> None:
        self.base_url = base_url.rstrip("/")
        self._client = client or httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._client.close()

    def upload_source(self, path: Path, crop_center_x: float) -> dict[str, Any]:
        """POST /v1/sources as multipart; returns sourceId, sha256, durationMs, mediaType.

        The service answers the same reference for identical content, which is what makes
        dataset preparation idempotent.
        """
        with path.open("rb") as handle:
            files = {"file": (path.name, handle, "video/webm")}
            data = {"cropCenterX": str(crop_center_x)}
            response = self._client.post(self.base_url + "/v1/sources", files=files, data=data)
        if response.status_code >= 400:
            raise MediaError(f"POST /v1/sources -> {response.status_code}: {response.text[:500]}")
        body = response.json()
        for key in ("sourceId", "sha256", "durationMs", "mediaType"):
            if key not in body:
                raise MediaError(f"POST /v1/sources response lacks {key!r}: {body}")
        return body

    def clip_url(self, clip_sha256: str) -> str:
        return f"{self.base_url}/v1/clips/{clip_sha256}"

    def download_clip(self, clip_sha256: str, destination: Path) -> None:
        destination.parent.mkdir(parents=True, exist_ok=True)
        tmp = destination.with_suffix(destination.suffix + ".tmp")
        with self._client.stream("GET", self.clip_url(clip_sha256)) as response:
            if response.status_code >= 400:
                raise MediaError(f"GET /v1/clips/{clip_sha256} -> {response.status_code}")
            with tmp.open("wb") as handle:
                for chunk in response.iter_bytes():
                    handle.write(chunk)
        tmp.replace(destination)
