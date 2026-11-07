"""Measuring a rendered clip for the clip rules.

ffprobe gives the streams and duration, a full decode proves playability, blackdetect
and silencedetect report runs, and the cue record written at render time is compared
with the timeline. The rules themselves live in the rules module.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

from . import rules_v1
from .ffmpeg import FFmpeg, FFmpegError
from .rules_v1 import ClipFacts, Cue, Finding
from .timeline import TimelineStore, clip_spans

_BLACK_RUN = re.compile(r"black_duration:\s*([0-9.]+)")
_SILENCE_RUN = re.compile(r"silence_duration:\s*([0-9.]+)")
_SILENCE_START = re.compile(r"silence_start:\s*([0-9.]+)")
_SILENCE_END = re.compile(r"silence_end:")
_SILENCE_NOISE_DB = "-50dB"


class ClipChecker:
    def __init__(self, ffmpeg: FFmpeg, clips_dir: Path, timelines: TimelineStore) -> None:
        self._ffmpeg = ffmpeg
        self._clips = clips_dir
        self._timelines = timelines

    def clip_path(self, clip_sha256: str) -> Path:
        return self._clips / f"{clip_sha256}.mp4"

    def check(self, clip_sha256: str) -> list[Finding]:
        path = self.clip_path(clip_sha256)
        timeline = self._load_timeline(clip_sha256)
        facts = self._measure(path, timeline)
        findings = rules_v1.check_clip(facts)
        if facts.decodable and timeline is not None:
            findings.extend(rules_v1.check_subtitles(clip_spans(timeline), self._load_cues(clip_sha256)))
        return findings

    def _measure(self, path: Path, timeline: dict | None) -> ClipFacts:
        timeline_duration = None if timeline is None else timeline["durationMs"]
        try:
            probe = self._ffmpeg.probe(path)
            # A complete decode; any decoder error is an error-level message on stderr.
            decode_log = self._ffmpeg.stderr_of(["-v", "error", "-xerror", "-i", str(path), "-f", "null", "-"])
            decodable = decode_log.strip() == ""
        except FFmpegError:
            return ClipFacts(False, False, False, None, None, False, None, timeline_duration, (), ())
        video = probe.video
        black_runs: tuple[int, ...] = ()
        silence_runs: tuple[int, ...] = ()
        if decodable and video is not None:
            black_runs = self._black_runs(path)
        if decodable and probe.audio is not None:
            silence_runs = self._silence_runs(path, probe.duration_ms)
        return ClipFacts(
            decodable=decodable,
            has_video=video is not None,
            has_audio=probe.audio is not None,
            width=None if video is None else video.width,
            height=None if video is None else video.height,
            square_pixels=video is not None and video.sample_aspect_ratio in (None, "1:1", "0:1", "N/A"),
            duration_ms=probe.duration_ms,
            timeline_duration_ms=timeline_duration,
            black_runs_ms=black_runs,
            silence_runs_ms=silence_runs,
        )

    def _black_runs(self, path: Path) -> tuple[int, ...]:
        minimum_s = rules_v1.BLACK_RUN_MAX_MS / 1000
        log = self._ffmpeg.stderr_of(
            ["-v", "info", "-i", str(path), "-an", "-vf", f"blackdetect=d={minimum_s}:pic_th=0.98", "-f", "null", "-"]
        )
        return tuple(int(round(float(m) * 1000)) for m in _BLACK_RUN.findall(log))

    def _silence_runs(self, path: Path, duration_ms: int) -> tuple[int, ...]:
        minimum_s = rules_v1.SILENCE_RUN_MAX_MS / 1000
        log = self._ffmpeg.stderr_of(
            [
                "-v",
                "info",
                "-i",
                str(path),
                "-vn",
                "-af",
                f"silencedetect=n={_SILENCE_NOISE_DB}:d={minimum_s}",
                "-f",
                "null",
                "-",
            ]
        )
        runs = [int(round(float(m) * 1000)) for m in _SILENCE_RUN.findall(log)]
        # A silence that lasts to the end of the stream is reported with a start only.
        starts = _SILENCE_START.findall(log)
        ends = len(_SILENCE_END.findall(log))
        if len(starts) > ends:
            runs.append(duration_ms - int(round(float(starts[-1]) * 1000)))
        return tuple(runs)

    def _load_timeline(self, clip_sha256: str) -> dict | None:
        cues_path = self._clips / f"{clip_sha256}.cues.json"
        if not cues_path.exists():
            return None
        record = json.loads(cues_path.read_text("utf-8"))
        return self._timelines.load(record["timelineSha256"])

    def _load_cues(self, clip_sha256: str) -> list[Cue]:
        cues_path = self._clips / f"{clip_sha256}.cues.json"
        if not cues_path.exists():
            return []
        record = json.loads(cues_path.read_text("utf-8"))
        return [Cue(c["sentence"], c["text"], c["startMs"], c["endMs"]) for c in record["cues"]]
