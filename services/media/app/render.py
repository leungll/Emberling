"""Rendering a timeline into a 9:16 clip with burned-in subtitles.

The render is a pure function of the timeline: the segments it names, the transcript's
word timestamps and the crop centre it recorded. FFmpeg runs with one thread, bit-exact
flags and no timestamp metadata, so the same timeline produces the same bytes in the
same environment. The subtitle cues are kept beside the clip for the clip check.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from pathlib import Path

from .ffmpeg import FFmpeg
from .rules_v1 import Cue
from .sources import SourceStore
from .storage import DataDir, sha256_file
from .timeline import TimelineStore
from .transcribe import Sentence
from .transcripts import TranscriptStore

CLIP_MEDIA_TYPE = "video/mp4"
MAX_WORDS_PER_CUE = 7
_ASS_FILE = "cues.ass"
_OUTPUT_FILE = "clip.mp4"

_ASS_HEADER = """[Script Info]
ScriptType: v4.00+
PlayResX: 1080
PlayResY: 1920
WrapStyle: 0
ScaledBorderAndShadow: yes

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,DejaVu Sans,64,&H00FFFFFF,&H00FFFFFF,&H00000000,&H80000000,-1,0,0,0,100,100,0,0,1,4,0,2,60,60,300,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
"""


class RenderError(RuntimeError):
    """The timeline cannot be rendered; the message names the missing fact."""


@dataclass(frozen=True)
class RenderResult:
    clipSha256: str
    clipUrl: str
    sizeBytes: int
    mediaType: str
    durationMs: int
    timelineSha256: str

    def to_dict(self) -> dict:
        return asdict(self)


def build_cues(timeline: dict, sentences_by_id: dict[str, Sentence]) -> list[Cue]:
    """Cues of at most MAX_WORDS_PER_CUE words, timed by the words, on the clip's axis."""
    cues: list[Cue] = []
    offset = 0
    for segment in timeline["segments"]:
        if segment["startMs"] is None or segment["endMs"] is None:
            continue
        for entry in segment["sentences"]:
            sentence = sentences_by_id[entry["id"]]
            words = list(sentence.words)
            for start in range(0, len(words), MAX_WORDS_PER_CUE):
                group = words[start : start + MAX_WORDS_PER_CUE]
                cues.append(
                    Cue(
                        sentence=sentence.id,
                        text=" ".join(w.text for w in group),
                        start_ms=offset + group[0].startMs - segment["startMs"],
                        end_ms=offset + group[-1].endMs - segment["startMs"],
                    )
                )
        offset += segment["endMs"] - segment["startMs"]
    return cues


def ass_document(cues: list[Cue]) -> str:
    lines = [_ASS_HEADER]
    for cue in cues:
        lines.append(
            f"Dialogue: 0,{_ass_time(cue.start_ms)},{_ass_time(cue.end_ms)},Default,,0,0,0,,{_ass_text(cue.text)}\n"
        )
    return "".join(lines)


def _ass_time(ms: int) -> str:
    centis = ms // 10
    hours, rest = divmod(centis, 360_000)
    minutes, rest = divmod(rest, 6_000)
    seconds, centis = divmod(rest, 100)
    return f"{hours}:{minutes:02d}:{seconds:02d}.{centis:02d}"


def _ass_text(text: str) -> str:
    return text.replace("\\", "\\\\").replace("{", "\\{").replace("}", "\\}").replace("\n", " ")


def filter_graph(timeline: dict) -> str:
    """The ffmpeg filter graph: trim and concatenate segments, crop, scale, subtitle."""
    parts: list[str] = []
    labels: list[str] = []
    for index, segment in enumerate(timeline["segments"]):
        start = segment["startMs"] / 1000
        end = segment["endMs"] / 1000
        parts.append(f"[0:v]trim=start={start:.3f}:end={end:.3f},setpts=PTS-STARTPTS[v{index}]")
        parts.append(
            f"[0:a]atrim=start={start:.3f}:end={end:.3f},asetpts=PTS-STARTPTS,"
            f"aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo[a{index}]"
        )
        labels.append(f"[v{index}][a{index}]")
    count = len(labels)
    parts.append(f"{''.join(labels)}concat=n={count}:v=1:a=1[vc][ac]")
    cx = timeline["cropCenterX"]
    parts.append(
        f"[vc]crop=w='min(iw,ih*9/16)':h=ih:x='min(max({cx}*iw-ow/2,0),iw-ow)':y=0,"
        f"scale=1080:1920:flags=bicubic,setsar=1,subtitles={_ASS_FILE}[vout]"
    )
    return ";".join(parts)


