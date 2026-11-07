"""Versioned record schemas for the video-editing evaluation.

Every record written by the scripts in this directory is a JSON object that carries a
`schemaVersion`. The meaning of each field is fixed by this module so the files can later
be loaded into a database without reinterpretation. Validation is deliberately strict
about required keys and value kinds and lenient about extra keys, so a newer writer can add
a field without breaking an older reader.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any, Iterable

DATASET_SCHEMA = "dataset/v1"
PREPARED_SCHEMA = "prepared/v1"
MANIFEST_SCHEMA = "batch-manifest/v1"
RUN_SCHEMA = "run/v1"
LABEL_SCHEMA = "label/v1"
BLIND_SHEET_SCHEMA = "blind-sheet/v1"
BLIND_KEY_SCHEMA = "blind-key/v1"
JUDGE_SCHEMA = "judge/v1"

BATCH_TYPES = ("eval", "regression", "showcase")
VARIANTS = ("basic", "reviewed")
OUTCOMES = ("CLIP_DELIVERED", "CANNOT_SATISFY", "INCOMPLETE", "FAILED")

# Human dimensions. The two marked `judged` are the ones the judge also scores; agreement
# is computed on their binary verdicts only.
HUMAN_DIMENSIONS = ("notBroken", "asRequested", "contentQuality", "presentationQuality")
JUDGED_DIMENSIONS = ("asRequested", "contentQuality")

# Failure tags a reviewer may attach to a clip. A label may carry none or several.
FAILURE_TAGS = (
    "misleading_cut",
    "missing_qualifier",
    "off_requirement",
    "not_understandable",
    "weak_opening",
    "bad_pacing",
    "subtitle_problem",
    "framing_problem",
    "technical_defect",
    "reason_mismatch",
    "other",
)


class RecordError(ValueError):
    """A record does not satisfy its schema."""


def _require(record: dict[str, Any], key: str, kinds: type | tuple[type, ...], where: str) -> Any:
    if key not in record:
        raise RecordError(f"{where}: missing field {key!r}")
    value = record[key]
    if not isinstance(value, kinds) or (isinstance(value, bool) and bool not in _as_tuple(kinds)):
        raise RecordError(f"{where}: field {key!r} has type {type(value).__name__}, want {kinds}")
    return value


def _as_tuple(kinds: type | tuple[type, ...]) -> tuple[type, ...]:
    return kinds if isinstance(kinds, tuple) else (kinds,)


def _require_schema(record: dict[str, Any], schema: str, where: str) -> None:
    if not isinstance(record, dict):
        raise RecordError(f"{where}: record is not an object")
    if record.get("schemaVersion") != schema:
        raise RecordError(f"{where}: schemaVersion {record.get('schemaVersion')!r}, want {schema!r}")


def _require_choice(record: dict[str, Any], key: str, choices: Iterable[str], where: str) -> str:
    value = _require(record, key, str, where)
    if value not in choices:
        raise RecordError(f"{where}: field {key!r} is {value!r}, want one of {list(choices)}")
    return value


def _require_optional(record: dict[str, Any], key: str, kinds: type | tuple[type, ...], where: str) -> Any:
    if key not in record:
        raise RecordError(f"{where}: missing field {key!r}")
    value = record[key]
    if value is None:
        return None
    if not isinstance(value, kinds):
        raise RecordError(f"{where}: field {key!r} has type {type(value).__name__}, want {kinds} or null")
    return value


def _require_score(value: Any, where: str) -> None:
    if value is None:
        return
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= 5:
        raise RecordError(f"{where}: score {value!r} is not an integer from 1 to 5")


# ---- dataset -------------------------------------------------------------------------


def validate_dataset_row(row: dict[str, Any]) -> dict[str, Any]:
    where = f"dataset row {row.get('sampleId')!r}"
    _require_schema(row, DATASET_SCHEMA, where)
    _require(row, "sampleId", str, where)
    _require(row, "slug", str, where)
    _require(row, "sourceTitle", str, where)
    _require(row, "sourceUrl", str, where)
    _require(row, "license", str, where)
    attribution = _require(row, "attribution", dict, where)
    _require(attribution, "artist", str, where + " attribution")
    _require(attribution, "note", str, where + " attribution")
    _require(row, "localFileName", str, where)
    sha = _require(row, "sourceSha256", str, where)
    if len(sha) != 64:
        raise RecordError(f"{where}: sourceSha256 must be 64 hex characters")
    cut = _require_optional(row, "cutRange", dict, where)
    if cut is not None:
        _require(cut, "startSeconds", (int, float), where + " cutRange")
        _require(cut, "durationSeconds", (int, float), where + " cutRange")
    crop = _require(row, "cropCenterX", (int, float), where)
    if not 0 <= crop <= 1:
        raise RecordError(f"{where}: cropCenterX must be between 0 and 1")
    annotation = _require(row, "annotation", dict, where)
    aw = where + " annotation"
    _require(annotation, "requirement", str, aw)
    _require(annotation, "expectedSatisfiable", bool, aw)
    _require(annotation, "misleadingSentences", list, aw)
    _require(annotation, "mustKeepQualifiers", list, aw)
    _require(annotation, "annotatedBy", str, aw)
    _require_choice(annotation, "annotationStatus", ("draft", "final"), aw)
    return row


def validate_prepared(record: dict[str, Any]) -> dict[str, Any]:
    where = "prepared dataset"
    _require_schema(record, PREPARED_SCHEMA, where)
    _require(record, "datasetVersion", str, where)
    _require(record, "mediaUrl", str, where)
    samples = _require(record, "samples", dict, where)
    for sample_id, prepared in samples.items():
        pw = f"{where} sample {sample_id!r}"
        if not isinstance(prepared, dict):
            raise RecordError(f"{pw}: not an object")
        _require(prepared, "sourceId", str, pw)
        _require(prepared, "sha256", str, pw)
        _require(prepared, "durationMs", int, pw)
        _require(prepared, "mediaType", str, pw)
        _require(prepared, "preparedFileName", str, pw)
    return record


# ---- batch manifest ------------------------------------------------------------------


def validate_manifest(record: dict[str, Any]) -> dict[str, Any]:
    where = "batch manifest"
    _require_schema(record, MANIFEST_SCHEMA, where)
    _require(record, "batchId", str, where)
    batch_type = _require_choice(record, "type", BATCH_TYPES, where)
    _require(record, "datasetVersion", str, where)
    definitions = _require(record, "definitions", dict, where)
    for variant in VARIANTS:
        if variant not in definitions:
            raise RecordError(f"{where}: definitions lacks variant {variant!r}")
        dw = f"{where} definitions.{variant}"
        _require(definitions[variant], "workflowId", str, dw)
        _require(definitions[variant], "version", int, dw)
    models = _require(record, "models", dict, where)
    _require(models, "decision", str, where + " models")
    _require(models, "judge", str, where + " models")
    _require(record, "promptVersion", str, where)
    _require(record, "toolSchemaDigest", str, where)
    _require(record, "ruleVersion", str, where)
    _require(record, "randomSeed", int, where)
    if batch_type == "regression":
        _require(record, "baseBatchId", str, where)
        _require(record, "hypothesis", str, where)
    return record


def manifest_digest(record: dict[str, Any]) -> str:
    """Digest of the frozen manifest, so a run record can prove which manifest it ran under."""
    canonical = json.dumps(record, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


# ---- runs ----------------------------------------------------------------------------


def validate_run(record: dict[str, Any]) -> dict[str, Any]:
    where = f"run record {record.get('sampleId')!r}/{record.get('variant')!r}"
    _require_schema(record, RUN_SCHEMA, where)
    _require(record, "batchId", str, where)
    _require(record, "manifestDigest", str, where)
    _require(record, "sampleId", str, where)
    _require_choice(record, "variant", VARIANTS, where)
    _require(record, "runId", str, where)
    _require_optional(record, "agentNodeRunId", str, where)
    _require_optional(record, "agentRunId", str, where)
    _require_choice(record, "outcome", OUTCOMES, where)
    _require(record, "runStatus", str, where)
    _require_optional(record, "clipSha256", str, where)
    _require_optional(record, "clipUrl", str, where)
    _require_optional(record, "failureReason", str, where)
    _require_optional(record, "cannotSatisfyReason", str, where)
    cost = _require(record, "cost", dict, where)
    _require(cost, "totals", list, where + " cost")
    _require(cost, "unknownCalls", int, where + " cost")
    _require_optional(record, "elapsedSeconds", (int, float), where)
    revisions = _require(record, "revisions", dict, where)
    _require(revisions, "mediaChecked", int, where + " revisions")
    _require(revisions, "contentReviewed", int, where + " revisions")
    checks = _require(record, "checks", dict, where)
    for key in ("mediaCheckedTotal", "mediaCheckedFailed", "contentReviewedTotal", "contentReviewedFailed"):
        _require(checks, key, int, where + " checks")
    _require(record, "turnsUsed", int, where)
    _require_optional(record, "transcriptArtifactId", str, where)
    _require_optional(record, "timelineArtifactId", str, where)
    _require_optional(record, "timelineSha256", str, where)
    _require(record, "recordedAt", str, where)
    return record


# ---- blind sheet and key -------------------------------------------------------------


def validate_blind_sheet_row(record: dict[str, Any]) -> dict[str, Any]:
    where = f"blind sheet row {record.get('blindId')!r}"
    _require_schema(record, BLIND_SHEET_SCHEMA, where)
    _require(record, "blindId", str, where)
    _require_choice(record, "kind", ("clip", "no_clip"), where)
    _require(record, "requirement", str, where)
    _require(record, "sourceTitle", str, where)
    _require(record, "sourceUrl", str, where)
    if record["kind"] == "clip":
        _require(record, "clipSha256", str, where)
        _require(record, "clipSrc", str, where)
    else:
        _require(record, "statedOutcome", str, where)
        _require_optional(record, "statedReason", str, where)
    for forbidden in ("variant", "runId", "annotation", "expectedSatisfiable"):
        if forbidden in record:
            raise RecordError(f"{where}: must not carry {forbidden!r}")
    return record


def validate_blind_key_row(record: dict[str, Any]) -> dict[str, Any]:
    where = f"blind key row {record.get('blindId')!r}"
    _require_schema(record, BLIND_KEY_SCHEMA, where)
    _require(record, "blindId", str, where)
    _require(record, "runId", str, where)
    _require(record, "sampleId", str, where)
    _require_choice(record, "variant", VARIANTS, where)
    _require(record, "annotation", dict, where)
    return record


# ---- labels --------------------------------------------------------------------------


def validate_label(record: dict[str, Any]) -> dict[str, Any]:
    where = f"label {record.get('blindId')!r}"
    _require_schema(record, LABEL_SCHEMA, where)
    _require(record, "blindId", str, where)
    _require(record, "reviewer", str, where)
    scores = _require(record, "scores", dict, where)
    for dimension in HUMAN_DIMENSIONS:
        if dimension not in scores:
            raise RecordError(f"{where}: scores lacks {dimension!r}")
        _require_score(scores[dimension], f"{where} scores.{dimension}")
    verdicts = _require(record, "verdicts", dict, where)
    for dimension in JUDGED_DIMENSIONS:
        if dimension not in verdicts:
            raise RecordError(f"{where}: verdicts lacks {dimension!r}")
        if verdicts[dimension] is not None and not isinstance(verdicts[dimension], bool):
            raise RecordError(f"{where}: verdicts.{dimension} must be true, false or null")
    _require(record, "issues", str, where)
    tags = _require(record, "failureTags", list, where)
    for tag in tags:
        if tag not in FAILURE_TAGS:
            raise RecordError(f"{where}: unknown failure tag {tag!r}")
    _require(record, "checkGap", bool, where)
    _require_optional(record, "reasonMatchesAnnotation", bool, where)
    return record


# ---- judge ---------------------------------------------------------------------------


def validate_judge(record: dict[str, Any]) -> dict[str, Any]:
    where = f"judge record {record.get('runId')!r}"
    _require_schema(record, JUDGE_SCHEMA, where)
    _require(record, "judgeVersion", str, where)
    _require(record, "batchId", str, where)
    _require(record, "runId", str, where)
    _require(record, "blindId", str, where)
    _require(record, "model", str, where)
    _require(record, "inputDigest", str, where)
    _require(record, "rawResponse", str, where)
    parsed = _require_optional(record, "parsed", dict, where)
    if parsed is not None:
        for dimension in JUDGED_DIMENSIONS:
            if dimension not in parsed:
                raise RecordError(f"{where}: parsed lacks {dimension!r}")
            entry = parsed[dimension]
            if not isinstance(entry, dict):
                raise RecordError(f"{where}: parsed.{dimension} is not an object")
            _require(entry, "verdict", bool, f"{where} parsed.{dimension}")
            _require_score(_require(entry, "score", int, f"{where} parsed.{dimension}"), f"{where} parsed.{dimension}")
            _require(entry, "reason", str, f"{where} parsed.{dimension}")
    _require_optional(record, "usage", dict, where)
    _require_optional(record, "cost", dict, where)
    _require(record, "createdAt", str, where)
    return record


# ---- file helpers --------------------------------------------------------------------


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    """Read every non-empty line of a JSONL file as an object. A missing file is empty."""
    if not path.exists():
        return []
    rows: list[dict[str, Any]] = []
    with path.open("r", encoding="utf-8") as handle:
        for line_no, line in enumerate(handle, start=1):
            line = line.strip()
            if not line:
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError as err:
                raise RecordError(f"{path}:{line_no}: invalid JSON: {err}") from err
            if not isinstance(value, dict):
                raise RecordError(f"{path}:{line_no}: not an object")
            rows.append(value)
    return rows


def append_jsonl(path: Path, record: dict[str, Any]) -> None:
    """Append one record and flush, so a crash after the write loses nothing."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n")
        handle.flush()


def write_jsonl(path: Path, records: Iterable[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with tmp.open("w", encoding="utf-8") as handle:
        for record in records:
            handle.write(json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n")
    tmp.replace(path)


def read_json(path: Path) -> Any:
    with path.open("r", encoding="utf-8") as handle:
        return json.load(handle)


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with tmp.open("w", encoding="utf-8") as handle:
        json.dump(value, handle, ensure_ascii=False, sort_keys=True, indent=2)
        handle.write("\n")
    tmp.replace(path)


def sha256_of_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()
