"""Pure rule decisions, independent of ffmpeg."""

from __future__ import annotations

from app import rules_v1
from app.rules_v1 import ClipFacts, Cue, SegmentFacts, SentenceSpan


def segment(index: int, from_index: int | None, to_index: int | None, start: int | None, end: int | None) -> SegmentFacts:
    from_id = "S?" if from_index is None else f"S{from_index + 1}"
    to_id = "S?" if to_index is None else f"S{to_index + 1}"
    return SegmentFacts(index, from_id, to_id, from_index, to_index, start, end, start, end)


def codes(findings) -> list[str]:
    return [f.code for f in findings]


def test_check_timeline_unknown_sentence_is_blocking() -> None:
    findings = rules_v1.check_timeline([segment(0, None, None, None, None)], 10_000)
    assert rules_v1.TIMELINE_SENTENCE_UNKNOWN in codes(findings)
    assert findings[0].segmentIndex == 0
    assert not rules_v1.passed(findings)


def test_check_timeline_overlapping_segments_is_blocking() -> None:
    findings = rules_v1.check_timeline(
        [segment(0, 0, 2, 0, 30_000), segment(1, 2, 3, 20_000, 40_000)], 40_000
    )
    overlap = [f for f in findings if f.code == rules_v1.TIMELINE_SEGMENTS_OVERLAP]
    assert len(overlap) == 1 and overlap[0].segmentIndex == 1
    assert rules_v1.TIMELINE_ORDER_CHANGED in codes(findings)


def test_check_timeline_reversed_segment_is_blocking() -> None:
    findings = rules_v1.check_timeline([segment(0, 3, 1, None, None)], None)
    assert rules_v1.TIMELINE_SEGMENT_REVERSED in codes(findings)


def test_check_timeline_cut_off_boundary_is_blocking() -> None:
    off = SegmentFacts(0, "S1", "S1", 0, 0, 100, 40_000, 0, 40_000)
    findings = rules_v1.check_timeline([off], None)
    assert rules_v1.TIMELINE_CUT_OFF_BOUNDARY in codes(findings)


def test_check_timeline_duration_default_range() -> None:
    short = rules_v1.check_timeline([segment(0, 0, 0, 0, 29_999)], None)
    assert codes(short) == [rules_v1.TIMELINE_DURATION_OUT_OF_RANGE]
    assert short[0].level == rules_v1.LEVEL_BLOCKING
    assert rules_v1.check_timeline([segment(0, 0, 0, 0, 30_000)], None) == []
    assert rules_v1.check_timeline([segment(0, 0, 0, 0, 60_000)], None) == []
    assert codes(rules_v1.check_timeline([segment(0, 0, 0, 0, 60_001)], None)) == [
        rules_v1.TIMELINE_DURATION_OUT_OF_RANGE
    ]


def test_check_timeline_duration_target_tolerance() -> None:
    assert rules_v1.check_timeline([segment(0, 0, 0, 0, 45_000)], 50_000) == []
    assert rules_v1.check_timeline([segment(0, 0, 0, 0, 55_000)], 50_000) == []
    assert codes(rules_v1.check_timeline([segment(0, 0, 0, 0, 44_999)], 50_000)) == [
        rules_v1.TIMELINE_DURATION_OUT_OF_RANGE
    ]


def test_check_timeline_order_changed_is_a_warning_only() -> None:
    findings = rules_v1.check_timeline(
        [segment(0, 2, 2, 20_000, 40_000), segment(1, 0, 0, 0, 20_000)], 40_000
    )
    assert codes(findings) == [rules_v1.TIMELINE_ORDER_CHANGED]
    assert findings[0].level == rules_v1.LEVEL_WARNING
    assert rules_v1.passed(findings)


def clip_facts(**overrides) -> ClipFacts:
    base = dict(
        decodable=True,
        has_video=True,
        has_audio=True,
        width=1080,
        height=1920,
        square_pixels=True,
        duration_ms=40_000,
        timeline_duration_ms=40_000,
        black_runs_ms=(),
        silence_runs_ms=(),
    )
    base.update(overrides)
    return ClipFacts(**base)


def test_check_clip_passes_a_conforming_clip() -> None:
    assert rules_v1.check_clip(clip_facts()) == []


def test_check_clip_not_decodable_stops_further_checks() -> None:
    findings = rules_v1.check_clip(clip_facts(decodable=False, has_video=False))
    assert codes(findings) == [rules_v1.CLIP_NOT_DECODABLE]


def test_check_clip_dimension_duration_black_and_silence() -> None:
    findings = rules_v1.check_clip(
        clip_facts(
            width=720,
            height=1280,
            duration_ms=40_501,
            black_runs_ms=(1_001,),
            silence_runs_ms=(2_001,),
            has_audio=False,
        )
    )
    assert codes(findings) == [
        rules_v1.CLIP_MISSING_STREAM,
        rules_v1.CLIP_WRONG_DIMENSIONS,
        rules_v1.CLIP_DURATION_MISMATCH,
        rules_v1.CLIP_BLACK_FRAMES,
        rules_v1.CLIP_SILENCE,
    ]
    levels = {f.code: f.level for f in findings}
    assert levels[rules_v1.CLIP_SILENCE] == rules_v1.LEVEL_WARNING
    assert levels[rules_v1.CLIP_BLACK_FRAMES] == rules_v1.LEVEL_BLOCKING


def test_check_clip_boundaries_are_inclusive() -> None:
    assert rules_v1.check_clip(clip_facts(duration_ms=40_500, black_runs_ms=(1_000,), silence_runs_ms=(2_000,))) == []


def test_check_clip_unknown_timeline_is_blocking() -> None:
    assert codes(rules_v1.check_clip(clip_facts(timeline_duration_ms=None))) == [rules_v1.CLIP_TIMELINE_UNKNOWN]


def test_check_subtitles_missing_text_and_timing() -> None:
    spans = [SentenceSpan("S1", "one two three", 0, 3_000), SentenceSpan("S2", "four five", 3_000, 5_000), SentenceSpan("S3", "six", 5_000, 6_000)]
    cues = [
        Cue("S1", "one two", 0, 1_500),
        Cue("S1", "seven", 1_500, 3_000),
        Cue("S2", "four five", 2_900, 5_000),
    ]
    findings = rules_v1.check_subtitles(spans, cues)
    assert codes(findings) == [
        rules_v1.CLIP_SUBTITLE_TEXT_MISMATCH,
        rules_v1.CLIP_SUBTITLE_TIMING,
        rules_v1.CLIP_SUBTITLE_MISSING,
    ]


def test_check_subtitles_accepts_matching_cues() -> None:
    spans = [SentenceSpan("S1", "one two three", 100, 3_000)]
    assert rules_v1.check_subtitles(spans, [Cue("S1", "one two three", 100, 3_000)]) == []
