"""Service configuration read from the environment once at startup."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Mapping

DEFAULT_DATA_DIR = "/data"
DEFAULT_WHISPER_MODEL = "small.en"
DEFAULT_PORT = 9201


@dataclass(frozen=True)
class Settings:
    data_dir: Path
    whisper_model: str
    ffmpeg_bin: str
    ffprobe_bin: str
    port: int
    public_url: str

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> "Settings":
        env = os.environ if env is None else env
        port_text = env.get("MEDIA_PORT", str(DEFAULT_PORT))
        try:
            port = int(port_text)
        except ValueError as exc:
            raise ValueError(f"MEDIA_PORT must be an integer, got {port_text!r}") from exc
        public_url = env.get("MEDIA_PUBLIC_URL", f"http://localhost:{port}").rstrip("/")
        return cls(
            data_dir=Path(env.get("MEDIA_DATA_DIR", DEFAULT_DATA_DIR)),
            whisper_model=env.get("MEDIA_WHISPER_MODEL", DEFAULT_WHISPER_MODEL),
            ffmpeg_bin=env.get("MEDIA_FFMPEG_BIN", "ffmpeg"),
            ffprobe_bin=env.get("MEDIA_FFPROBE_BIN", "ffprobe"),
            port=port,
            public_url=public_url,
        )
