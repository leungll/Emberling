"""Check rules, version v1.

Every check result carries RULE_VERSION. A finding has a stable code, a level and a
message; BLOCKING findings fail the check, WARNING findings are reported only. The
functions here are pure: they take facts that other modules measured and return
findings, never suggestions.

Timeline rules:
  sentence references exist and segments do not overlap     BLOCKING
  every cut point is a sentence boundary                      BLOCKING
  total duration within 10% of the target, or 30-60 s when
  no target is given                                          BLOCKING
  segments keep the original order                            WARNING

Clip rules:
  decodable with one video and one audio stream               BLOCKING
  1080x1920 square pixels                                     BLOCKING
  duration within 0.5 s of the timeline                       BLOCKING
  every chosen sentence has cues whose text comes from the
  transcript and whose times fall inside the sentence         BLOCKING
  no black run longer than 1 s                                BLOCKING
  no silent run longer than 2 s                               WARNING
"""

from __future__ import annotations

from dataclasses import dataclass

RULE_VERSION = "v1"

KIND_TIMELINE = "TIMELINE"
KIND_CLIP = "CLIP"

LEVEL_BLOCKING = "BLOCKING"
LEVEL_WARNING = "WARNING"

DEFAULT_MIN_DURATION_MS = 30_000
DEFAULT_MAX_DURATION_MS = 60_000
TARGET_TOLERANCE = 0.10

CLIP_WIDTH = 1080
CLIP_HEIGHT = 1920
CLIP_DURATION_TOLERANCE_MS = 500
BLACK_RUN_MAX_MS = 1_000
SILENCE_RUN_MAX_MS = 2_000

TIMELINE_SENTENCE_UNKNOWN = "TIMELINE_SENTENCE_UNKNOWN"
TIMELINE_SEGMENTS_OVERLAP = "TIMELINE_SEGMENTS_OVERLAP"
TIMELINE_SEGMENT_REVERSED = "TIMELINE_SEGMENT_REVERSED"
TIMELINE_CUT_OFF_BOUNDARY = "TIMELINE_CUT_OFF_BOUNDARY"
TIMELINE_DURATION_OUT_OF_RANGE = "TIMELINE_DURATION_OUT_OF_RANGE"
TIMELINE_ORDER_CHANGED = "TIMELINE_ORDER_CHANGED"

CLIP_NOT_DECODABLE = "CLIP_NOT_DECODABLE"
CLIP_MISSING_STREAM = "CLIP_MISSING_STREAM"
CLIP_WRONG_DIMENSIONS = "CLIP_WRONG_DIMENSIONS"
CLIP_DURATION_MISMATCH = "CLIP_DURATION_MISMATCH"
CLIP_TIMELINE_UNKNOWN = "CLIP_TIMELINE_UNKNOWN"
CLIP_SUBTITLE_MISSING = "CLIP_SUBTITLE_MISSING"
CLIP_SUBTITLE_TEXT_MISMATCH = "CLIP_SUBTITLE_TEXT_MISMATCH"
CLIP_SUBTITLE_TIMING = "CLIP_SUBTITLE_TIMING"
CLIP_BLACK_FRAMES = "CLIP_BLACK_FRAMES"
CLIP_SILENCE = "CLIP_SILENCE"


@dataclass(frozen=True)
class Finding:
    code: str
    level: str
    message: str
    segmentIndex: int | None = None

    def to_dict(self) -> dict:
        data = {"code": self.code, "level": self.level, "message": self.message}
        if self.segmentIndex is not None:
            data["segmentIndex"] = self.segmentIndex
        return data


def passed(findings: list[Finding]) -> bool:
    return not any(f.level == LEVEL_BLOCKING for f in findings)


@dataclass(frozen=True)
class SegmentFacts:
    """One timeline segment after sentence references were resolved."""

    index: int
    from_id: str
    to_id: str
    from_index: int | None  # position of from_id among the sentences, None when unknown
    to_index: int | None
    start_ms: int | None  # resolved start, None when the segment could not be resolved
    end_ms: int | None
    boundary_start_ms: int | None  # the sentence boundaries the cut should fall on
    boundary_end_ms: int | None


