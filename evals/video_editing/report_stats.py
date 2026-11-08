"""Pure statistics over unblinded batch records.

Every rate uses all runs of the batch (or variant) as its denominator, so a failed or
incomplete Run lowers the delivery rate instead of disappearing from it.
"""

from __future__ import annotations

from collections import Counter, defaultdict
from decimal import Decimal
from typing import Any

from records import HUMAN_DIMENSIONS, JUDGED_DIMENSIONS, OUTCOMES, VARIANTS


def outcome_counts(runs: list[dict[str, Any]]) -> dict[str, dict[str, int]]:
    counts = {variant: {outcome: 0 for outcome in OUTCOMES} for variant in VARIANTS}
    for run in runs:
        counts[run["variant"]][run["outcome"]] += 1
    return counts


def failure_tag_counts(labels_by_run: dict[str, dict[str, Any]], runs: list[dict[str, Any]]) -> dict[str, Counter]:
    counts = {variant: Counter() for variant in VARIANTS}
    for run in runs:
        label = labels_by_run.get(run["runId"])
        if label:
            counts[run["variant"]].update(label.get("failureTags", []))
    return counts


def mean(values: list[float]) -> float | None:
    return round(sum(values) / len(values), 2) if values else None


def human_scores(labels_by_run: dict[str, dict[str, Any]], runs: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    """Mean human score per dimension and variant over delivered clips with a score."""
    result: dict[str, dict[str, Any]] = {}
    for variant in VARIANTS:
        per_dimension: dict[str, Any] = {}
        for dimension in HUMAN_DIMENSIONS:
            values = [labels_by_run[r["runId"]]["scores"][dimension] for r in runs
                      if r["variant"] == variant and r["runId"] in labels_by_run
                      and labels_by_run[r["runId"]]["scores"].get(dimension) is not None]
            per_dimension[dimension] = {"mean": mean(values), "n": len(values)}
        result[variant] = per_dimension
    return result


def agreement(labels_by_run: dict[str, dict[str, Any]], judge_by_run: dict[str, dict[str, Any]],
              runs: list[dict[str, Any]]) -> dict[str, Any]:
    """Agreement of binary conclusions per shared dimension, plus every disagreement."""
    per_dimension: dict[str, dict[str, Any]] = {}
    disagreements: list[dict[str, Any]] = []
    for dimension in JUDGED_DIMENSIONS:
        compared = agreed = 0
        for run in runs:
            label, judged = labels_by_run.get(run["runId"]), judge_by_run.get(run["runId"])
            if not label or not judged or not judged.get("parsed"):
                continue
            human = label["verdicts"].get(dimension)
            if human is None:
                continue
            machine = judged["parsed"][dimension]["verdict"]
            compared += 1
            if human == machine:
                agreed += 1
            else:
                disagreements.append({
                    "dimension": dimension, "sampleId": run["sampleId"], "variant": run["variant"],
                    "runId": run["runId"], "human": human, "judge": machine,
                    "humanIssues": label.get("issues", ""), "judgeReason": judged["parsed"][dimension]["reason"],
                })
        per_dimension[dimension] = {"compared": compared, "agreed": agreed,
                                    "rate": round(agreed / compared, 3) if compared else None}
    return {"perDimension": per_dimension, "disagreements": disagreements}


def check_rates(runs: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    """Check pass rate over check facts and revision trigger rate over all runs."""
    result: dict[str, dict[str, Any]] = {}
    for variant in VARIANTS:
        subset = [r for r in runs if r["variant"] == variant]
        total = sum(r["checks"]["mediaCheckedTotal"] + r["checks"]["contentReviewedTotal"] for r in subset)
        failed = sum(r["checks"]["mediaCheckedFailed"] + r["checks"]["contentReviewedFailed"] for r in subset)
        triggered = sum(1 for r in subset if r["revisions"]["mediaChecked"] + r["revisions"]["contentReviewed"] > 0)
        result[variant] = {
            "checkFacts": total,
            "checkPassRate": round((total - failed) / total, 3) if total else None,
            "runs": len(subset),
            "revisionTriggerRate": round(triggered / len(subset), 3) if subset else None,
            "meanRevisions": mean([r["revisions"]["mediaChecked"] + r["revisions"]["contentReviewed"] for r in subset]),
        }
    return result


def check_gaps(labels_by_run: dict[str, dict[str, Any]], runs: list[dict[str, Any]]) -> list[str]:
    return [r["runId"] for r in runs if labels_by_run.get(r["runId"], {}).get("checkGap")]


def cost_totals(runs: list[dict[str, Any]]) -> dict[str, Any]:
    """Task cost per variant and currency, summed from the API's own Run totals."""
    totals: dict[str, dict[str, Decimal]] = {variant: defaultdict(Decimal) for variant in VARIANTS}
    unknown = {variant: 0 for variant in VARIANTS}
    for run in runs:
        for total in run["cost"]["totals"]:
            totals[run["variant"]][total["currency"]] += Decimal(str(total["amount"]))
        unknown[run["variant"]] += run["cost"]["unknownCalls"]
    return {variant: {"totals": {cur: str(amount) for cur, amount in totals[variant].items()},
                      "unknownCalls": unknown[variant]} for variant in VARIANTS}


def judge_cost(judge_rows: list[dict[str, Any]]) -> dict[str, Any]:
    total = Decimal(0)
    known = 0
    for row in judge_rows:
        if row.get("cost"):
            total += Decimal(str(row["cost"]["amount"]))
            known += 1
    return {"amount": str(total), "currency": "USD", "calls": len(judge_rows), "callsWithKnownCost": known}


def elapsed(runs: list[dict[str, Any]]) -> dict[str, Any]:
    return {variant: {"meanSeconds": mean([r["elapsedSeconds"] for r in runs
                                           if r["variant"] == variant and r["elapsedSeconds"] is not None]),
                      "meanTurns": mean([r["turnsUsed"] for r in runs if r["variant"] == variant])}
            for variant in VARIANTS}


def cannot_satisfy(labels_by_run: dict[str, dict[str, Any]], key_by_run: dict[str, dict[str, Any]],
                   runs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Runs without a clip, judged only on whether the stated reason matches the annotation."""
    rows = []
    for run in runs:
        if run["outcome"] == "CLIP_DELIVERED":
            continue
        label = labels_by_run.get(run["runId"], {})
        annotation = key_by_run.get(run["runId"], {}).get("annotation", {})
        rows.append({
            "sampleId": run["sampleId"], "variant": run["variant"], "runId": run["runId"],
            "outcome": run["outcome"], "expectedSatisfiable": annotation.get("expectedSatisfiable"),
            "reasonMatchesAnnotation": label.get("reasonMatchesAnnotation"),
            "statedReason": run.get("cannotSatisfyReason") or run.get("failureReason"),
        })
    return rows
