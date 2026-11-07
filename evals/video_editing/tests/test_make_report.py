import pytest

import make_report
from fixtures import judge_row, key_rows, labels_for, manifest, run_record, runs_fixture, write_batch, CLIP_C
from records import RecordError


def _write_full(tmp_path, with_judge=True):
    man = manifest()
    runs = runs_fixture(man)
    key = key_rows(runs)
    judge = [judge_row("run_001", "B01", True, True), judge_row("run_002", "B02", False, True)] if with_judge else None
    return write_batch(tmp_path, man, runs, labels_for(runs), key, judge)


def test_missing_label_rows_fail_loudly(tmp_path):
    man = manifest()
    runs = runs_fixture(man)
    labels = labels_for(runs)[:-1]
    write_batch(tmp_path, man, runs, labels, key_rows(runs))
    with pytest.raises(RecordError, match=r"labels.jsonl lacks rows for blinded items \['B04'\]"):
        make_report.main(["eval-001", "--batches-dir", str(tmp_path)])


def test_missing_key_fails_loudly(tmp_path):
    man = manifest()
    runs = runs_fixture(man)
    write_batch(tmp_path, man, runs, labels_for(runs))
    with pytest.raises(RecordError, match="key.jsonl is missing"):
        make_report.main(["eval-001", "--batches-dir", str(tmp_path)])


def test_report_renders_every_required_section(tmp_path):
    batch_dir = _write_full(tmp_path)
    assert make_report.main(["eval-001", "--batches-dir", str(tmp_path)]) == 0
    text = (batch_dir / "report.md").read_text(encoding="utf-8")
    for heading in ("## Outcomes", "## Failure types", "## Human scores", "## Judge agreement", "### Disagreements",
                    "## Checks and revisions", "## Cost and time", "## Runs without a clip", "## Per-run record"):
        assert heading in text, heading
    assert "4 runs" in text and "2 samples" in text
    assert "Reviewers | 1 (author)" in text
    assert "seed 20261107" in text
    assert "Rule version | v1" in text
    assert "| basic | 1 | 1 | 0 | 0 | 1/2 |" in text
    assert "| weak_opening | 1 | 0 |" in text
    assert "| asRequested | 2 | 1 | 0.5 |" in text
    assert "run_002" in text and "opening is slow" in text  # a disagreement row
    assert "0.60 USD" in text and "0.80 USD" in text
    assert "Judge cost (separate, not part of task cost): 0.04 USD over 2 calls" in text
    assert "| 02 | basic | run_003 | CANNOT_SATISFY | False | True |" in text
    assert "preliminary calibration" in text


def test_report_without_judge_says_so(tmp_path):
    batch_dir = _write_full(tmp_path, with_judge=False)
    make_report.main(["eval-001", "--batches-dir", str(tmp_path)])
    text = (batch_dir / "report.md").read_text(encoding="utf-8")
    assert "judge.jsonl is absent; no agreement computed." in text


def test_regression_report_shows_before_and_after_per_sample(tmp_path):
    _write_full(tmp_path)
    reg = manifest("reg-001", "regression")
    reg_runs = [run_record("01", "basic", "run_101", "CLIP_DELIVERED", clip=CLIP_C, man=reg),
                run_record("01", "reviewed", "run_102", "INCOMPLETE", man=reg, reason="limit reached")]
    write_batch(tmp_path, reg, reg_runs, labels_for(reg_runs), key_rows(reg_runs))
    make_report.main(["reg-001", "--batches-dir", str(tmp_path)])
    text = (tmp_path / "reg-001" / "report.md").read_text(encoding="utf-8")
    assert "## Before/after against base batch eval-001" in text
    assert "Hypothesis: the opening sentence rule" in text
    assert "| 01 | basic | CLIP_DELIVERED | CLIP_DELIVERED | 5/4/4/3 | 5/4/4/3 |" in text
    assert "| 01 | reviewed | CLIP_DELIVERED | INCOMPLETE | 5/4/4/3 | -/-/-/- |" in text
