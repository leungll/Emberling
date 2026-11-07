"""The single worker thread.

Tasks run one at a time in submission order. Each result is persisted before its
callback is sent, so a crash between the two leaves a finished task the caller can
poll. Callback tokens live in a dictionary on this object and nowhere else.
"""

from __future__ import annotations

import logging
import queue
import threading
from typing import Callable

from .callback import CallbackDeliverer
from .tasks import STATUS_FAILED, STATUS_QUEUED, STATUS_SUCCEEDED, Task, TaskFailure, TaskStore
from .transcribe import Transcriber

INTERNAL_ERROR = "INTERNAL_ERROR"
_QUEUE_LIMIT = 10_000

log = logging.getLogger(__name__)


class Worker:
    def __init__(
        self,
        store: TaskStore,
        executors: dict[str, Callable[[dict], dict]],
        callbacks: CallbackDeliverer,
        transcriber: Transcriber,
    ) -> None:
        self._store = store
        self._executors = executors
        self._callbacks = callbacks
        self._transcriber = transcriber
        self._queue: queue.Queue[str | None] = queue.Queue(maxsize=_QUEUE_LIMIT)
        self._tokens: dict[str, str] = {}
        self._tokens_lock = threading.Lock()
        self._thread = threading.Thread(target=self._run, name="media-worker", daemon=True)
        self._stopping = threading.Event()

    def start(self, recovered: list[Task]) -> None:
        for task in recovered:
            if task.status == STATUS_QUEUED:
                self._queue.put(task.taskId)
        self._thread.start()

    def enqueue(self, task_id: str, callback_token: str) -> None:
        with self._tokens_lock:
            self._tokens[task_id] = callback_token
        self._queue.put(task_id)

    def stop(self, join_timeout_s: float = 30.0) -> None:
        self._stopping.set()
        self._queue.put(None)
        self._thread.join(join_timeout_s)
        self._callbacks.stop()

    def _run(self) -> None:
        try:
            self._transcriber.load()
        except Exception:
            # Transcription tasks will fail with a definite code; other tasks still run.
            log.exception("whisper model %s failed to load", self._transcriber.model_name)
        while not self._stopping.is_set():
            task_id = self._queue.get()
            if task_id is None:
                break
            try:
                self._execute(task_id)
            except Exception:
                log.exception("task %s crashed the worker loop", task_id)

    def _execute(self, task_id: str) -> None:
        task = self._store.get(task_id)
        if task is None or task.status != STATUS_QUEUED:
            return
        self._store.mark_running(task_id)
        executor = self._executors[task.type]
        try:
            result = executor(task.input)
            finished = self._store.mark_succeeded(task_id, result)
        except TaskFailure as failure:
            finished = self._store.mark_failed(task_id, failure.code, failure.message)
        except Exception as exc:
            log.exception("task %s failed unexpectedly", task_id)
            finished = self._store.mark_failed(task_id, INTERNAL_ERROR, f"{type(exc).__name__}")
        self._send_callback(finished)

    def _send_callback(self, task: Task) -> None:
        with self._tokens_lock:
            token = self._tokens.pop(task.taskId, None)
        if token is None:
            # The token was never known to this process (the task was recovered after a
            # restart); the caller learns the outcome by polling.
            return
        if task.status == STATUS_SUCCEEDED:
            payload = {"status": STATUS_SUCCEEDED, "result": task.result or {}}
        else:
            payload = {"status": STATUS_FAILED, "error": task.error or {}}
        self._callbacks.deliver_async(task.taskId, task.callbackUrl, token, payload)
