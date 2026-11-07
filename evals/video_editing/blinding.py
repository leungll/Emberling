"""Blind-review mapping: a deterministic shuffle that hides the variant from the reviewer.

The sheet rows are what the review page shows; the key rows are what unblinding needs.
No sheet row ever carries the variant, the Run id or the annotation.
"""

from __future__ import annotations

import random
from typing import Any

from records import BLIND_KEY_SCHEMA, BLIND_SHEET_SCHEMA


def _stated_reason(run: dict[str, Any]) -> str | None:
    if run["outcome"] == "CANNOT_SATISFY":
        return run.get("cannotSatisfyReason")
    return run.get("failureReason")


def build_blind_mapping(runs: list[dict[str, Any]], rows_by_sample: dict[str, dict[str, Any]], seed: int,
                        clip_src_for) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Shuffle every run with the batch seed and assign opaque blind ids.

    Delivered clips and no-clip runs are shuffled together so the position reveals
    nothing; the page lists them in two sections by kind but keeps the shuffled order
    inside each. clip_src_for(run) returns the <video> source for a delivered run.
    """
    ordered = sorted(runs, key=lambda run: (run["sampleId"], run["variant"], run["runId"]))
    rng = random.Random(seed)
    rng.shuffle(ordered)
    sheet: list[dict[str, Any]] = []
    key: list[dict[str, Any]] = []
    for index, run in enumerate(ordered, start=1):
        blind_id = f"B{index:02d}"
        row = rows_by_sample[run["sampleId"]]
        base = {
            "schemaVersion": BLIND_SHEET_SCHEMA,
            "blindId": blind_id,
            "requirement": row["annotation"]["requirement"],
            "sourceTitle": row["sourceTitle"],
            "sourceUrl": row["sourceUrl"],
        }
        if run["outcome"] == "CLIP_DELIVERED" and run.get("clipSha256"):
            sheet.append({**base, "kind": "clip", "clipSha256": run["clipSha256"], "clipSrc": clip_src_for(run)})
        else:
            sheet.append({**base, "kind": "no_clip", "statedOutcome": run["outcome"],
                          "statedReason": _stated_reason(run)})
        key.append({
            "schemaVersion": BLIND_KEY_SCHEMA,
            "blindId": blind_id,
            "runId": run["runId"],
            "sampleId": run["sampleId"],
            "variant": run["variant"],
            "outcome": run["outcome"],
            "annotation": row["annotation"],
        })
    return sheet, key


def unblind(key_rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    return {row["blindId"]: row for row in key_rows}
