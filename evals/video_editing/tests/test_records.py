import pytest

import records
from fixtures import dataset_rows, judge_row, label, manifest, run_record


def test_dataset_v1_rows_validate_and_cover_six_samples():
    rows = [records.validate_dataset_row(row) for row in dataset_rows()]
    assert [row["sampleId"] for row in rows] == ["01", "02", "03", "04", "05", "06"]
    assert sum(not row["annotation"]["expectedSatisfiable"] for row in rows) == 1
    assert rows[4]["cutRange"] == {"startSeconds": 60, "durationSeconds": 480}
    assert rows[5]["cutRange"] == {"startSeconds": 120, "durationSeconds": 480}
    assert all(row["cutRange"] is None for row in rows[:4])
    assert all(row["license"] == "CC BY 3.0" and "edited excerpt" in row["attribution"]["note"] for row in rows[4:])


def test_dataset_row_rejects_missing_annotation_field():
    row = dataset_rows()[0]
    del row["annotation"]["mustKeepQualifiers"]
    with pytest.raises(records.RecordError, match="mustKeepQualifiers"):
        records.validate_dataset_row(row)


def test_manifest_requires_base_batch_for_regression():
    with pytest.raises(records.RecordError, match="baseBatchId"):
        records.validate_manifest(manifest("reg-001", "regression", baseBatchId=None) | {"baseBatchId": None})
    records.validate_manifest(manifest("reg-001", "regression"))


def test_manifest_rejects_unknown_type_and_wrong_schema():
    with pytest.raises(records.RecordError, match="type"):
        records.validate_manifest(manifest(batch_type="pilot"))
    with pytest.raises(records.RecordError, match="schemaVersion"):
        records.validate_manifest(manifest(schemaVersion="batch-manifest/v0"))


def test_manifest_digest_is_order_independent():
    a = manifest()
    b = dict(reversed(list(a.items())))
    assert records.manifest_digest(a) == records.manifest_digest(b)
    assert records.manifest_digest(a) != records.manifest_digest(manifest(randomSeed=1))


def test_run_record_rejects_unknown_outcome():
    run = run_record("01", "basic", "run_001", "CLIP_DELIVERED", clip="a" * 64)
    run["outcome"] = "DELIVERED"
    with pytest.raises(records.RecordError, match="outcome"):
        records.validate_run(run)


def test_label_rejects_out_of_range_score_and_unknown_tag():
    bad = label("B01", scores={"notBroken": 6, "asRequested": 4, "contentQuality": 4, "presentationQuality": 4})
    with pytest.raises(records.RecordError, match="1 to 5"):
        records.validate_label(bad)
    with pytest.raises(records.RecordError, match="unknown failure tag"):
        records.validate_label(label("B01", tags=["looks_bad"]))


def test_judge_record_allows_unparsed_answer_but_checks_parsed_shape():
    row = judge_row("run_001", "B01", True, False)
    records.validate_judge(row)
    row["parsed"] = None
    records.validate_judge(row)
    row["parsed"] = {"asRequested": {"verdict": "yes", "score": 4, "reason": ""}}
    with pytest.raises(records.RecordError):
        records.validate_judge(row)


def test_jsonl_round_trip_and_append(tmp_path):
    path = tmp_path / "x.jsonl"
    records.append_jsonl(path, {"a": 1})
    records.append_jsonl(path, {"b": "two"})
    assert records.read_jsonl(path) == [{"a": 1}, {"b": "two"}]
    assert records.read_jsonl(tmp_path / "missing.jsonl") == []
