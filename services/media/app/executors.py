"""The four task types, each mapped from its input to its result.

An executor raises TaskFailure for a definite failure (the task becomes FAILED with that
code) and returns the result members of a SUCCEEDED task otherwise. A check that does
not pass is still a SUCCEEDED task: its findings are the result.
"""

from __future__ import annotations

from . import rules_v1
from .clipcheck import ClipChecker
from .ffmpeg import FFmpegError
from .render import RenderError, Renderer
from .sources import SourceStore
from .storage import is_sha256_hex
from .tasks import TYPE_CHECK_CLIP, TYPE_CHECK_TIMELINE, TYPE_RENDER, TYPE_TRANSCRIBE, TaskFailure
from .timeline import SENTENCE_ID_PATTERN, SegmentRequest, TimelineStore, build_timeline, canonical_json, resolve_segments
from .transcribe import Transcriber
from .transcripts import TranscriptStore

INPUT_INVALID = "INPUT_INVALID"
SOURCE_NOT_FOUND = "SOURCE_NOT_FOUND"
TIMELINE_INPUT_INVALID = "TIMELINE_INPUT_INVALID"
TIMELINE_NOT_FOUND = "TIMELINE_NOT_FOUND"
CLIP_NOT_FOUND = "CLIP_NOT_FOUND"
MODEL_UNAVAILABLE = "MODEL_UNAVAILABLE"
FFMPEG_FAILED = "FFMPEG_FAILED"
RENDER_FAILED = "RENDER_FAILED"

_SENTENCE_ID_MAX = 16


