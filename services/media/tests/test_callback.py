"""Callback delivery: shape, retry with backoff, and the task state staying put."""

from __future__ import annotations

import threading

from app.callback import CALLBACK_TOKEN_HEADER, CallbackDeliverer, callback_target


class FakeClock:
    def __init__(self) -> None:
        self.now = 0.0
        self.sleeps: list[float] = []

    def sleep(self, seconds: float) -> None:
        self.sleeps.append(seconds)
        self.now += seconds


def test_callback_carries_task_id_token_and_payload(callback_receiver) -> None:
    receiver = callback_receiver()
    deliverer = CallbackDeliverer()
    assert deliverer.deliver("ext_1", receiver.url, "secret-token", {"status": "SUCCEEDED", "x": 1}) is True
    assert len(receiver.deliveries) == 1
    delivery = receiver.deliveries[0]
    assert delivery["path"] == "/callback"
    assert delivery["token"] == "secret-token"
    assert delivery["body"] == {"externalTaskId": "ext_1", "payload": {"status": "SUCCEEDED", "x": 1}}


def test_failed_callback_is_retried_with_backoff(callback_receiver) -> None:
    receiver = callback_receiver(fail_first=3)
    clock = FakeClock()
    deliverer = CallbackDeliverer(sleep=clock.sleep, now=lambda: clock.now)
    assert deliverer.deliver("ext_2", receiver.url, "t", {"status": "FAILED", "code": "X", "message": "m"}) is True
    assert len(receiver.deliveries) == 4
    assert clock.sleeps == [1.0, 2.0, 4.0]


def test_callback_gives_up_after_the_retry_budget(callback_receiver) -> None:
    receiver = callback_receiver(fail_first=1_000)
    clock = FakeClock()
    deliverer = CallbackDeliverer(retry_budget_s=10.0, sleep=clock.sleep, now=lambda: clock.now)
    assert deliverer.deliver("ext_3", receiver.url, "t", {"status": "SUCCEEDED"}) is False
    # 1 + 2 + 4 = 7 s of waiting; an 8 s wait would exceed the 10 s budget.
    assert clock.sleeps == [1.0, 2.0, 4.0]
    assert len(receiver.deliveries) == 4


def test_definite_rejection_is_not_retried(callback_receiver) -> None:
    receiver = callback_receiver(fail_first=5, fail_status=401)
    clock = FakeClock()
    deliverer = CallbackDeliverer(sleep=clock.sleep, now=lambda: clock.now)
    assert deliverer.deliver("ext_4", receiver.url, "t", {"status": "SUCCEEDED"}) is False
    assert len(receiver.deliveries) == 1
    assert clock.sleeps == []


def test_unreachable_receiver_is_retried_until_the_budget(callback_receiver) -> None:
    clock = FakeClock()
    deliverer = CallbackDeliverer(retry_budget_s=3.0, sleep=clock.sleep, now=lambda: clock.now)
    assert deliverer.deliver("ext_5", "http://127.0.0.1:9/closed", "t", {"status": "SUCCEEDED"}) is False
    assert clock.sleeps == [1.0, 2.0]


def test_callback_target_drops_query_and_userinfo() -> None:
    assert callback_target("http://user:pw@host:8080/cb/x?token=abc#f") == "http://host/cb/x"


def test_task_state_is_unchanged_when_the_callback_fails(make_service, callback_receiver) -> None:
    receiver = callback_receiver(fail_first=1_000)
    clock = FakeClock()
    finished = threading.Event()

    class Observed(CallbackDeliverer):
        def deliver(self, *args, **kwargs):
            try:
                return super().deliver(*args, **kwargs)
            finally:
                finished.set()

    service = make_service(callbacks=Observed(retry_budget_s=5.0, sleep=clock.sleep, now=lambda: clock.now))
    view = service.run("ext_cb_fail", "CHECK_CLIP", {"clipSha256": "f" * 64}, callback_url=receiver.url)
    assert view["status"] == "FAILED"
    assert finished.wait(30), "delivery did not finish"
    assert len(receiver.deliveries) >= 2
    payload = receiver.deliveries[0]["body"]["payload"]
    assert payload == {"status": "FAILED", "error": {"code": "CLIP_NOT_FOUND", "message": view["error"]["message"]}}
    assert service.client.get("/v1/tasks/ext_cb_fail").json() == view
    assert service.services.tasks.get("ext_cb_fail").attempts == 1


def test_successful_task_delivers_result_with_token(make_service, callback_receiver) -> None:
    receiver = callback_receiver()
    service = make_service()
    source = service.upload()
    view = service.run("ext_cb_ok", "TRANSCRIBE", {"sourceId": source["sourceId"]}, callback_url=receiver.url)
    assert view["status"] == "SUCCEEDED"
    receiver.wait_for(1)
    delivery = receiver.deliveries[0]
    assert delivery["token"] == "test-token-ext_cb_ok"
    assert delivery["body"]["externalTaskId"] == "ext_cb_ok"
    payload = delivery["body"]["payload"]
    assert set(payload) == {"status", "result"}
    assert payload["status"] == "SUCCEEDED"
    assert payload["result"] == view["result"]
    assert CALLBACK_TOKEN_HEADER == "X-Emberling-Callback-Token"
