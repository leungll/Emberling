"""Thin wrappers around the ffmpeg and ffprobe binaries.

Every external process is started here with a fixed argument list, bounded output and a
timeout. Nothing in this module decides what a result means; it reports the process
outcome and lets the caller apply the rules.
"""

from __future__ import annotations

import json
import subprocess
from dataclasses import dataclass
from pathlib import Path

import numpy as np

_STDERR_TAIL = 2048
_PROBE_TIMEOUT_S = 120
_DECODE_TIMEOUT_S = 1800
_RENDER_TIMEOUT_S = 3600

REQUIRED_FILTERS = ("subtitles", "blackdetect", "silencedetect")
WHISPER_SAMPLE_RATE = 16000


class FFmpegError(RuntimeError):
    """A process did not complete successfully. The message carries a bounded stderr tail."""


@dataclass(frozen=True)
class StreamInfo:
    codec_type: str
    width: int | None
    height: int | None
    sample_aspect_ratio: str | None


@dataclass(frozen=True)
class ProbeResult:
    format_name: str
    duration_ms: int
    streams: tuple[StreamInfo, ...]

    @property
    def video(self) -> StreamInfo | None:
        return next((s for s in self.streams if s.codec_type == "video"), None)

    @property
    def audio(self) -> StreamInfo | None:
        return next((s for s in self.streams if s.codec_type == "audio"), None)


class FFmpeg:
    def __init__(self, ffmpeg_bin: str, ffprobe_bin: str) -> None:
        self.ffmpeg_bin = ffmpeg_bin
        self.ffprobe_bin = ffprobe_bin

    def run(
        self,
        args: list[str],
        *,
        timeout_s: int = _RENDER_TIMEOUT_S,
        cwd: Path | None = None,
        capture_stdout: bool = False,
    ) -> subprocess.CompletedProcess[bytes]:
        """Runs ffmpeg with args. Raises FFmpegError on a non-zero exit."""
        command = [self.ffmpeg_bin, "-hide_banner", "-nostdin", *args]
        try:
            completed = subprocess.run(
                command,
                cwd=cwd,
                stdout=subprocess.PIPE if capture_stdout else subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                timeout=timeout_s,
                check=False,
            )
        except subprocess.TimeoutExpired as exc:
            raise FFmpegError(f"ffmpeg timed out after {timeout_s}s") from exc
        except OSError as exc:
            raise FFmpegError(f"ffmpeg could not start: {exc.strerror}") from exc
        if completed.returncode != 0:
            raise FFmpegError(
                f"ffmpeg exited with status {completed.returncode}: {_tail(completed.stderr)}"
            )
        return completed

    def stderr_of(self, args: list[str], *, timeout_s: int = _DECODE_TIMEOUT_S) -> str:
        """Runs ffmpeg and returns its stderr text, which analysis filters report on."""
        completed = self.run(args, timeout_s=timeout_s)
        return completed.stderr.decode("utf-8", errors="replace")

    def probe(self, path: Path) -> ProbeResult:
        command = [
            self.ffprobe_bin,
            "-v",
            "error",
            "-show_entries",
            "format=format_name,duration:stream=codec_type,width,height,sample_aspect_ratio",
            "-of",
            "json",
            str(path),
        ]
        try:
            completed = subprocess.run(
                command,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=_PROBE_TIMEOUT_S,
                check=False,
            )
        except subprocess.TimeoutExpired as exc:
            raise FFmpegError("ffprobe timed out") from exc
        except OSError as exc:
            raise FFmpegError(f"ffprobe could not start: {exc.strerror}") from exc
        if completed.returncode != 0:
            raise FFmpegError(f"ffprobe exited with status {completed.returncode}: {_tail(completed.stderr)}")
        try:
            data = json.loads(completed.stdout)
        except json.JSONDecodeError as exc:
            raise FFmpegError("ffprobe produced no JSON") from exc
        fmt = data.get("format") or {}
        duration_text = fmt.get("duration")
        if duration_text is None:
            raise FFmpegError("ffprobe reported no duration")
        streams = tuple(
            StreamInfo(
                codec_type=str(s.get("codec_type", "")),
                width=s.get("width"),
                height=s.get("height"),
                sample_aspect_ratio=s.get("sample_aspect_ratio"),
            )
            for s in data.get("streams", [])
        )
        return ProbeResult(
            format_name=str(fmt.get("format_name", "")),
            duration_ms=int(round(float(duration_text) * 1000)),
            streams=streams,
        )

    def decode_audio(self, path: Path) -> np.ndarray:
        """Decodes the audio track to mono float32 samples at the transcription rate."""
        completed = self.run(
            [
                "-v",
                "error",
                "-i",
                str(path),
                "-vn",
                "-f",
                "f32le",
                "-acodec",
                "pcm_f32le",
                "-ac",
                "1",
                "-ar",
                str(WHISPER_SAMPLE_RATE),
                "-",
            ],
            timeout_s=_DECODE_TIMEOUT_S,
            capture_stdout=True,
        )
        return np.frombuffer(completed.stdout, dtype=np.float32)

    def readiness_problems(self) -> list[str]:
        """Names what is missing for the service to work: binaries, filters, encoders."""
        problems: list[str] = []
        try:
            filters = self.stderr_and_stdout(["-filters"])
        except FFmpegError as exc:
            return [f"ffmpeg unavailable: {exc}"]
        for name in REQUIRED_FILTERS:
            if f" {name} " not in filters:
                problems.append(f"ffmpeg filter missing: {name}")
        try:
            encoders = self.stderr_and_stdout(["-encoders"])
        except FFmpegError as exc:
            return [f"ffmpeg unavailable: {exc}"]
        for name in ("libx264", "aac"):
            if f" {name} " not in encoders:
                problems.append(f"ffmpeg encoder missing: {name}")
        try:
            subprocess.run(
                [self.ffprobe_bin, "-version"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=_PROBE_TIMEOUT_S,
                check=True,
            )
        except (OSError, subprocess.SubprocessError):
            problems.append("ffprobe unavailable")
        return problems

    def stderr_and_stdout(self, args: list[str]) -> str:
        completed = self.run(args, timeout_s=_PROBE_TIMEOUT_S, capture_stdout=True)
        return completed.stdout.decode("utf-8", errors="replace") + completed.stderr.decode(
            "utf-8", errors="replace"
        )


def _tail(stderr: bytes) -> str:
    text = stderr.decode("utf-8", errors="replace").strip()
    return text[-_STDERR_TAIL:]
