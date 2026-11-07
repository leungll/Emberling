#!/usr/bin/env python3
"""Prepare the evaluation videos and register them with the media service.

For every sample in dataset/<version>.jsonl the script takes the local source file,
verifies its SHA-256 against the dataset, cuts the declared continuous range with ffmpeg
(stream copy, no re-encode) when the sample has one, uploads the prepared file to the
media service with the sample's cropCenterX, and records the returned reference in
dataset/<version>.prepared.json. Nothing is downloaded: the sources must already exist.

The script is idempotent: a prepared file whose digest is already recorded is not cut
again, and the media service returns the same reference for identical content.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path
from typing import Any

from media_api import MediaClient
from records import (
    PREPARED_SCHEMA,
    RecordError,
    read_json,
    read_jsonl,
    sha256_of_file,
    validate_dataset_row,
    write_json,
)

HERE = Path(__file__).resolve().parent
DEFAULT_SOURCES_DIR = Path("/Users/ll_leung/Development/GitHub/Emberling-video-data/sources")


def cut_range(ffmpeg: str, source: Path, destination: Path, start_seconds: float, duration_seconds: float) -> None:
    """Cut a continuous range with stream copy, keyframe-aligned, into a temporary file."""
    destination.parent.mkdir(parents=True, exist_ok=True)
    tmp = destination.with_name(destination.name + ".tmp.webm")
    command = [
        ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
        "-ss", str(start_seconds), "-i", str(source), "-t", str(duration_seconds),
        "-c", "copy", "-map", "0:v:0", "-map", "0:a:0", "-map_metadata", "-1",
        str(tmp),
    ]
    subprocess.run(command, check=True)
    tmp.replace(destination)


def prepare_sample(row: dict[str, Any], *, sources_dir: Path, data_dir: Path, ffmpeg: str,
                   previous: dict[str, Any] | None) -> tuple[Path, str]:
    source = sources_dir / row["localFileName"]
    if not source.exists():
        raise FileNotFoundError(f"sample {row['sampleId']}: source file {source} does not exist")
    actual = sha256_of_file(source)
    if actual != row["sourceSha256"]:
        raise RecordError(f"sample {row['sampleId']}: source digest {actual} does not match the dataset")

    cut = row.get("cutRange")
    if cut is None:
        return source, actual

    prepared = data_dir / f"{row['slug']}.cut-{int(cut['startSeconds'])}s-{int(cut['durationSeconds'])}s.webm"
    if prepared.exists() and previous and previous.get("preparedSha256") == sha256_of_file(prepared):
        return prepared, previous["preparedSha256"]
    cut_range(ffmpeg, source, prepared, cut["startSeconds"], cut["durationSeconds"])
    return prepared, sha256_of_file(prepared)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--dataset-version", default="v1")
    parser.add_argument("--sources-dir", type=Path, default=DEFAULT_SOURCES_DIR,
                        help="directory holding the downloaded source videos")
    parser.add_argument("--data-dir", type=Path, default=HERE / "data",
                        help="where prepared (cut) files are written; ignored by git")
    parser.add_argument("--media-url", default="http://localhost:9201")
    parser.add_argument("--ffmpeg", default="ffmpeg", help="ffmpeg binary used for cutting")
    parser.add_argument("--only", action="append", default=[], help="sample id to prepare; repeatable")
    args = parser.parse_args(argv)

    dataset_path = HERE / "dataset" / f"{args.dataset_version}.jsonl"
    prepared_path = HERE / "dataset" / f"{args.dataset_version}.prepared.json"
    rows = [validate_dataset_row(row) for row in read_jsonl(dataset_path)]
    if not rows:
        print(f"no samples in {dataset_path}", file=sys.stderr)
        return 1

    prepared: dict[str, Any] = {"schemaVersion": PREPARED_SCHEMA, "datasetVersion": args.dataset_version,
                                "mediaUrl": args.media_url, "samples": {}}
    if prepared_path.exists():
        previous = read_json(prepared_path)
        if isinstance(previous, dict) and isinstance(previous.get("samples"), dict):
            prepared["samples"] = previous["samples"]

    media = MediaClient(args.media_url)
    try:
        for row in rows:
            sample_id = row["sampleId"]
            if args.only and sample_id not in args.only:
                continue
            file, digest = prepare_sample(row, sources_dir=args.sources_dir, data_dir=args.data_dir,
                                          ffmpeg=args.ffmpeg, previous=prepared["samples"].get(sample_id))
            uploaded = media.upload_source(file, row["cropCenterX"])
            prepared["samples"][sample_id] = {
                "sourceId": uploaded["sourceId"],
                "sha256": uploaded["sha256"],
                "durationMs": int(uploaded["durationMs"]),
                "mediaType": uploaded["mediaType"],
                "preparedFileName": file.name,
                "preparedSha256": digest,
                "cropCenterX": row["cropCenterX"],
            }
            write_json(prepared_path, prepared)
            print(f"{sample_id}: {file.name} -> {uploaded['sourceId']} ({uploaded['durationMs']} ms)")
    finally:
        media.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
