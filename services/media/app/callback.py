"""Callback delivery with retry.

A finished task is reported with one POST to its callback URL, carrying the one-time
token in a header. Delivery happens after the task record is persisted, so the record
never depends on the callback succeeding. A failed delivery is retried with exponential
backoff until the retry budget is spent; the task's state is never changed by delivery,
and a lost callback is recovered by the caller polling the status route. The token is
kept in memory only and never logged.
"""

from __future__ import annotations

import logging
import threading
import time
from typing import Callable
from urllib.parse import urlsplit, urlunsplit

import httpx

CALLBACK_TOKEN_HEADER = "X-Emberling-Callback-Token"
DEFAULT_RETRY_BUDGET_S = 600.0
_INITIAL_DELAY_S = 1.0
_MAX_DELAY_S = 30.0
_REQUEST_TIMEOUT_S = 10.0
_RETRIABLE_STATUSES = {408, 425, 429}

log = logging.getLogger(__name__)


def callback_target(url: str) -> str:
    """The URL without userinfo, query or fragment, for log lines."""
    parts = urlsplit(url)
    return urlunsplit((parts.scheme, parts.hostname or "", parts.path, "", ""))


class CallbackDeliverer:
    def __init__(
        self,
        retry_budget_s: float = DEFAULT_RETRY_BUDGET_S,
        sleep: Callable[[float], None] | None = None,
        now: Callable[[], float] = time.monotonic,
        client_factory: Callable[[], httpx.Client] | None = None,
    ) -> None:
        self._budget = retry_budget_s
        self._stop = threading.Event()
        self._sleep = sleep or (lambda seconds: self._stop.wait(seconds))
        self._now = now
        self._client_factory = client_factory or (lambda: httpx.Client(timeout=_REQUEST_TIMEOUT_S))
        self._threads: list[threading.Thread] = []
        self._lock = threading.Lock()

    def deliver_async(self, task_id: str, url: str, token: str, payload: dict) -> None:
        thread = threading.Thread(
            target=self.deliver, args=(task_id, url, token, payload), name=f"callback-{task_id}", daemon=True
        )
        with self._lock:
            self._threads = [t for t in self._threads if t.is_alive()]
            self._threads.append(thread)
        thread.start()

    def deliver(self, task_id: str, url: str, token: str, payload: dict) -> bool:
        """Delivers until accepted, definitively rejected, or the budget is spent."""
        body = {"externalTaskId": task_id, "payload": payload}
        headers = {CALLBACK_TOKEN_HEADER: token}
        started = self._now()
        delay = _INITIAL_DELAY_S
        attempt = 0
        with self._client_factory() as client:
            while not self._stop.is_set():
                attempt += 1
                outcome = self._attempt(client, url, headers, body)
                if outcome == "delivered":
                    log.info("callback delivered task=%s target=%s attempt=%d", task_id, callback_target(url), attempt)
                    return True
                if outcome == "rejected":
                    log.warning("callback rejected task=%s target=%s attempt=%d", task_id, callback_target(url), attempt)
                    return False
                elapsed = self._now() - started
                if elapsed + delay > self._budget:
                    log.warning(
                        "callback abandoned task=%s target=%s attempts=%d; status stays available for polling",
                        task_id,
                        callback_target(url),
                        attempt,
                    )
                    return False
                self._sleep(delay)
                delay = min(delay * 2, _MAX_DELAY_S)
        return False

    def _attempt(self, client: httpx.Client, url: str, headers: dict, body: dict) -> str:
        try:
            response = client.post(url, json=body, headers=headers)
        except httpx.HTTPError as exc:
            log.info("callback transport error target=%s error=%s", callback_target(url), type(exc).__name__)
            return "retry"
        if 200 <= response.status_code < 300:
            return "delivered"
        if response.status_code >= 500 or response.status_code in _RETRIABLE_STATUSES:
            return "retry"
        # Any other 4xx is the receiver's definite answer; retrying cannot change it.
        return "rejected"

    def stop(self, join_timeout_s: float = 5.0) -> None:
        self._stop.set()
        with self._lock:
            threads = list(self._threads)
        for thread in threads:
            thread.join(join_timeout_s)
