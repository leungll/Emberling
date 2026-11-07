"""Task records, persisted one JSON file per task.

A task is accepted once: a second submission with the same id returns the first. The
record survives a restart so a status query can always be answered; the callback token
is not part of it and is held in memory only.
"""

from __future__ import annotations

import json
import re
import threading
import time
from dataclasses import dataclass, field, replace
from pathlib import Path

from .storage import DataDir

TASK_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$")

TYPE_TRANSCRIBE = "TRANSCRIBE"
TYPE_CHECK_TIMELINE = "CHECK_TIMELINE"
TYPE_CHECK_CLIP = "CHECK_CLIP"
TYPE_RENDER = "RENDER"
TASK_TYPES = (TYPE_TRANSCRIBE, TYPE_CHECK_TIMELINE, TYPE_CHECK_CLIP, TYPE_RENDER)

STATUS_QUEUED = "QUEUED"
STATUS_RUNNING = "RUNNING"
STATUS_SUCCEEDED = "SUCCEEDED"
STATUS_FAILED = "FAILED"
TERMINAL_STATUSES = (STATUS_SUCCEEDED, STATUS_FAILED)


class TaskFailure(Exception):
    """A task reached a definite failure; code and message become its error."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code
        self.message = message


@dataclass(frozen=True)
class Task:
    taskId: str
    type: str
    input: dict
    callbackUrl: str
    status: str
    submittedAt: float
    seq: int
    result: dict | None = None
    error: dict | None = None
    attempts: int = field(default=0)

    def view(self) -> dict:
        """The response body of a status query. It never carries the callback URL."""
        data = {"taskId": self.taskId, "type": self.type, "status": self.status, "input": self.input}
        if self.result is not None:
            data["result"] = self.result
        if self.error is not None:
            data["error"] = self.error
        return data

    def to_record(self) -> dict:
        return {
            "taskId": self.taskId,
            "type": self.type,
            "input": self.input,
            "callbackUrl": self.callbackUrl,
            "status": self.status,
            "submittedAt": self.submittedAt,
            "seq": self.seq,
            "result": self.result,
            "error": self.error,
            "attempts": self.attempts,
        }

    @classmethod
    def from_record(cls, data: dict) -> "Task":
        return cls(
            taskId=data["taskId"],
            type=data["type"],
            input=data["input"],
            callbackUrl=data["callbackUrl"],
            status=data["status"],
            submittedAt=data["submittedAt"],
            seq=data["seq"],
            result=data.get("result"),
            error=data.get("error"),
            attempts=data.get("attempts", 0),
        )


class TaskStore:
    def __init__(self, data: DataDir) -> None:
        self._data = data
        self._tasks: dict[str, Task] = {}
        self._condition = threading.Condition()
        self._seq = 0

    def _path(self, task_id: str) -> Path:
        return self._data.tasks / f"{task_id}.json"

    def load_all(self) -> list[Task]:
        """Reads every record; a task found RUNNING is reset to QUEUED to run again."""
        tasks: list[Task] = []
        for path in self._data.tasks.glob("*.json"):
            task = Task.from_record(json.loads(path.read_text("utf-8")))
            if task.status == STATUS_RUNNING:
                task = replace(task, status=STATUS_QUEUED)
                self._write(task)
            tasks.append(task)
        tasks.sort(key=lambda t: (t.submittedAt, t.seq, t.taskId))
        with self._condition:
            for task in tasks:
                self._tasks[task.taskId] = task
                self._seq = max(self._seq, task.seq)
        return tasks

    def submit(self, task_id: str, task_type: str, task_input: dict, callback_url: str) -> tuple[Task, bool]:
        """Returns the task and whether this call created it."""
        with self._condition:
            existing = self._tasks.get(task_id)
            if existing is not None:
                return existing, False
            self._seq += 1
            task = Task(
                taskId=task_id,
                type=task_type,
                input=task_input,
                callbackUrl=callback_url,
                status=STATUS_QUEUED,
                submittedAt=time.time(),
                seq=self._seq,
            )
            self._write(task)
            self._tasks[task_id] = task
            return task, True

    def get(self, task_id: str) -> Task | None:
        with self._condition:
            return self._tasks.get(task_id)

    def mark_running(self, task_id: str) -> Task:
        return self._update(task_id, lambda t: replace(t, status=STATUS_RUNNING, attempts=t.attempts + 1))

    def mark_succeeded(self, task_id: str, result: dict) -> Task:
        return self._update(task_id, lambda t: replace(t, status=STATUS_SUCCEEDED, result=result, error=None))

    def mark_failed(self, task_id: str, code: str, message: str) -> Task:
        return self._update(
            task_id,
            lambda t: replace(t, status=STATUS_FAILED, error={"code": code, "message": message}, result=None),
        )

    def wait_terminal(self, task_id: str, timeout_s: float) -> Task | None:
        """Blocks until the task is SUCCEEDED or FAILED, or the timeout passes."""
        deadline = time.monotonic() + timeout_s
        with self._condition:
            while True:
                task = self._tasks.get(task_id)
                if task is not None and task.status in TERMINAL_STATUSES:
                    return task
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return task
                self._condition.wait(remaining)

    def _update(self, task_id: str, change) -> Task:
        with self._condition:
            task = change(self._tasks[task_id])
            self._write(task)
            self._tasks[task_id] = task
            self._condition.notify_all()
            return task

    def _write(self, task: Task) -> None:
        self._data.write_text(self._path(task.taskId), json.dumps(task.to_record(), sort_keys=True))
