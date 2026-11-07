"""Task submission, status, duplicates and restart recovery through the HTTP routes."""

from __future__ import annotations

import json
from pathlib import Path

from app.tasks import STATUS_QUEUED, STATUS_RUNNING


def transcribe_input(service) -> dict:
    return {"sourceId": service.upload()["sourceId"]}


def test_healthz_reports_ready(service) -> None:
    response = service.client.get("/healthz")
    assert response.status_code == 200, response.text
    assert response.json()["whisper"] == "ready"


def test_unknown_task_is_404(service) -> None:
    assert service.client.get("/v1/tasks/nope").status_code == 404


def test_invalid_task_request_is_rejected(service) -> None:
    bad = service.client.post(
        "/v1/tasks",
        json={"taskId": "x", "type": "FLY", "input": {}, "callbackUrl": "http://h/cb", "callbackToken": "t"},
    )
    assert bad.status_code == 422
    relative = service.submit("x", "TRANSCRIBE", {}, callback_url="/relative")
    assert relative.status_code == 422
    extra = service.client.post(
        "/v1/tasks",
        json={"taskId": "x", "type": "TRANSCRIBE", "input": {}, "callbackUrl": "http://h/cb", "callbackToken": "t", "mode": 1},
    )
    assert extra.status_code == 422
    assert service.client.get("/v1/tasks/x").status_code == 404


def test_duplicate_task_id_is_not_executed_again(service) -> None:
    task_input = transcribe_input(service)
    first = service.submit("ext_dup", "TRANSCRIBE", task_input)
    assert first.status_code == 202
    assert first.json()["status"] == STATUS_QUEUED
    finished = service.wait("ext_dup")

    again = service.submit("ext_dup", "TRANSCRIBE", task_input)
    assert again.status_code == 200
    assert again.json() == finished
    assert service.services.tasks.get("ext_dup").attempts == 1
    assert service.wait("ext_dup") == finished


def test_duplicate_task_id_with_different_input_conflicts(service) -> None:
    assert service.submit("ext_conf", "CHECK_CLIP", {"clipSha256": "a" * 64}).status_code == 202
    assert service.submit("ext_conf", "CHECK_CLIP", {"clipSha256": "b" * 64}).status_code == 409


def test_task_failure_has_a_code_and_no_result(service) -> None:
    view = service.run("ext_missing", "TRANSCRIBE", {"sourceId": "src_" + "0" * 32})
    assert view["status"] == "FAILED"
    assert view["error"]["code"] == "SOURCE_NOT_FOUND"
    assert set(view) == {"taskId", "status", "error"}


def test_status_view_of_a_succeeded_task_has_only_the_result(service) -> None:
    source = service.upload()
    view = service.run("ext_" + "a" * 32, "TRANSCRIBE", {"sourceId": source["sourceId"]})
    assert view["status"] == "SUCCEEDED"
    assert set(view) == {"taskId", "status", "result"}
    assert view["taskId"] == "ext_" + "a" * 32


def test_any_task_id_up_to_128_characters_is_accepted(service) -> None:
    odd = "ext:" + "x" * 60 + " with spaces and ünïcode"
    assert service.submit(odd, "CHECK_CLIP", {"clipSha256": "a" * 64}).status_code == 202
    assert service.wait(odd)["taskId"] == odd
    assert service.submit("y" * 129, "CHECK_CLIP", {"clipSha256": "a" * 64}).status_code == 422
    assert service.submit("", "CHECK_CLIP", {"clipSha256": "a" * 64}).status_code == 422


def test_task_record_never_stores_the_callback_token(service) -> None:
    service.run("ext_secret", "CHECK_CLIP", {"clipSha256": "c" * 64}, callback_url="http://127.0.0.1:9/cb?x=1")
    record = (service.settings.data_dir / "tasks" / "ext_secret.json").read_text()
    assert "test-token" not in record


def test_running_task_found_on_restart_is_executed_again(make_service, tmp_path: Path) -> None:
    data_dir = tmp_path / "data"
    first = make_service(data_dir)
    finished = first.run("ext_restart", "CHECK_CLIP", {"clipSha256": "d" * 64})
    assert finished["status"] == "FAILED"
    first.stop()

    # Simulate a crash mid-execution: the persisted record says RUNNING.
    record_path = data_dir / "tasks" / "ext_restart.json"
    record = json.loads(record_path.read_text())
    record.update(status=STATUS_RUNNING, result=None, error=None)
    record_path.write_text(json.dumps(record))

    second = make_service(data_dir)
    view = second.wait("ext_restart")
    assert view["status"] == "FAILED"
    assert view["error"]["code"] == "CLIP_NOT_FOUND"
    assert second.services.tasks.get("ext_restart").attempts == 2
