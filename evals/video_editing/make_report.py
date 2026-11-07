#!/usr/bin/env python3
"""Unblind a batch and write batches/<id>/report.md.

Requires runs.jsonl, blind/key.jsonl and a labels.jsonl row for every blinded item.
judge.jsonl is optional: without it the agreement section says so. A regression batch
that names a base batch gets a per-sample before/after table.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Any

import report_stats as stats
from blinding import unblind
from records import (
    HUMAN_DIMENSIONS,
    JUDGED_DIMENSIONS,
    OUTCOMES,
    VARIANTS,
    RecordError,
    read_json,
    read_jsonl,
    validate_blind_key_row,
    validate_judge,
    validate_label,
    validate_manifest,
    validate_run,
)

HERE = Path(__file__).resolve().parent


def load_batch(batch_dir: Path) -> dict[str, Any]:
    manifest = validate_manifest(read_json(batch_dir / "manifest.json"))
    runs = [validate_run(run) for run in read_jsonl(batch_dir / "runs.jsonl")]
    key_rows = [validate_blind_key_row(row) for row in read_jsonl(batch_dir / "blind" / "key.jsonl")]
    labels = [validate_label(label) for label in read_jsonl(batch_dir / "labels.jsonl")]
    judge_rows = [validate_judge(row) for row in read_jsonl(batch_dir / "judge.jsonl")]
    if not runs:
        raise RecordError(f"{batch_dir}: runs.jsonl is empty")
    if not key_rows:
        raise RecordError(f"{batch_dir}: blind/key.jsonl is missing; build the blind sheet first")
    key_by_blind = unblind(key_rows)
    missing = sorted(b for b in key_by_blind if b not in {label["blindId"] for label in labels})
    if missing:
        raise RecordError(f"{batch_dir}: labels.jsonl lacks rows for blinded items {missing}")
    labels_by_run = {key_by_blind[label["blindId"]]["runId"]: label for label in labels
                     if label["blindId"] in key_by_blind}
    unknown = sorted(label["blindId"] for label in labels if label["blindId"] not in key_by_blind)
    if unknown:
        raise RecordError(f"{batch_dir}: labels.jsonl names unknown blind ids {unknown}")
    latest_judge: dict[str, dict[str, Any]] = {}
    for row in judge_rows:
        latest_judge[row["runId"]] = row
    return {
        "manifest": manifest, "runs": runs, "labels": labels, "labels_by_run": labels_by_run,
        "key_by_run": {row["runId"]: row for row in key_rows}, "judge_rows": judge_rows,
        "judge_by_run": latest_judge, "reviewers": sorted({label["reviewer"] for label in labels}),
    }


def _table(headers: list[str], rows: list[list[Any]]) -> str:
    lines = ["| " + " | ".join(headers) + " |", "|" + "|".join("---" for _ in headers) + "|"]
    for row in rows:
        lines.append("| " + " | ".join("" if cell is None else str(cell) for cell in row) + " |")
    return "\n".join(lines)


def render_report(batch: dict[str, Any], base: dict[str, Any] | None) -> str:
    manifest, runs = batch["manifest"], batch["runs"]
    labels_by_run, judge_by_run = batch["labels_by_run"], batch["judge_by_run"]
    out: list[str] = [f"# Evaluation report: batch {manifest['batchId']}", ""]
    out.append(_table(["Item", "Value"], [
        ["Batch type", manifest["type"]],
        ["Dataset version", manifest["datasetVersion"]],
        ["Sample size", f"{len({r['sampleId'] for r in runs})} samples, {len(runs)} runs"],
        ["Reviewers", f"{len(batch['reviewers'])} ({', '.join(batch['reviewers'])}); the reviewer is the author"],
        ["Blinding", f"variant hidden, order shuffled with seed {manifest['randomSeed']}; "
                     "unblinded only in this report, after labels were locked"],
        ["Rule version", manifest["ruleVersion"]],
        ["Prompt version", manifest["promptVersion"]],
        ["Tool schema digest", manifest["toolSchemaDigest"]],
        ["Decision model", manifest["models"]["decision"]],
        ["Review model", manifest["models"].get("review", "none")],
        ["Judge model", manifest["models"]["judge"]],
        ["Definitions", "; ".join(f"{v}: {manifest['definitions'][v]['workflowId']} v{manifest['definitions'][v]['version']}"
                                  for v in VARIANTS)],
    ]))
    if manifest["type"] == "regression":
        out += ["", f"Base batch: {manifest['baseBatchId']}. Hypothesis: {manifest['hypothesis']}"]
    out += ["", "Two runs per sample is an observation, not evidence that one variant is better. "
                "The judge saw only text (requirement, transcript, chosen sentences); the reviewer saw the clip "
                "and the source video. This batch both tunes and checks the judge, so its agreement is a "
                "preliminary calibration.", ""]

    out += ["## Outcomes", ""]
    counts = stats.outcome_counts(runs)
    out.append(_table(["Variant", *OUTCOMES, "Delivery rate"], [
        [v, *[counts[v][o] for o in OUTCOMES],
         f"{counts[v]['CLIP_DELIVERED']}/{sum(counts[v].values())}"] for v in VARIANTS]))

    out += ["", "## Failure types (reviewer tags)", ""]
    tags = stats.failure_tag_counts(labels_by_run, runs)
    all_tags = sorted({t for v in VARIANTS for t in tags[v]})
    out.append(_table(["Tag", *VARIANTS], [[t, *[tags[v][t] for v in VARIANTS]] for t in all_tags])
               if all_tags else "No failure tags were recorded.")

    out += ["", "## Human scores (delivered clips, mean of 1-5)", ""]
    scores = stats.human_scores(labels_by_run, runs)
    out.append(_table(["Dimension", *[f"{v} (mean, n)" for v in VARIANTS]], [
        [d, *[f"{scores[v][d]['mean']} ({scores[v][d]['n']})" for v in VARIANTS]] for d in HUMAN_DIMENSIONS]))

    out += ["", "## Judge agreement (binary conclusions)", ""]
    if judge_by_run:
        agree = stats.agreement(labels_by_run, judge_by_run, runs)
        out.append(_table(["Dimension", "Compared", "Agreed", "Rate"], [
            [d, agree["perDimension"][d]["compared"], agree["perDimension"][d]["agreed"],
             agree["perDimension"][d]["rate"]] for d in JUDGED_DIMENSIONS]))
        out += ["", "### Disagreements", ""]
        out.append(_table(["Dimension", "Sample", "Variant", "Run", "Human", "Judge", "Human issues", "Judge reason"], [
            [d["dimension"], d["sampleId"], d["variant"], d["runId"], d["human"], d["judge"],
             d["humanIssues"].replace("\n", " "), d["judgeReason"].replace("\n", " ")]
            for d in agree["disagreements"]]) if agree["disagreements"] else "No disagreements.")
    else:
        out.append("judge.jsonl is absent; no agreement computed.")

    out += ["", "## Checks and revisions", ""]
    rates = stats.check_rates(runs)
    out.append(_table(["Variant", "Check facts", "Check pass rate", "Runs", "Revision trigger rate", "Mean revisions"], [
        [v, rates[v]["checkFacts"], rates[v]["checkPassRate"], rates[v]["runs"],
         rates[v]["revisionTriggerRate"], rates[v]["meanRevisions"]] for v in VARIANTS]))
    gaps = stats.check_gaps(labels_by_run, runs)
    out.append(f"\nCheck gaps (checks passed, reviewer saw a technical problem): {len(gaps)}"
               + (f" ({', '.join(gaps)})" if gaps else ""))

    out += ["", "## Cost and time (model calls only)", ""]
    cost = stats.cost_totals(runs)
    timing = stats.elapsed(runs)
    out.append(_table(["Variant", "Task cost", "Calls with unknown cost", "Mean elapsed (s)", "Mean turns"], [
        [v, ", ".join(f"{a} {c}" for c, a in cost[v]["totals"].items()) or "0", cost[v]["unknownCalls"],
         timing[v]["meanSeconds"], timing[v]["meanTurns"]] for v in VARIANTS]))
    jc = stats.judge_cost(batch["judge_rows"])
    out.append(f"\nJudge cost (separate, not part of task cost): {jc['amount']} {jc['currency']} over "
               f"{jc['calls']} calls ({jc['callsWithKnownCost']} with a known charge).")

    out += ["", "## Runs without a clip", ""]
    rows = stats.cannot_satisfy(labels_by_run, batch["key_by_run"], runs)
    out.append(_table(["Sample", "Variant", "Run", "Outcome", "Expected satisfiable", "Reason matches annotation", "Stated reason"], [
        [r["sampleId"], r["variant"], r["runId"], r["outcome"], r["expectedSatisfiable"],
         r["reasonMatchesAnnotation"], (r["statedReason"] or "").replace("\n", " ")] for r in rows])
               if rows else "Every run delivered a clip.")

    out += ["", "## Per-run record", ""]
    out.append(_table(["Sample", "Variant", "Run", "Outcome", "Clip", "Turns", "Revisions", "Elapsed (s)"], [
        [r["sampleId"], r["variant"], r["runId"], r["outcome"], r["clipSha256"][:12] if r["clipSha256"] else "",
         r["turnsUsed"], r["revisions"]["mediaChecked"] + r["revisions"]["contentReviewed"], r["elapsedSeconds"]]
        for r in sorted(runs, key=lambda x: (x["sampleId"], x["variant"]))]))

    if base is not None:
        out += ["", f"## Before/after against base batch {base['manifest']['batchId']}", ""]
        base_by_pair = {(r["sampleId"], r["variant"]): r for r in base["runs"]}
        rows = []
        for run in sorted(runs, key=lambda x: (x["sampleId"], x["variant"])):
            before = base_by_pair.get((run["sampleId"], run["variant"]))
            rows.append([run["sampleId"], run["variant"],
                         before["outcome"] if before else "(not in base)", run["outcome"],
                         _score_summary(base["labels_by_run"].get(before["runId"])) if before else "",
                         _score_summary(labels_by_run.get(run["runId"]))])
        out.append(_table(["Sample", "Variant", "Outcome before", "Outcome after", "Scores before", "Scores after"], rows))
    return "\n".join(out) + "\n"


def _score_summary(label: dict[str, Any] | None) -> str:
    if not label:
        return ""
    return "/".join("-" if label["scores"][d] is None else str(label["scores"][d]) for d in HUMAN_DIMENSIONS)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("batch_id")
    parser.add_argument("--batches-dir", type=Path, default=HERE / "batches")
    args = parser.parse_args(argv)

    batch = load_batch(args.batches_dir / args.batch_id)
    base = None
    if batch["manifest"]["type"] == "regression":
        base = load_batch(args.batches_dir / batch["manifest"]["baseBatchId"])
    report_path = args.batches_dir / args.batch_id / "report.md"
    report_path.write_text(render_report(batch, base), encoding="utf-8")
    print(report_path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
