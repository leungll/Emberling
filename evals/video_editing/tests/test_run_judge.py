import json

import pytest

import run_judge
from blinding import build_blind_mapping
from fakes import FakeEmberling, FakeOpenRouter
from fixtures import TIMELINE_A, dataset_rows, labels_for, manifest, runs_fixture, write_batch
from judge_input import build_messages, chosen_sentences, parse_verdict
from records import RecordError, read_jsonl, write_jsonl

TRANSCRIPT = "\n".join([
    "S1\t0.000\t2.500\tI became an engineer because I liked taking things apart.",
    "S2\t2.500\t6.000\tThis approach does not work",
    "S3\t6.000\t9.000\tunless the sample is large enough.",
    "S4\t9.000\t12.000\tI enjoy the problem solving most.",
])
TIMELINE = {"timelineSha256": TIMELINE_A, "segments": [{"from": "S1", "to": "S1"}, {"from": "S4", "to": "S4"}]}
GOOD_ANSWER = json.dumps({"asRequested": {"verdict": True, "score": 4, "reason": "fits"},
                          "contentQuality": {"verdict": False, "score": 2, "reason": "flat opening"}})


def _setup(tmp_path, *, drop_label=False):
    man = manifest()
    runs = runs_fixture(man)
    rows = {row["sampleId"]: row for row in dataset_rows()}
    sheet, key = build_blind_mapping(runs, rows, man["randomSeed"], lambda run: "clips/x.mp4")
    labels = labels_for(runs)
    # labels_for numbers blind ids by run order; align them with the shuffled key.
    by_run = {k["runId"]: k["blindId"] for k in key}
    for run, label in zip(runs, labels):
        label["blindId"] = by_run[run["runId"]]
    if drop_label:
        labels = labels[:-1]
    batch_dir = write_batch(tmp_path, man, runs, labels, key)
    write_jsonl(batch_dir / "blind" / "sheet.jsonl", sheet)
    return batch_dir, key


def _artifacts():
    return {"artifact_transcript": TRANSCRIPT, "artifact_" + TIMELINE_A[:32]: json.dumps(TIMELINE)}


def test_chosen_sentences_follow_timeline_segments():
    assert chosen_sentences(TRANSCRIPT, TIMELINE) == [
        ("S1", "I became an engineer because I liked taking things apart."),
        ("S4", "I enjoy the problem solving most.")]
    assert chosen_sentences(TRANSCRIPT, {"segments": [{"from": "S2", "to": "S3"}]}) == [
        ("S2", "This approach does not work"), ("S3", "unless the sample is large enough.")]


def test_judge_input_contains_requirement_transcript_and_kept_sentences_only():
    messages = build_messages("Cut a clip about engineering", TRANSCRIPT, chosen_sentences(TRANSCRIPT, TIMELINE))
    user = messages[1]["content"]
    assert "Cut a clip about engineering" in user
    assert "S3\tunless the sample is large enough." in user
    assert "Sentences kept in the clip" in user and "S4\tI enjoy the problem solving most." in user
    assert "expectedSatisfiable" not in user and "misleading" not in user.lower()


def test_parse_verdict_accepts_fenced_json_and_rejects_bad_scores():
    parsed = parse_verdict("```json\n" + GOOD_ANSWER + "\n```")
    assert parsed["contentQuality"] == {"verdict": False, "score": 2, "reason": "flat opening"}
    with pytest.raises(ValueError):
        parse_verdict('{"asRequested": {"verdict": true, "score": 9, "reason": ""}, "contentQuality": {}}')


def test_judge_refuses_without_a_label_for_every_blinded_item(tmp_path, monkeypatch):
    batch_dir, _ = _setup(tmp_path, drop_label=True)
    monkeypatch.setenv("EMBERLING_MODEL_API_KEY", "test-key-not-real")
    with FakeEmberling({}, _artifacts()) as api, FakeOpenRouter(GOOD_ANSWER) as router:
        with pytest.raises(RecordError, match="human scoring is not locked"):
            run_judge.main(["eval-001", "--batches-dir", str(tmp_path), "--api-url", api.url,
                            "--openrouter-url", router.url])
        assert router.requests == []
    assert not (batch_dir / "judge.jsonl").exists()