class TaskExecutors:
    def __init__(
        self,
        sources: SourceStore,
        transcriber: Transcriber,
        transcripts: TranscriptStore,
        timelines: TimelineStore,
        renderer: Renderer,
        clip_checker: ClipChecker,
    ) -> None:
        self._sources = sources
        self._transcriber = transcriber
        self._transcripts = transcripts
        self._timelines = timelines
        self._renderer = renderer
        self._clip_checker = clip_checker
        self.by_type = {
            TYPE_TRANSCRIBE: self.transcribe,
            TYPE_CHECK_TIMELINE: self.check_timeline,
            TYPE_RENDER: self.render,
            TYPE_CHECK_CLIP: self.check_clip,
        }

    def transcribe(self, task_input: dict) -> dict:
        source_id = _required_str(task_input, "sourceId")
        source = self._sources.get(source_id)
        if source is None:
            raise TaskFailure(SOURCE_NOT_FOUND, f"source {source_id} is unknown")
        model = self._transcriber.model_name
        transcript_sha256 = self._transcripts.cached_sha256(source.sha256, model)
        if transcript_sha256 is None:
            try:
                transcript = self._transcriber.transcribe(
                    self._sources.content_path(source.sha256), source.sha256, source.durationMs
                )
            except FFmpegError as exc:
                raise TaskFailure(FFMPEG_FAILED, str(exc)) from exc
            except Exception as exc:
                raise TaskFailure(MODEL_UNAVAILABLE, f"transcription failed: {type(exc).__name__}") from exc
            transcript_sha256 = self._transcripts.save(transcript)
        else:
            transcript = self._transcripts.load(transcript_sha256)
        return {
            "material": transcript.material,
            "language": transcript.language,
            "sentenceCount": len(transcript.sentences),
            "lineCount": len(transcript.sentences),
            "durationMs": transcript.durationMs,
            "sourceId": source.sourceId,
            "sourceSha256": source.sha256,
            "transcriptSha256": transcript_sha256,
            "model": model,
        }

    def check_timeline(self, task_input: dict) -> dict:
        # Every input problem of this task, including a transcript that belongs to another
        # source, is a FAILED task; only a well-formed selection is checked by the rules.
        try:
            source_id = _required_str(task_input, "sourceId")
            transcript_sha256 = _required_str(task_input, "transcriptSha256")
            requests = _segment_requests(task_input.get("segments"))
        except TaskFailure as failure:
            raise TaskFailure(TIMELINE_INPUT_INVALID, failure.message) from failure
        target = task_input.get("targetDurationMs")
        if target is not None and (not isinstance(target, int) or isinstance(target, bool) or target <= 0):
            raise TaskFailure(TIMELINE_INPUT_INVALID, "targetDurationMs must be a positive integer or null")
        source = self._sources.get(source_id)
        if source is None:
            raise TaskFailure(TIMELINE_INPUT_INVALID, f"source {source_id} is unknown")
        if not is_sha256_hex(transcript_sha256):
            raise TaskFailure(TIMELINE_INPUT_INVALID, "transcriptSha256 must be a hex SHA-256")
        transcript = self._transcripts.load(transcript_sha256)
        if transcript is None:
            raise TaskFailure(TIMELINE_INPUT_INVALID, f"transcript {transcript_sha256} is unknown")
        if transcript.sourceSha256 != source.sha256:
            raise TaskFailure(
                TIMELINE_INPUT_INVALID, f"transcript {transcript_sha256} does not belong to source {source_id}"
            )
        segments = resolve_segments(transcript, requests)
        findings = rules_v1.check_timeline(segments, target)
        timeline = build_timeline(source, transcript_sha256, transcript, segments, target)
        text = canonical_json(timeline)
        timeline_sha256 = self._timelines.save(text)
        return {
            "kind": rules_v1.KIND_TIMELINE,
            "ruleVersion": rules_v1.RULE_VERSION,
            "passed": rules_v1.passed(findings),
            "subjectSha256": timeline_sha256,
            "timelineSha256": timeline_sha256,
            "timeline": text,
            "durationMs": timeline["durationMs"],
            "findings": [f.to_dict() for f in findings],
        }

    def render(self, task_input: dict) -> dict:
        timeline_sha256 = _required_str(task_input, "timelineSha256")
        if not is_sha256_hex(timeline_sha256):
            raise TaskFailure(INPUT_INVALID, "timelineSha256 must be a hex SHA-256")
        if self._timelines.load(timeline_sha256) is None:
            raise TaskFailure(TIMELINE_NOT_FOUND, f"timeline {timeline_sha256} is unknown")
        try:
            return self._renderer.render(timeline_sha256).to_dict()
        except RenderError as exc:
            raise TaskFailure(RENDER_FAILED, str(exc)) from exc
        except FFmpegError as exc:
            raise TaskFailure(FFMPEG_FAILED, str(exc)) from exc

    def check_clip(self, task_input: dict) -> dict:
        clip_sha256 = _required_str(task_input, "clipSha256")
        if not is_sha256_hex(clip_sha256):
            raise TaskFailure(INPUT_INVALID, "clipSha256 must be a hex SHA-256")
        if not self._clip_checker.clip_path(clip_sha256).exists():
            raise TaskFailure(CLIP_NOT_FOUND, f"clip {clip_sha256} is unknown")
        try:
            findings = self._clip_checker.check(clip_sha256)
        except FFmpegError as exc:
            raise TaskFailure(FFMPEG_FAILED, str(exc)) from exc
        return {
            "kind": rules_v1.KIND_CLIP,
            "ruleVersion": rules_v1.RULE_VERSION,
            "passed": rules_v1.passed(findings),
            "subjectSha256": clip_sha256,
            "timelineSha256": self._clip_checker.timeline_sha256(clip_sha256),
            "findings": [f.to_dict() for f in findings],
        }


def _required_str(task_input: dict, key: str) -> str:
    value = task_input.get(key)
    if not isinstance(value, str) or not value:
        raise TaskFailure(INPUT_INVALID, f"{key} is required")
    return value


def _segment_requests(value: object) -> list[SegmentRequest]:
    if not isinstance(value, list):
        raise TaskFailure(INPUT_INVALID, "segments must be a list")
    requests: list[SegmentRequest] = []
    for entry in value:
        if not isinstance(entry, dict):
            raise TaskFailure(INPUT_INVALID, "each segment must be an object with from and to")
        from_id, to_id = entry.get("from"), entry.get("to")
        for ref in (from_id, to_id):
            if not isinstance(ref, str) or len(ref) > _SENTENCE_ID_MAX or not SENTENCE_ID_PATTERN.match(ref):
                raise TaskFailure(INPUT_INVALID, "segment from and to must be sentence ids such as S12")
        requests.append(SegmentRequest(from_id=from_id, to_id=to_id))
    return requests
