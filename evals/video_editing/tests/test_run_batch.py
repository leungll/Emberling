import pytest

import run_batch
from fakes import FakeEmberling
from fixtures import CLIP_A, CLIP_B, dataset_rows, manifest, prepared, write_batch
from records import RecordError, read_json, read_jsonl, write_json, write_jsonl

REQ = {row["sampleId"]: row["annotation"]["requirement"] for row in dataset_rows()}


def scripts():
    return {
        REQ["01"]: {"outcome": "CLIP_DELIVERED", "clipSha256": CLIP_A, "mediaChecked": [False, True, True], "turns": 7},
        REQ["02"]: {"outcome": "CANNOT_SATISFY", "cannotSatisfyReason": "no such claim in the interview", "turns": 3},
        REQ["03"]: {"outcome": "INCOMPLETE", "mediaChecked": [False, False, False, False], "turns": 12},
        REQ["04"]: {"outcome": "FAILED", "errorCode": "MODEL_ERROR"},
        REQ["05"]: {"outcome": "CLIP_DELIVERED", "clipSha256": CLIP_B, "mediaChecked": [True, True],
                    "contentReviewed": [False, True]},
        REQ["06"]: {"outcome": "CLIP_DELIVERED", "clipSha256": CLIP_B, "mediaChecked": [True, True]},
    }


def _setup(tmp_path, batch_type="eval"):
    man = manifest(batch_type=batch_type)
    batches = tmp_path / "batches"
    dataset = tmp_path / "dataset"
    write_batch(batches, man)
    write_jsonl(dataset / "v1.jsonl", dataset_rows())
    write_json(dataset / "v1.prepared.json", prepared())
    return man, batches, dataset


def _run(batches, dataset, api, batch_id="eval-001", *extra):
    return run_batch.main([batch_id, "--batches-dir", str(batches), "--dataset-dir", str(dataset),
                           "--api-url", api.url, "--media-url", "http://media.test", "--poll-seconds", "0", *extra])


def test_batch_records_every_pair_with_outcome_cost_and_revisions(tmp_path):
    man, batches, dataset = _setup(tmp_path)
    with FakeEmberling(scripts()) as api:
        assert _run(batches, dataset, api, "eval-001", "--only", "01", "--only", "02", "--only", "03", "--only", "05") == 0
        runs = read_jsonl(batches / "eval-001" / "runs.jsonl")
        assert [(r["sampleId"], r["variant"]) for r in runs] == [
            ("01", "basic"), ("01", "reviewed"), ("02", "basic"), ("02", "reviewed"),
            ("03", "basic"), ("03", "reviewed"), ("05", "basic"), ("05", "reviewed")]
        by_pair = {(r["sampleId"], r["variant"]): r for r in runs}
        delivered = by_pair[("01", "basic")]
        assert delivered["outcome"] == "CLIP_DELIVERED"
        assert delivered["clipSha256"] == CLIP_A
        assert delivered["clipUrl"] == f"http://media.test/v1/clips/{CLIP_A}"
        assert delivered["revisions"] == {"mediaChecked": 1, "contentReviewed": 0}
        assert delivered["checks"]["mediaCheckedTotal"] == 3
        assert delivered["turnsUsed"] == 7
        assert delivered["cost"] == {"totals": [{"amount": "0.30", "currency": "USD"}], "unknownCalls": 0}
        assert delivered["elapsedSeconds"] == 210.0
        assert delivered["transcriptArtifactId"] == "artifact_transcript"
        assert delivered["timelineArtifactId"] == "artifact_" + "t" * 32
        assert delivered["agentRunId"] == "ar_001" and delivered["agentNodeRunId"] == "nr_agent_001"
        cannot = by_pair[("02", "reviewed")]
        assert cannot["outcome"] == "CANNOT_SATISFY"
        assert cannot["cannotSatisfyReason"] == "no such claim in the interview"
        assert cannot["clipSha256"] is None
        incomplete = by_pair[("03", "basic")]
        assert incomplete["outcome"] == "INCOMPLETE"
        assert "CLIP_NOT_CHECKED" in incomplete["failureReason"] and "TASK_INCOMPLETE" in incomplete["failureReason"]
        assert incomplete["revisions"]["mediaChecked"] == 4
        reviewed = by_pair[("05", "reviewed")]
        assert reviewed["revisions"] == {"mediaChecked": 0, "contentReviewed": 1}
        # The Run input carries the media reference, digest, duration, requirement and goal.
        created = [r for r in api.requests if r["method"] == "POST"]
        assert len(created) == 8
        task = __import__("json").loads(created[0]["body"])["input"]["task"]
        assert set(task) == {"sourceId", "sourceSha256", "durationMs", "requirement", "goal"}
        assert task["goal"] == "DELIVER_CLIP" and task["sourceId"] == "src_01"
        assert created[0]["headers"]["idempotency-key"] == "eval-001:01:basic:1"


