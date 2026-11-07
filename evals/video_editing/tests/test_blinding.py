import json

from blinding import build_blind_mapping, unblind
from fixtures import dataset_rows, manifest, runs_fixture
from make_blind_sheet import render_page
from records import validate_blind_key_row, validate_blind_sheet_row

ROWS = {row["sampleId"]: row for row in dataset_rows()}


def _mapping(seed=20261107):
    return build_blind_mapping(runs_fixture(), ROWS, seed, lambda run: f"clips/{run['clipSha256']}.mp4")


def test_blind_mapping_round_trips_to_every_run():
    sheet, key = _mapping()
    for row in sheet:
        validate_blind_sheet_row(row)
    for row in key:
        validate_blind_key_row(row)
    by_blind = unblind(key)
    assert {row["blindId"] for row in sheet} == set(by_blind)
    assert sorted(by_blind[b]["runId"] for b in by_blind) == ["run_001", "run_002", "run_003", "run_004"]
    for row in sheet:
        run = by_blind[row["blindId"]]
        if row["kind"] == "clip":
            assert run["outcome"] == "CLIP_DELIVERED"
            assert row["clipSrc"].endswith(row["clipSha256"] + ".mp4")
        else:
            assert run["outcome"] in ("CANNOT_SATISFY", "FAILED")
            assert row["statedOutcome"] == run["outcome"]


def test_sheet_never_carries_variant_run_id_or_annotation():
    sheet, _ = _mapping()
    text = json.dumps(sheet)
    for forbidden in ("variant", "basic", "reviewed", "run_00", "annotation", "expectedSatisfiable", "wf_"):
        assert forbidden not in text, forbidden


def test_page_hides_variant_and_annotation_but_shows_requirement_and_source():
    sheet, key = _mapping()
    page = render_page("eval-001", sheet)
    for forbidden in ("basic", "reviewed", "run_00", "expectedSatisfiable", "misleadingSentences"):
        assert forbidden not in page, forbidden
    assert "social media" in page  # the requirement text is shown
    assert "<video" in page and 'controls' in page
    assert "Does the stated reason match the annotation?" in page
    assert ROWS["01"]["sourceUrl"] in page
    assert "the interview never discusses social media" in page  # stated reason of the no-clip run


def test_shuffle_is_deterministic_for_the_seed_and_changes_with_it():
    first, _ = _mapping()
    second, _ = _mapping()
    assert [r["blindId"] + r["kind"] + r["requirement"] for r in first] == \
        [r["blindId"] + r["kind"] + r["requirement"] for r in second]
    _, key_a = _mapping(1)
    _, key_b = _mapping(2)
    assert [k["runId"] for k in key_a] != [k["runId"] for k in key_b]


def test_manifest_seed_is_what_the_script_uses():
    assert manifest()["randomSeed"] == 20261107
