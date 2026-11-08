#!/usr/bin/env python3
"""Run a frozen batch: one basic and one reviewed Run per sample, serially.

Reads batches/<id>/manifest.json, creates each Run through the Emberling API, waits for
it to end and appends one line per Run to batches/<id>/runs.jsonl. The script never
writes anything into Emberling except the Run submissions, and never alters a line it
already wrote.

Resumption: a (sample, variant) pair that already has a record is skipped, whatever its
outcome. An `eval` batch is immutable once it has run: the manifest digest is recorded
with every line, a changed manifest is refused, and no pair can be run again. A
`regression` or `showcase` batch may re-run a pair only with --rerun, which appends a new
line and keeps the old one.
"""

from __future__ import annotations

import argparse
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from emberling_api import EmberlingClient
from media_api import MediaClient
from records import (
    RUN_SCHEMA,
    VARIANTS,
    RecordError,
    append_jsonl,
    manifest_digest,
    read_json,
    read_jsonl,
    validate_manifest,
    validate_prepared,
    validate_run,
)
from trace_summary import summarize

HERE = Path(__file__).resolve().parent


def run_input_for(row: dict[str, Any], prepared: dict[str, Any]) -> dict[str, Any]:
    """The Run input task object: media reference, digest, duration, requirement and goal."""
    return {
        "task": {
            "sourceId": prepared["sourceId"],
            "sourceSha256": prepared["sha256"],
            "durationMs": prepared["durationMs"],
            "requirement": row["annotation"]["requirement"],
            "goal": "DELIVER_CLIP",
        }
    }


def recorded_pairs(runs: list[dict[str, Any]]) -> set[tuple[str, str]]:
    return {(run["sampleId"], run["variant"]) for run in runs}


def check_batch_state(manifest: dict[str, Any], runs: list[dict[str, Any]], rerun: set[tuple[str, str]]) -> None:
    """Refuse the edits an eval batch forbids: a changed manifest or a repeated pair."""
    digest = manifest_digest(manifest)
    for run in runs:
        validate_run(run)
        if run["manifestDigest"] != digest:
            raise RecordError(
                f"batch {manifest['batchId']}: runs.jsonl was written under a different manifest "
                f"({run['manifestDigest'][:12]} != {digest[:12]}); a frozen batch is never edited")
    if manifest["type"] == "eval" and rerun:
        raise RecordError(f"batch {manifest['batchId']} is an eval batch: a pair is never run twice")
    already = recorded_pairs(runs)
    for pair in rerun:
        if pair not in already:
            raise RecordError(f"--rerun {pair[0]}:{pair[1]} names a pair that has no record yet")


def execute_pair(client: EmberlingClient, media: MediaClient, manifest: dict[str, Any], row: dict[str, Any],
                 prepared: dict[str, Any], variant: str, *, run_timeout: float, poll_seconds: float,
                 attempt: int) -> dict[str, Any]:
    definition = manifest["definitions"][variant]
    key = f"{manifest['batchId']}:{row['sampleId']}:{variant}:{attempt}"
    created = client.create_run(definition["workflowId"], definition["version"], run_input_for(row, prepared),
                                idempotency_key=key)
    run_id = created["id"]
    print(f"{row['sampleId']} {variant}: run {run_id} created", flush=True)

    try:
        snapshot = client.wait_for_run(run_id, timeout_seconds=run_timeout, poll_seconds=poll_seconds)
    except TimeoutError as err:
        print(f"{row['sampleId']} {variant}: {err}", file=sys.stderr, flush=True)
        snapshot = client.get_run(run_id)

    trace = None
    agent_node = next((n for n in snapshot.get("nodeRuns", []) if n.get("nodeType") == "agent"), None)
    if agent_node is not None:
        trace = client.get_agent_trace(run_id, agent_node["id"])
    summary = summarize(snapshot, trace, clip_url_for=media.clip_url)
    if snapshot["run"]["status"] not in ("COMPLETED", "FAILED"):
        summary["outcome"] = "FAILED"
        summary["failureReason"] = f"run still {snapshot['run']['status']} after {run_timeout}s; recorded as failed"
    cost = client.get_run_cost(run_id, definition["workflowId"])

    record = {
        "schemaVersion": RUN_SCHEMA,
        "batchId": manifest["batchId"],
        "manifestDigest": manifest_digest(manifest),
        "sampleId": row["sampleId"],
        "variant": variant,
        "runId": run_id,
        "definition": {"workflowId": definition["workflowId"], "version": definition["version"]},
        "cost": {"totals": cost.get("totals", []), "unknownCalls": int(cost.get("unknownCalls", 0))},
        "recordedAt": datetime.now(timezone.utc).isoformat(),
        **summary,
    }
    return validate_run(record)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("batch_id")
    parser.add_argument("--batches-dir", type=Path, default=HERE / "batches")
    parser.add_argument("--dataset-dir", type=Path, default=HERE / "dataset")
    parser.add_argument("--api-url", default="http://localhost:8080")
    parser.add_argument("--media-url", default="http://localhost:9201")
    parser.add_argument("--run-timeout", type=float, default=2400.0,
                        help="seconds to wait for one Run before recording it as failed")
    parser.add_argument("--poll-seconds", type=float, default=2.0)
    parser.add_argument("--only", action="append", default=[], help="sample id to run; repeatable")
    parser.add_argument("--rerun", action="append", default=[], metavar="SAMPLE:VARIANT",
                        help="append a new attempt for a recorded pair (not allowed for eval batches)")
    args = parser.parse_args(argv)

    batch_dir = args.batches_dir / args.batch_id
    manifest = validate_manifest(read_json(batch_dir / "manifest.json"))
    if manifest["batchId"] != args.batch_id:
        raise RecordError(f"manifest batchId {manifest['batchId']!r} does not match directory {args.batch_id!r}")
    rows = {row["sampleId"]: row for row in read_jsonl(args.dataset_dir / f"{manifest['datasetVersion']}.jsonl")}
    prepared = validate_prepared(read_json(args.dataset_dir / f"{manifest['datasetVersion']}.prepared.json"))
    runs_path = batch_dir / "runs.jsonl"
    runs = read_jsonl(runs_path)

    rerun: set[tuple[str, str]] = set()
    for item in args.rerun:
        sample_id, _, variant = item.partition(":")
        if variant not in VARIANTS:
            parser.error(f"--rerun {item!r}: variant must be one of {list(VARIANTS)}")
        rerun.add((sample_id, variant))
    check_batch_state(manifest, runs, rerun)
    already = recorded_pairs(runs)

    client = EmberlingClient(args.api_url)
    media = MediaClient(args.media_url)
    try:
        for sample_id, row in sorted(rows.items()):
            if args.only and sample_id not in args.only:
                continue
            if sample_id not in prepared["samples"]:
                raise RecordError(f"sample {sample_id} is not prepared; run prepare_dataset.py first")
            for variant in VARIANTS:
                pair = (sample_id, variant)
                if pair in already and pair not in rerun:
                    print(f"{sample_id} {variant}: already recorded, skipped", flush=True)
                    continue
                attempt = sum(1 for run in runs if (run["sampleId"], run["variant"]) == pair) + 1
                record = execute_pair(client, media, manifest, row, prepared["samples"][sample_id], variant,
                                      run_timeout=args.run_timeout, poll_seconds=args.poll_seconds, attempt=attempt)
                append_jsonl(runs_path, record)
                runs.append(record)
                print(f"{sample_id} {variant}: {record['outcome']} in {record['elapsedSeconds']}s, "
                      f"{record['turnsUsed']} turns", flush=True)
    finally:
        client.close()
        media.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