def test_failed_run_is_recorded_not_replaced_and_resume_skips_recorded_pairs(tmp_path):
    man, batches, dataset = _setup(tmp_path)
    with FakeEmberling(scripts()) as api:
        assert _run(batches, dataset, api, "eval-001", "--only", "04") == 0
        runs = read_jsonl(batches / "eval-001" / "runs.jsonl")
        assert [r["outcome"] for r in runs] == ["FAILED", "FAILED"]
        assert "MODEL_ERROR" in runs[0]["failureReason"]
        posts_before = sum(1 for r in api.requests if r["method"] == "POST")
        # Second invocation: nothing is created again and the failed records stay.
        assert _run(batches, dataset, api, "eval-001", "--only", "04") == 0
        assert sum(1 for r in api.requests if r["method"] == "POST") == posts_before
        assert read_jsonl(batches / "eval-001" / "runs.jsonl") == runs


def test_resume_after_crash_mid_batch_continues_with_the_missing_pairs(tmp_path):
    man, batches, dataset = _setup(tmp_path)
    with FakeEmberling(scripts()) as api:
        api.fail_create = False
        _run(batches, dataset, api, "eval-001", "--only", "01")
        api.fail_create = True
        with pytest.raises(Exception):
            _run(batches, dataset, api, "eval-001", "--only", "01", "--only", "02")
        assert len(read_jsonl(batches / "eval-001" / "runs.jsonl")) == 2
        api.fail_create = False
        _run(batches, dataset, api, "eval-001", "--only", "01", "--only", "02")
        pairs = [(r["sampleId"], r["variant"]) for r in read_jsonl(batches / "eval-001" / "runs.jsonl")]
        assert pairs == [("01", "basic"), ("01", "reviewed"), ("02", "basic"), ("02", "reviewed")]


def test_eval_batch_refuses_rerun_and_a_changed_manifest(tmp_path):
    man, batches, dataset = _setup(tmp_path)
    with FakeEmberling(scripts()) as api:
        _run(batches, dataset, api, "eval-001", "--only", "01")
        with pytest.raises(RecordError, match="eval batch: a pair is never run twice"):
            _run(batches, dataset, api, "eval-001", "--rerun", "01:basic")
        edited = read_json(batches / "eval-001" / "manifest.json")
        edited["promptVersion"] = "clip-agent/v2"
        write_json(batches / "eval-001" / "manifest.json", edited)
        with pytest.raises(RecordError, match="different manifest"):
            _run(batches, dataset, api, "eval-001", "--only", "02")
        assert len(read_jsonl(batches / "eval-001" / "runs.jsonl")) == 2


def test_regression_batch_rerun_appends_and_keeps_the_old_record(tmp_path):
    man, batches, dataset = _setup(tmp_path, batch_type="regression")
    with FakeEmberling(scripts()) as api:
        _run(batches, dataset, api, "eval-001", "--only", "01")
        with pytest.raises(RecordError, match="has no record yet"):
            _run(batches, dataset, api, "eval-001", "--rerun", "02:basic")
        _run(batches, dataset, api, "eval-001", "--only", "01", "--rerun", "01:basic")
        runs = read_jsonl(batches / "eval-001" / "runs.jsonl")
        assert [(r["sampleId"], r["variant"], r["runId"]) for r in runs] == [
            ("01", "basic", "run_001"), ("01", "reviewed", "run_002"), ("01", "basic", "run_003")]
        posts = [r for r in api.requests if r["method"] == "POST"]
        assert posts[-1]["headers"]["idempotency-key"] == "eval-001:01:basic:2"


def test_unprepared_sample_is_refused_before_any_run_is_created(tmp_path):
    man, batches, dataset = _setup(tmp_path)
    prepared_record = read_json(dataset / "v1.prepared.json")
    del prepared_record["samples"]["01"]
    write_json(dataset / "v1.prepared.json", prepared_record)
    with FakeEmberling(scripts()) as api:
        with pytest.raises(RecordError, match="sample 01 is not prepared"):
            _run(batches, dataset, api)
        assert not any(r["method"] == "POST" for r in api.requests)