def check_timeline(segments: list[SegmentFacts], target_duration_ms: int | None) -> list[Finding]:
    findings: list[Finding] = []
    covered: dict[int, int] = {}  # sentence index -> segment index that first used it
    last_end_index: int | None = None
    order_changed = False

    for segment in segments:
        if segment.from_index is None:
            findings.append(
                Finding(
                    TIMELINE_SENTENCE_UNKNOWN,
                    LEVEL_BLOCKING,
                    f"sentence {segment.from_id} does not exist in the transcript",
                    segment.index,
                )
            )
        if segment.to_index is None and segment.to_id != segment.from_id:
            findings.append(
                Finding(
                    TIMELINE_SENTENCE_UNKNOWN,
                    LEVEL_BLOCKING,
                    f"sentence {segment.to_id} does not exist in the transcript",
                    segment.index,
                )
            )
        if segment.from_index is None or segment.to_index is None:
            continue
        if segment.to_index < segment.from_index:
            findings.append(
                Finding(
                    TIMELINE_SEGMENT_REVERSED,
                    LEVEL_BLOCKING,
                    f"segment ends at {segment.to_id} before it starts at {segment.from_id}",
                    segment.index,
                )
            )
            continue
        overlapping = [i for i in range(segment.from_index, segment.to_index + 1) if i in covered]
        if overlapping:
            findings.append(
                Finding(
                    TIMELINE_SEGMENTS_OVERLAP,
                    LEVEL_BLOCKING,
                    f"segment overlaps segment {covered[overlapping[0]]} on {len(overlapping)} sentence(s)",
                    segment.index,
                )
            )
        for i in range(segment.from_index, segment.to_index + 1):
            covered.setdefault(i, segment.index)
        if segment.start_ms != segment.boundary_start_ms or segment.end_ms != segment.boundary_end_ms:
            findings.append(
                Finding(
                    TIMELINE_CUT_OFF_BOUNDARY,
                    LEVEL_BLOCKING,
                    "segment start or end is not on a sentence boundary",
                    segment.index,
                )
            )
        if last_end_index is not None and segment.from_index <= last_end_index:
            order_changed = True
        last_end_index = segment.to_index

    if order_changed:
        findings.append(
            Finding(
                TIMELINE_ORDER_CHANGED,
                LEVEL_WARNING,
                "segments do not follow the order of the source video",
            )
        )

    total_ms = sum(
        s.end_ms - s.start_ms
        for s in segments
        if s.start_ms is not None and s.end_ms is not None and s.end_ms >= s.start_ms
    )
    if target_duration_ms is None:
        low, high = DEFAULT_MIN_DURATION_MS, DEFAULT_MAX_DURATION_MS
        expectation = f"between {low / 1000:.0f} s and {high / 1000:.0f} s"
    else:
        low = int(round(target_duration_ms * (1 - TARGET_TOLERANCE)))
        high = int(round(target_duration_ms * (1 + TARGET_TOLERANCE)))
        expectation = f"within 10% of the {target_duration_ms / 1000:.3f} s target"
    if not low <= total_ms <= high:
        findings.append(
            Finding(
                TIMELINE_DURATION_OUT_OF_RANGE,
                LEVEL_BLOCKING,
                f"total duration {total_ms / 1000:.3f} s is not {expectation}",
            )
        )
    return findings


@dataclass(frozen=True)
class ClipFacts:
    """What was measured on a rendered clip."""

    decodable: bool
    has_video: bool
    has_audio: bool
    width: int | None
    height: int | None
    square_pixels: bool
    duration_ms: int | None
    timeline_duration_ms: int | None  # None when the clip's timeline is unknown
    black_runs_ms: tuple[int, ...]
    silence_runs_ms: tuple[int, ...]


