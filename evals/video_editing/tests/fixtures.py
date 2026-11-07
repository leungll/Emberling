"""Fixed records shared by the self-tests."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from records import (
    BLIND_KEY_SCHEMA,
    JUDGE_SCHEMA,
    LABEL_SCHEMA,
    MANIFEST_SCHEMA,
    PREPARED_SCHEMA,
    RUN_SCHEMA,
    manifest_digest,
    write_json,
    write_jsonl,
)

DATASET_PATH = Path(__file__).resolve().parent.parent / "dataset" / "v1.jsonl"

CLIP_A = "a1" * 32
CLIP_B = "b2" * 32
CLIP_C = "c3" * 32
TIMELINE_A = "d4" * 32


def manifest(batch_id: str = "eval-001", batch_type: str = "eval", **overrides: Any) -> dict[str, Any]:
    record = {
        "schemaVersion": MANIFEST_SCHEMA,
        "batchId": batch_id,
        "type": batch_type,
        "datasetVersion": "v1",
        "definitions": {"basic": {"workflowId": "wf_basic", "version": 1},
                        "reviewed": {"workflowId": "wf_reviewed", "version": 1}},
        "models": {"decision": "opus-5.5", "review": "text-review", "judge": "anthropic/claude-opus-5.5"},
        "promptVersion": "clip-agent/v1",
        "toolSchemaDigest": "sha256:" + "0" * 64,
        "ruleVersion": "v1",
        "randomSeed": 20261107,
        "frozenAt": "2026-11-07T09:00:00Z",
    }
    if batch_type == "regression":
        record["baseBatchId"] = "eval-001"
        record["hypothesis"] = "the opening sentence rule reduces weak openings"
    record.update(overrides)
    return record


def dataset_rows() -> list[dict[str, Any]]:
    return [json.loads(line) for line in DATASET_PATH.read_text(encoding="utf-8").splitlines() if line.strip()]


def prepared(media_url: str = "http://media.test") -> dict[str, Any]:
    return {"schemaVersion": PREPARED_SCHEMA, "datasetVersion": "v1", "mediaUrl": media_url,
            "samples": {row["sampleId"]: {"sourceId": f"src_{row['sampleId']}", "sha256": row["sourceSha256"],
                                          "durationMs": 267000, "mediaType": "video/webm",
                                          "preparedFileName": row["localFileName"]}
                        for row in dataset_rows()}}


def run_record(sample_id: str, variant: str, run_id: str, outcome: str, *, clip: str | None = None,
               man: dict[str, Any] | None = None, media_failed: int = 0, review_failed: int = 0,
               cost: str = "0.30", elapsed: float = 210.0, turns: int = 6, reason: str | None = None) -> dict[str, Any]:
    man = man or manifest()
    return {
        "schemaVersion": RUN_SCHEMA,
        "batchId": man["batchId"],
        "manifestDigest": manifest_digest(man),
        "sampleId": sample_id,
        "variant": variant,
        "runId": run_id,
        "agentNodeRunId": f"nr_{run_id}",
        "agentRunId": f"ar_{run_id}",
        "outcome": outcome,
        "runStatus": "COMPLETED" if outcome in ("CLIP_DELIVERED", "CANNOT_SATISFY") else "FAILED",
        "clipSha256": clip,
        "clipUrl": f"http://media.test/v1/clips/{clip}" if clip else None,
        "failureReason": reason if outcome in ("INCOMPLETE", "FAILED") else None,
        "cannotSatisfyReason": reason if outcome == "CANNOT_SATISFY" else None,
        "cost": {"totals": [{"amount": cost, "currency": "USD"}], "unknownCalls": 0},
        "elapsedSeconds": elapsed,
        "revisions": {"mediaChecked": media_failed, "contentReviewed": review_failed},
        "checks": {"mediaCheckedTotal": media_failed + 2, "mediaCheckedFailed": media_failed,
                   "contentReviewedTotal": review_failed + (1 if variant == "reviewed" else 0),
                   "contentReviewedFailed": review_failed},
        "turnsUsed": turns,
        "transcriptArtifactId": "artifact_transcript",
        "timelineArtifactId": "artifact_" + TIMELINE_A[:32],
        "timelineSha256": TIMELINE_A,
        "recordedAt": "2026-11-07T10:05:00Z",
    }


def runs_fixture(man: dict[str, Any] | None = None) -> list[dict[str, Any]]:
    """Two samples, both variants: one delivered pair, one cannot-satisfy plus one failed."""
    man = man or manifest()
    return [
        run_record("01", "basic", "run_001", "CLIP_DELIVERED", clip=CLIP_A, man=man, media_failed=1),
        run_record("01", "reviewed", "run_002", "CLIP_DELIVERED", clip=CLIP_B, man=man, review_failed=1, cost="0.50"),
        run_record("02", "basic", "run_003", "CANNOT_SATISFY", man=man, reason="the interview never discusses social media"),
        run_record("02", "reviewed", "run_004", "FAILED", man=man, reason="node_agent MODEL_ERROR: injected"),
    ]


def key_rows(runs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    rows_by_sample = {row["sampleId"]: row for row in dataset_rows()}
    return [{"schemaVersion": BLIND_KEY_SCHEMA, "blindId": f"B{i:02d}", "runId": run["runId"],
             "sampleId": run["sampleId"], "variant": run["variant"], "outcome": run["outcome"],
             "annotation": rows_by_sample[run["sampleId"]]["annotation"]}
            for i, run in enumerate(runs, start=1)]


def label(blind_id: str, *, scores: dict[str, int | None] | None = None, verdicts: dict[str, bool | None] | None = None,
          tags: list[str] | None = None, check_gap: bool = False, reason_match: bool | None = None,
          issues: str = "") -> dict[str, Any]:
    return {"schemaVersion": LABEL_SCHEMA, "blindId": blind_id, "reviewer": "author",
            "scores": scores or {"notBroken": 5, "asRequested": 4, "contentQuality": 4, "presentationQuality": 3},
            "verdicts": verdicts or {"asRequested": True, "contentQuality": True},
            "issues": issues, "failureTags": tags or [], "checkGap": check_gap,
            "reasonMatchesAnnotation": reason_match}


def labels_for(runs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    rows = []
    for i, run in enumerate(runs, start=1):
        blind_id = f"B{i:02d}"
        if run["outcome"] == "CLIP_DELIVERED":
            rows.append(label(blind_id, tags=["weak_opening"] if run["variant"] == "basic" else [],
                              verdicts={"asRequested": True, "contentQuality": run["variant"] == "reviewed"}))
        else:
            rows.append(label(blind_id, scores={d: None for d in ("notBroken", "asRequested", "contentQuality", "presentationQuality")},
                              verdicts={"asRequested": None, "contentQuality": None},
                              reason_match=run["outcome"] == "CANNOT_SATISFY"))
    return rows


def judge_row(run_id: str, blind_id: str, as_requested: bool, content: bool) -> dict[str, Any]:
    return {"schemaVersion": JUDGE_SCHEMA, "judgeVersion": "judge/v1", "batchId": "eval-001", "runId": run_id,
            "blindId": blind_id, "model": "anthropic/claude-opus-5.5", "inputDigest": "e" * 64,
            "rawResponse": "{}", "parsed": {"asRequested": {"verdict": as_requested, "score": 4, "reason": "fits"},
                                            "contentQuality": {"verdict": content, "score": 3, "reason": "opening is slow"}},
            "usage": {"prompt_tokens": 1000, "completion_tokens": 80, "total_tokens": 1080, "cost": 0.02},
            "cost": {"amount": "0.02", "currency": "USD"}, "createdAt": "2026-11-07T12:00:00Z"}


def write_batch(batches_dir: Path, man: dict[str, Any], runs: list[dict[str, Any]] | None = None,
                labels: list[dict[str, Any]] | None = None, key: list[dict[str, Any]] | None = None,
                judge: list[dict[str, Any]] | None = None) -> Path:
    batch_dir = batches_dir / man["batchId"]
    write_json(batch_dir / "manifest.json", man)
    if runs is not None:
        write_jsonl(batch_dir / "runs.jsonl", runs)
    if key is not None:
        write_jsonl(batch_dir / "blind" / "key.jsonl", key)
    if labels is not None:
        write_jsonl(batch_dir / "labels.jsonl", labels)
    if judge is not None:
        write_jsonl(batch_dir / "judge.jsonl", judge)
    return batch_dir
