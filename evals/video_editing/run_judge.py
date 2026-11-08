#!/usr/bin/env python3
"""Score every delivered clip of a batch with the judge model, after human labels are locked.

Refuses to run unless batches/<id>/labels.jsonl holds a row for every blinded item. For
each delivered Run it fetches the transcript and the timeline from Emberling's artifact
content route, builds the judge input (requirement, full transcript, chosen sentences;
never the annotation or the video), calls the OpenRouter chat completions API and appends
one line to batches/<id>/judge.jsonl. A Run already judged under the same judge version
and input digest is skipped, so the script can resume.

The API key is read from EMBERLING_MODEL_API_KEY only and is never written anywhere.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx

from emberling_api import EmberlingClient
from judge_input import JUDGE_VERSION, build_messages, chosen_sentences, input_digest, parse_verdict
from records import (
    JUDGE_SCHEMA,
    RecordError,
    append_jsonl,
    read_json,
    read_jsonl,
    validate_blind_key_row,
    validate_blind_sheet_row,
    validate_judge,
    validate_label,
    validate_manifest,
    validate_run,
)

HERE = Path(__file__).resolve().parent
DEFAULT_JUDGE_MODEL = "anthropic/claude-opus-5.5"
API_KEY_ENV = "EMBERLING_MODEL_API_KEY"


def require_labels_locked(sheet: list[dict[str, Any]], labels: list[dict[str, Any]]) -> None:
    """Every blinded item must be labelled before the judge sees any input."""
    labelled = {label["blindId"] for label in labels}
    missing = sorted(row["blindId"] for row in sheet if row["blindId"] not in labelled)
    if missing:
        raise RecordError(f"labels.jsonl lacks rows for blinded items {missing}; human scoring is not locked")


def call_openrouter(client: httpx.Client, api_key: str, model: str, messages: list[dict[str, str]],
                    *, base_url: str) -> dict[str, Any]:
    response = client.post(
        base_url.rstrip("/") + "/chat/completions",
        headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
        json={"model": model, "messages": messages, "temperature": 0, "max_tokens": 1024,
              "usage": {"include": True}},
    )
    if response.status_code >= 400:
        raise RuntimeError(f"judge request failed with status {response.status_code}")
    return response.json()


def judge_record(batch_id: str, run: dict[str, Any], blind_id: str, model: str, messages: list[dict[str, str]],
                 completion: dict[str, Any]) -> dict[str, Any]:
    raw = ""
    choices = completion.get("choices") or []
    if choices:
        raw = str((choices[0].get("message") or {}).get("content") or "")
    parsed: dict[str, Any] | None
    parse_error: str | None = None
    try:
        parsed = parse_verdict(raw)
    except (ValueError, json.JSONDecodeError) as err:
        parsed, parse_error = None, str(err)
    usage = completion.get("usage") if isinstance(completion.get("usage"), dict) else None
    cost = None
    if usage and isinstance(usage.get("cost"), (int, float)):
        cost = {"amount": str(usage["cost"]), "currency": "USD"}
    record = {
        "schemaVersion": JUDGE_SCHEMA,
        "judgeVersion": JUDGE_VERSION,
        "batchId": batch_id,
        "runId": run["runId"],
        "blindId": blind_id,
        "model": model,
        "inputDigest": input_digest(messages),
        "rawResponse": raw,
        "parsed": parsed,
        "parseError": parse_error,
        "usage": usage,
        "cost": cost,
        "createdAt": datetime.now(timezone.utc).isoformat(),
    }
    return validate_judge(record)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("batch_id")
    parser.add_argument("--batches-dir", type=Path, default=HERE / "batches")
    parser.add_argument("--api-url", default="http://localhost:8080")
    parser.add_argument("--openrouter-url", default="https://openrouter.ai/api/v1")
    parser.add_argument("--model", default=None, help="judge model; defaults to the manifest's models.judge")
    args = parser.parse_args(argv)

    api_key = os.environ.get(API_KEY_ENV)
    if not api_key:
        print(f"{API_KEY_ENV} is not set", file=sys.stderr)
        return 2

    batch_dir = args.batches_dir / args.batch_id
    manifest = validate_manifest(read_json(batch_dir / "manifest.json"))
    runs = {run["runId"]: validate_run(run) for run in read_jsonl(batch_dir / "runs.jsonl")}
    sheet = [validate_blind_sheet_row(row) for row in read_jsonl(batch_dir / "blind" / "sheet.jsonl")]
    key = [validate_blind_key_row(row) for row in read_jsonl(batch_dir / "blind" / "key.jsonl")]
    labels = [validate_label(label) for label in read_jsonl(batch_dir / "labels.jsonl")]
    if not sheet:
        raise RecordError("blind/sheet.jsonl is empty; build the blind sheet first")
    require_labels_locked(sheet, labels)

    model = args.model or manifest["models"].get("judge") or DEFAULT_JUDGE_MODEL
    judge_path = batch_dir / "judge.jsonl"
    done = {(j["runId"], j["judgeVersion"], j["inputDigest"]) for j in read_jsonl(judge_path)}

    emberling = EmberlingClient(args.api_url)
    http = httpx.Client(timeout=180.0)
    try:
        for key_row in key:
            run = runs.get(key_row["runId"])
            if run is None or run["outcome"] != "CLIP_DELIVERED":
                continue
            if not run.get("transcriptArtifactId") or not run.get("timelineArtifactId"):
                print(f"{key_row['blindId']}: run {run['runId']} lacks artifact ids; skipped", file=sys.stderr)
                continue
            transcript = emberling.get_artifact_text(run["runId"], run["transcriptArtifactId"])
            timeline = json.loads(emberling.get_artifact_text(run["runId"], run["timelineArtifactId"]))
            requirement = key_row["annotation"]["requirement"]
            messages = build_messages(requirement, transcript, chosen_sentences(transcript, timeline))
            digest = input_digest(messages)
            if (run["runId"], JUDGE_VERSION, digest) in done:
                print(f"{key_row['blindId']}: already judged, skipped")
                continue
            completion = call_openrouter(http, api_key, model, messages, base_url=args.openrouter_url)
            record = judge_record(args.batch_id, run, key_row["blindId"], model, messages, completion)
            append_jsonl(judge_path, record)
            verdicts = {k: v["verdict"] for k, v in (record["parsed"] or {}).items()}
            print(f"{key_row['blindId']}: judged {verdicts or record['parseError']}")
    finally:
        emberling.close()
        http.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