@dataclass(frozen=True)
class SentenceSpan:
    """A chosen sentence placed on the clip's own time axis."""

    id: str
    text: str
    start_ms: int
    end_ms: int


@dataclass(frozen=True)
class Cue:
    sentence: str
    text: str
    start_ms: int
    end_ms: int


def check_clip(facts: ClipFacts) -> list[Finding]:
    findings: list[Finding] = []
    if not facts.decodable:
        findings.append(Finding(CLIP_NOT_DECODABLE, LEVEL_BLOCKING, "clip cannot be fully decoded"))
        return findings
    if not facts.has_video:
        findings.append(Finding(CLIP_MISSING_STREAM, LEVEL_BLOCKING, "clip has no video stream"))
    if not facts.has_audio:
        findings.append(Finding(CLIP_MISSING_STREAM, LEVEL_BLOCKING, "clip has no audio stream"))
    if facts.has_video and (
        facts.width != CLIP_WIDTH or facts.height != CLIP_HEIGHT or not facts.square_pixels
    ):
        findings.append(
            Finding(
                CLIP_WRONG_DIMENSIONS,
                LEVEL_BLOCKING,
                f"clip is {facts.width}x{facts.height}, expected {CLIP_WIDTH}x{CLIP_HEIGHT} with square pixels",
            )
        )
    if facts.timeline_duration_ms is None:
        findings.append(
            Finding(CLIP_TIMELINE_UNKNOWN, LEVEL_BLOCKING, "clip is not linked to a timeline")
        )
    elif facts.duration_ms is None or abs(facts.duration_ms - facts.timeline_duration_ms) > CLIP_DURATION_TOLERANCE_MS:
        findings.append(
            Finding(
                CLIP_DURATION_MISMATCH,
                LEVEL_BLOCKING,
                f"clip duration {facts.duration_ms} ms differs from the timeline's {facts.timeline_duration_ms} ms by more than {CLIP_DURATION_TOLERANCE_MS} ms",
            )
        )
    for run in facts.black_runs_ms:
        if run > BLACK_RUN_MAX_MS:
            findings.append(
                Finding(CLIP_BLACK_FRAMES, LEVEL_BLOCKING, f"black run of {run} ms exceeds {BLACK_RUN_MAX_MS} ms")
            )
            break
    for run in facts.silence_runs_ms:
        if run > SILENCE_RUN_MAX_MS:
            findings.append(
                Finding(CLIP_SILENCE, LEVEL_WARNING, f"silent run of {run} ms exceeds {SILENCE_RUN_MAX_MS} ms")
            )
            break
    return findings


def check_subtitles(spans: list[SentenceSpan], cues: list[Cue]) -> list[Finding]:
    """Compares the cue data written at render time against the chosen sentences."""
    findings: list[Finding] = []
    by_sentence: dict[str, list[Cue]] = {}
    for cue in cues:
        by_sentence.setdefault(cue.sentence, []).append(cue)
    for span in spans:
        sentence_cues = by_sentence.get(span.id, [])
        if not sentence_cues:
            findings.append(
                Finding(CLIP_SUBTITLE_MISSING, LEVEL_BLOCKING, f"sentence {span.id} has no subtitle cue")
            )
            continue
        for cue in sentence_cues:
            if cue.text not in span.text:
                findings.append(
                    Finding(
                        CLIP_SUBTITLE_TEXT_MISMATCH,
                        LEVEL_BLOCKING,
                        f"a cue of sentence {span.id} has text that is not in the transcript",
                    )
                )
                break
        for cue in sentence_cues:
            if cue.start_ms < span.start_ms or cue.end_ms > span.end_ms or cue.end_ms < cue.start_ms:
                findings.append(
                    Finding(
                        CLIP_SUBTITLE_TIMING,
                        LEVEL_BLOCKING,
                        f"a cue of sentence {span.id} lies outside the sentence",
                    )
                )
                break
    return findings
