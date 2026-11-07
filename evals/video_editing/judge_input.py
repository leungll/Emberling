"""Build the judge's input from the transcript and the timeline, without the annotation.

The judge sees the user requirement, the full transcript and the sentences the Agent
chose. It never sees the human annotation or the rendered video, so its verdict is
independent of the reviewer's.
"""

from __future__ import annotations

import hashlib
import json
import re
from typing import Any

JUDGE_VERSION = "judge/v1"

SENTENCE_LINE = re.compile(r"^(S\d+)\t(\S+)\t(\S+)\t(.*)$")


def parse_transcript(text: str) -> list[tuple[str, str]]:
    """Transcript lines are `S<n>\\t<start>\\t<end>\\t<text>`; other lines are ignored."""
    sentences: list[tuple[str, str]] = []
    for line in text.splitlines():
        match = SENTENCE_LINE.match(line)
        if match:
            sentences.append((match.group(1), match.group(4).strip()))
    return sentences


def _sentence_number(sentence_id: str) -> int:
    return int(sentence_id[1:])


def timeline_segments(timeline: dict[str, Any]) -> list[tuple[str, str]]:
    """Segments as (from, to) sentence ids, read from the canonical timeline object."""
    raw = timeline.get("segments")
    if raw is None:
        raw = timeline.get("clips", [])
    segments: list[tuple[str, str]] = []
    for segment in raw:
        start = segment.get("from") or segment.get("fromSentence")
        end = segment.get("to") or segment.get("toSentence") or start
        if isinstance(start, str) and isinstance(end, str):
            segments.append((start, end))
    return segments


def chosen_sentences(transcript_text: str, timeline: dict[str, Any]) -> list[tuple[str, str]]:
    sentences = parse_transcript(transcript_text)
    by_number = {_sentence_number(sid): (sid, text) for sid, text in sentences}
    chosen: list[tuple[str, str]] = []
    for start, end in timeline_segments(timeline):
        for number in range(_sentence_number(start), _sentence_number(end) + 1):
            if number in by_number:
                chosen.append(by_number[number])
    return chosen


SYSTEM_PROMPT = """You evaluate a short vertical clip cut from a longer interview. You only see text: the user's requirement, the full transcript with sentence ids, and the sentences that were kept in the clip, in order. You do not see the video.

Judge two dimensions and answer with strict JSON and nothing else:
{"asRequested": {"verdict": true|false, "score": 1-5, "reason": "..."},
 "contentQuality": {"verdict": true|false, "score": 1-5, "reason": "..."}}

asRequested: the kept sentences satisfy the user's requirement AND do not distort the speaker's meaning when heard on their own (no dropped qualifiers, no reversed conclusions, no claims the speaker did not make). verdict is true only when both hold.
contentQuality: a viewer who has not seen the full interview can follow the clip, and the opening sentence draws them in. verdict is true when the clip is understandable on its own and opens well.
Scores: 5 excellent, 4 good, 3 acceptable, 2 poor, 1 unacceptable. Keep each reason under 60 words."""


def build_messages(requirement: str, transcript_text: str, chosen: list[tuple[str, str]]) -> list[dict[str, str]]:
    kept = "\n".join(f"{sid}\t{text}" for sid, text in chosen) or "(no sentences)"
    transcript = "\n".join(f"{sid}\t{text}" for sid, text in parse_transcript(transcript_text))
    user = (
        f"User requirement:\n{requirement}\n\n"
        f"Full transcript (sentence id, text):\n{transcript}\n\n"
        f"Sentences kept in the clip, in order:\n{kept}\n"
    )
    return [{"role": "system", "content": SYSTEM_PROMPT}, {"role": "user", "content": user}]


def input_digest(messages: list[dict[str, str]]) -> str:
    canonical = json.dumps(messages, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def parse_verdict(raw: str) -> dict[str, Any]:
    """Parse the judge's strict JSON answer, tolerating a fenced code block around it."""
    text = raw.strip()
    if text.startswith("```"):
        text = text.strip("`")
        if text.startswith("json"):
            text = text[4:]
    start, end = text.find("{"), text.rfind("}")
    if start < 0 or end < 0:
        raise ValueError("judge answer contains no JSON object")
    value = json.loads(text[start:end + 1])
    parsed: dict[str, Any] = {}
    for dimension in ("asRequested", "contentQuality"):
        entry = value.get(dimension)
        if not isinstance(entry, dict):
            raise ValueError(f"judge answer lacks {dimension}")
        verdict, score, reason = entry.get("verdict"), entry.get("score"), entry.get("reason", "")
        if not isinstance(verdict, bool):
            raise ValueError(f"{dimension}.verdict is not a boolean")
        if isinstance(score, bool) or not isinstance(score, int) or not 1 <= score <= 5:
            raise ValueError(f"{dimension}.score is not an integer from 1 to 5")
        parsed[dimension] = {"verdict": verdict, "score": score, "reason": str(reason)}
    return parsed