def test_judge_refuses_without_api_key(tmp_path, monkeypatch, capsys):
    _setup(tmp_path)
    monkeypatch.delenv("EMBERLING_MODEL_API_KEY", raising=False)
    assert run_judge.main(["eval-001", "--batches-dir", str(tmp_path)]) == 2
    assert "EMBERLING_MODEL_API_KEY is not set" in capsys.readouterr().err


def test_judge_scores_delivered_runs_only_and_never_writes_the_key(tmp_path, monkeypatch):
    batch_dir, key = _setup(tmp_path)
    secret = "sk-or-test-secret-value"
    monkeypatch.setenv("EMBERLING_MODEL_API_KEY", secret)
    with FakeEmberling({}, _artifacts()) as api, FakeOpenRouter(GOOD_ANSWER) as router:
        assert run_judge.main(["eval-001", "--batches-dir", str(tmp_path), "--api-url", api.url,
                               "--openrouter-url", router.url]) == 0
        assert len(router.requests) == 2  # run_001 and run_002 delivered; run_003/004 did not
        assert router.requests[0]["headers"]["authorization"] == f"Bearer {secret}"
        sent = json.loads(router.requests[0]["body"])
        assert sent["model"] == "anthropic/claude-opus-5.5"
        assert sent["usage"] == {"include": True}
        assert "expectedSatisfiable" not in router.requests[0]["body"].decode()
        rows = read_jsonl(batch_dir / "judge.jsonl")
        assert sorted(r["runId"] for r in rows) == ["run_001", "run_002"]
        by_blind = {k["runId"]: k["blindId"] for k in key}
        assert all(r["blindId"] == by_blind[r["runId"]] for r in rows)
        assert rows[0]["parsed"]["asRequested"]["verdict"] is True
        assert rows[0]["usage"]["total_tokens"] == 1290
        assert rows[0]["cost"] == {"amount": "0.0213", "currency": "USD"}
        assert rows[0]["judgeVersion"] == "judge/v1" and len(rows[0]["inputDigest"]) == 64
        # Re-running skips runs already judged under the same input digest.
        assert run_judge.main(["eval-001", "--batches-dir", str(tmp_path), "--api-url", api.url,
                               "--openrouter-url", router.url]) == 0
        assert len(router.requests) == 2
        assert len(read_jsonl(batch_dir / "judge.jsonl")) == 2
    for path in batch_dir.rglob("*"):
        if path.is_file():
            assert secret not in path.read_text(encoding="utf-8"), path


def test_unparseable_answer_is_recorded_raw_with_parse_error(tmp_path, monkeypatch):
    batch_dir, _ = _setup(tmp_path)
    monkeypatch.setenv("EMBERLING_MODEL_API_KEY", "k")
    with FakeEmberling({}, _artifacts()) as api, FakeOpenRouter("I cannot decide.") as router:
        run_judge.main(["eval-001", "--batches-dir", str(tmp_path), "--api-url", api.url, "--openrouter-url", router.url])
    rows = read_jsonl(batch_dir / "judge.jsonl")
    assert rows[0]["parsed"] is None and "no JSON object" in rows[0]["parseError"]
    assert rows[0]["rawResponse"] == "I cannot decide."


def test_provider_error_stops_the_judge_without_a_record(tmp_path, monkeypatch):
    batch_dir, _ = _setup(tmp_path)
    monkeypatch.setenv("EMBERLING_MODEL_API_KEY", "k")
    with FakeEmberling({}, _artifacts()) as api, FakeOpenRouter(GOOD_ANSWER, fail=True) as router:
        with pytest.raises(RuntimeError, match="status 429"):
            run_judge.main(["eval-001", "--batches-dir", str(tmp_path), "--api-url", api.url, "--openrouter-url", router.url])
    assert not (batch_dir / "judge.jsonl").exists()
