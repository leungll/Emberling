"""Thin client for the Emberling REST API used by the evaluation scripts.

Only public routes are used, read-only apart from Run creation, and nothing here
interprets Runtime state: the Trace summary module turns the raw payloads into evaluation
facts. Every response is returned as parsed JSON.
"""

from __future__ import annotations

import time
from typing import Any

import httpx

TERMINAL_RUN_STATUSES = ("COMPLETED", "FAILED")


class EmberlingError(RuntimeError):
    """The API answered with an error or an unexpected shape."""


class EmberlingClient:
    def __init__(self, base_url: str, *, timeout: float = 30.0, client: httpx.Client | None = None) -> None:
        self.base_url = base_url.rstrip("/")
        self._client = client or httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._client.close()

    def _request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        response = self._client.request(method, self.base_url + path, **kwargs)
        if response.status_code >= 400:
            raise EmberlingError(f"{method} {path} -> {response.status_code}: {response.text[:500]}")
        return response

    def _json(self, method: str, path: str, **kwargs: Any) -> Any:
        return self._request(method, path, **kwargs).json()

    def create_run(self, workflow_id: str, definition_version: int, run_input: dict[str, Any],
                   idempotency_key: str | None = None) -> dict[str, Any]:
        """POST /api/runs. The Idempotency-Key makes a retried submission return the same Run."""
        headers = {"Idempotency-Key": idempotency_key} if idempotency_key else {}
        body = {"workflowId": workflow_id, "definitionVersion": definition_version, "input": run_input}
        return self._json("POST", "/api/runs", json=body, headers=headers)

    def get_run(self, run_id: str) -> dict[str, Any]:
        """GET /api/runs/{runId}: {"run": {...}, "nodeRuns": [...], "lastSeq": n}."""
        return self._json("GET", f"/api/runs/{run_id}")

    def wait_for_run(self, run_id: str, *, timeout_seconds: float, poll_seconds: float = 2.0,
                     sleep=time.sleep, clock=time.monotonic) -> dict[str, Any]:
        """Poll the Run snapshot until its status is terminal or the bound passes.

        The bound is the caller's patience, not a Runtime deadline: a Run that is still
        running when it passes is reported as such by raising, and keeps running.
        """
        deadline = clock() + timeout_seconds
        while True:
            snapshot = self.get_run(run_id)
            if snapshot["run"]["status"] in TERMINAL_RUN_STATUSES:
                return snapshot
            if clock() >= deadline:
                raise TimeoutError(f"run {run_id} still {snapshot['run']['status']} after {timeout_seconds}s")
            sleep(poll_seconds)

    def get_agent_trace(self, run_id: str, node_run_id: str) -> dict[str, Any]:
        """GET /api/runs/{runId}/nodes/{nodeRunId}/agent."""
        return self._json("GET", f"/api/runs/{run_id}/nodes/{node_run_id}/agent")

    def get_artifact_text(self, run_id: str, artifact_id: str) -> str:
        """GET /api/runs/{runId}/artifacts/{artifactId}/content as UTF-8 text."""
        response = self._request("GET", f"/api/runs/{run_id}/artifacts/{artifact_id}/content")
        return response.content.decode("utf-8")

    def get_run_cost(self, run_id: str, workflow_id: str) -> dict[str, Any]:
        """Read the Run's charge summary from the Run list item that carries it.

        The list is filtered by workflowId and paged by cursor until the Run is found;
        the cost is the API's own per-currency sum, never recomputed here.
        """
        cursor: str | None = None
        for _ in range(100):
            params: dict[str, Any] = {"workflowId": workflow_id, "limit": 100}
            if cursor:
                params["cursor"] = cursor
            page = self._json("GET", "/api/runs", params=params)
            for item in page.get("items", []):
                if item.get("id") == run_id:
                    cost = item.get("cost")
                    if not isinstance(cost, dict):
                        raise EmberlingError(f"run list item {run_id} has no cost object")
                    return cost
            cursor = page.get("nextCursor")
            if not cursor:
                break
        raise EmberlingError(f"run {run_id} not found in the Run list of workflow {workflow_id}")
