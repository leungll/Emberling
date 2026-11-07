"""Timelines: sentence selections resolved to exact times, stored by content hash.

A timeline is canonical JSON: sorted keys, no insignificant whitespace, integer
milliseconds. The same selection on the same transcript and source always produces the
same bytes, and the hash of those bytes is the timeline's identity.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from .rules_v1 import SegmentFacts, SentenceSpan
from .sources import SourceRecord
from .storage import DataDir, sha256_text
from .transcribe import Transcript

CROP_CENTER_DECIMALS = 4


@dataclass(frozen=True)
class SegmentRequest:
    from_id: str
    to_id: str


def canonical_json(value: object) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False)


def resolve_segments(transcript: Transcript, requests: list[SegmentRequest]) -> list[SegmentFacts]:
    """Looks each sentence reference up and derives the segment's times from boundaries."""
    index_of = {s.id: i for i, s in enumerate(transcript.sentences)}
    facts: list[SegmentFacts] = []
    for index, request in enumerate(requests):
        from_index = index_of.get(request.from_id)
        to_index = index_of.get(request.to_id)
        start_ms = end_ms = None
        if from_index is not None and to_index is not None and from_index <= to_index:
            start_ms = transcript.sentences[from_index].startMs
            end_ms = transcript.sentences[to_index].endMs
        facts.append(
            SegmentFacts(
                index=index,
                from_id=request.from_id,
                to_id=request.to_id,
                from_index=from_index,
                to_index=to_index,
                start_ms=start_ms,
                end_ms=end_ms,
                # Cuts are expressed in whole sentences, so the resolved times are the
                # boundaries themselves; the rule still verifies that equality.
                boundary_start_ms=start_ms,
                boundary_end_ms=end_ms,
            )
        )
    return facts


def build_timeline(
    source: SourceRecord,
    transcript_sha256: str,
    transcript: Transcript,
    segments: list[SegmentFacts],
    target_duration_ms: int | None,
) -> dict:
    """The canonical timeline document. Unresolved segments keep null times."""
    entries = []
    total_ms = 0
    for segment in segments:
        sentences = []
        if segment.from_index is not None and segment.to_index is not None and segment.from_index <= segment.to_index:
            sentences = [
                {"endMs": s.endMs, "id": s.id, "startMs": s.startMs, "text": s.text}
                for s in transcript.sentences[segment.from_index : segment.to_index + 1]
            ]
        if segment.start_ms is not None and segment.end_ms is not None:
            total_ms += segment.end_ms - segment.start_ms
        entries.append(
            {
                "endMs": segment.end_ms,
                "from": segment.from_id,
                "sentences": sentences,
                "startMs": segment.start_ms,
                "to": segment.to_id,
            }
        )
    return {
        "cropCenterX": round(source.cropCenterX, CROP_CENTER_DECIMALS),
        "durationMs": total_ms,
        "segments": entries,
        "sourceId": source.sourceId,
        "sourceSha256": source.sha256,
        "targetDurationMs": target_duration_ms,
        "transcriptSha256": transcript_sha256,
    }


def clip_spans(timeline: dict) -> list[SentenceSpan]:
    """Places every chosen sentence on the clip's time axis, segment after segment."""
    spans: list[SentenceSpan] = []
    offset = 0
    for segment in timeline["segments"]:
        if segment["startMs"] is None or segment["endMs"] is None:
            continue
        for sentence in segment["sentences"]:
            spans.append(
                SentenceSpan(
                    id=sentence["id"],
                    text=sentence["text"],
                    start_ms=offset + sentence["startMs"] - segment["startMs"],
                    end_ms=offset + sentence["endMs"] - segment["startMs"],
                )
            )
        offset += segment["endMs"] - segment["startMs"]
    return spans


class TimelineStore:
    def __init__(self, data: DataDir) -> None:
        self._data = data

    def path(self, timeline_sha256: str) -> Path:
        return self._data.timelines / f"{timeline_sha256}.json"

    def save(self, canonical_text: str) -> str:
        timeline_sha256 = sha256_text(canonical_text)
        self._data.write_text(self.path(timeline_sha256), canonical_text)
        return timeline_sha256

    def load_text(self, timeline_sha256: str) -> str | None:
        path = self.path(timeline_sha256)
        if not path.exists():
            return None
        return path.read_text("utf-8")

    def load(self, timeline_sha256: str) -> dict | None:
        text = self.load_text(timeline_sha256)
        return None if text is None else json.loads(text)