class Renderer:
    def __init__(
        self,
        data: DataDir,
        ffmpeg: FFmpeg,
        sources: SourceStore,
        transcripts: TranscriptStore,
        timelines: TimelineStore,
        public_url: str,
    ) -> None:
        self._data = data
        self._ffmpeg = ffmpeg
        self._sources = sources
        self._transcripts = transcripts
        self._timelines = timelines
        self._public_url = public_url

    def clip_path(self, clip_sha256: str) -> Path:
        return self._data.clips / f"{clip_sha256}.mp4"

    def cues_path(self, clip_sha256: str) -> Path:
        return self._data.clips / f"{clip_sha256}.cues.json"

    def clip_url(self, clip_sha256: str) -> str:
        return f"{self._public_url}/v1/clips/{clip_sha256}"

    def render(self, timeline_sha256: str) -> RenderResult:
        timeline = self._timelines.load(timeline_sha256)
        if timeline is None:
            raise RenderError(f"timeline {timeline_sha256} is unknown")
        if any(s["startMs"] is None or s["endMs"] is None for s in timeline["segments"]):
            raise RenderError("timeline has unresolved segments and cannot be rendered")
        if not timeline["segments"]:
            raise RenderError("timeline has no segments")
        source = self._sources.get_by_sha256(timeline["sourceSha256"])
        if source is None:
            raise RenderError(f"source {timeline['sourceSha256']} is unknown")
        transcript = self._transcripts.load(timeline["transcriptSha256"])
        if transcript is None:
            raise RenderError(f"transcript {timeline['transcriptSha256']} is unknown")
        sentences_by_id = {s.id: s for s in transcript.sentences}
        cues = build_cues(timeline, sentences_by_id)

        with self._data.temp_dir() as work:
            (work / _ASS_FILE).write_text(ass_document(cues), encoding="utf-8")
            output = work / _OUTPUT_FILE
            self._ffmpeg.run(
                [
                    "-y",
                    "-v",
                    "error",
                    "-threads",
                    "1",
                    "-filter_threads",
                    "1",
                    "-filter_complex_threads",
                    "1",
                    "-i",
                    str(self._sources.content_path(source.sha256)),
                    "-filter_complex",
                    filter_graph(timeline),
                    "-map",
                    "[vout]",
                    "-map",
                    "[ac]",
                    "-fps_mode",
                    "cfr",
                    "-c:v",
                    "libx264",
                    "-preset",
                    "medium",
                    "-crf",
                    "20",
                    "-pix_fmt",
                    "yuv420p",
                    "-threads",
                    "1",
                    "-c:a",
                    "aac",
                    "-b:a",
                    "128k",
                    "-map_metadata",
                    "-1",
                    "-map_chapters",
                    "-1",
                    "-fflags",
                    "+bitexact",
                    "-flags",
                    "+bitexact",
                    "-movflags",
                    "+faststart",
                    _OUTPUT_FILE,
                ],
                cwd=work,
            )
            clip_sha256 = sha256_file(output)
            size_bytes = output.stat().st_size
            duration_ms = self._ffmpeg.probe(output).duration_ms
            # The clip is renamed into place before its cue record is written, so a cue
            # record never describes a clip that does not exist.
            with self._data.atomic_file(self.clip_path(clip_sha256), ".mp4") as temp:
                output.replace(temp)
        self._data.write_text(
            self.cues_path(clip_sha256),
            json.dumps(
                {
                    "timelineSha256": timeline_sha256,
                    "cues": [
                        {"sentence": c.sentence, "text": c.text, "startMs": c.start_ms, "endMs": c.end_ms}
                        for c in cues
                    ],
                },
                sort_keys=True,
            ),
        )
        return RenderResult(
            clipSha256=clip_sha256,
            clipUrl=self.clip_url(clip_sha256),
            sizeBytes=size_bytes,
            mediaType=CLIP_MEDIA_TYPE,
            durationMs=duration_ms,
            timelineSha256=timeline_sha256,
        )
