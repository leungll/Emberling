import report_stats as stats
from fixtures import judge_row, key_rows, labels_for, runs_fixture


def _labels_by_run(runs):
    key = key_rows(runs)
    by_blind = {k["blindId"]: k["runId"] for k in key}
    return {by_blind[label["blindId"]]: label for label in labels_for(runs)}, {k["runId"]: k for k in key}


def test_failed_and_incomplete_runs_stay_in_the_denominator():
    runs = runs_fixture()
    counts = stats.outcome_counts(runs)
    assert counts["basic"] == {"CLIP_DELIVERED": 1, "CANNOT_SATISFY": 1, "INCOMPLETE": 0, "FAILED": 0}
    assert counts["reviewed"] == {"CLIP_DELIVERED": 1, "CANNOT_SATISFY": 0, "INCOMPLETE": 0, "FAILED": 1}
    rates = stats.check_rates(runs)
    # Both reviewed runs count: the failed one triggered no revision, so the rate is 1/2.
    assert rates["reviewed"]["runs"] == 2
    assert rates["reviewed"]["revisionTriggerRate"] == 0.5
    assert rates["basic"]["revisionTriggerRate"] == 0.5
    # basic: run_001 has 3 media facts (1 failed) and run_003 has 2 passed -> 4/5.
    assert rates["basic"]["checkFacts"] == 5
    assert rates["basic"]["checkPassRate"] == 0.8


def test_human_scores_only_average_scored_clips():
    runs = runs_fixture()
    labels_by_run, _ = _labels_by_run(runs)
    scores = stats.human_scores(labels_by_run, runs)
    assert scores["basic"]["asRequested"] == {"mean": 4.0, "n": 1}
    assert scores["reviewed"]["notBroken"] == {"mean": 5.0, "n": 1}


def test_agreement_counts_only_shared_verdicts_and_lists_disagreements():
    runs = runs_fixture()
    labels_by_run, _ = _labels_by_run(runs)
    judge_by_run = {"run_001": judge_row("run_001", "B01", True, True),
                    "run_002": judge_row("run_002", "B02", False, True)}
    result = stats.agreement(labels_by_run, judge_by_run, runs)
    per = result["perDimension"]
    # asRequested: human True/True, judge True/False -> 1 of 2 agree.
    assert per["asRequested"] == {"compared": 2, "agreed": 1, "rate": 0.5}
    # contentQuality: human basic False, reviewed True; judge True/True -> 1 of 2 agree.
    assert per["contentQuality"] == {"compared": 2, "agreed": 1, "rate": 0.5}
    pairs = {(d["dimension"], d["runId"]) for d in result["disagreements"]}
    assert pairs == {("asRequested", "run_002"), ("contentQuality", "run_001")}


def test_agreement_without_judge_rows_compares_nothing():
    runs = runs_fixture()
    labels_by_run, _ = _labels_by_run(runs)
    result = stats.agreement(labels_by_run, {}, runs)
    assert result["perDimension"]["asRequested"] == {"compared": 0, "agreed": 0, "rate": None}
    assert result["disagreements"] == []


def test_cost_is_summed_from_api_totals_and_judge_cost_is_separate():
    runs = runs_fixture()
    cost = stats.cost_totals(runs)
    assert cost["basic"]["totals"] == {"USD": "0.60"}
    assert cost["reviewed"]["totals"] == {"USD": "0.80"}
    judge = stats.judge_cost([judge_row("run_001", "B01", True, True), judge_row("run_002", "B02", True, True)])
    assert judge == {"amount": "0.04", "currency": "USD", "calls": 2, "callsWithKnownCost": 2}


def test_cannot_satisfy_rows_carry_annotation_expectation_and_reviewer_match():
    runs = runs_fixture()
    labels_by_run, key_by_run = _labels_by_run(runs)
    rows = stats.cannot_satisfy(labels_by_run, key_by_run, runs)
    assert [(r["runId"], r["expectedSatisfiable"], r["reasonMatchesAnnotation"]) for r in rows] == \
        [("run_003", False, True), ("run_004", False, False)]
