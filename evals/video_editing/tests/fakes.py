"""In-process fake HTTP services for the self-tests: Emberling, the media service and OpenRouter.

They answer the exact routes and shapes the scripts rely on and record every request, so
a test can assert what was sent without any real service running.
"""

from __future__ import annotations

import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable
from urllib.parse import parse_qs, urlparse


class FakeServer:
    """A threaded HTTP server dispatching to a handler(method, path, query, body, headers) -> (status, body, content_type)."""

    def __init__(self, handler: Callable[..., tuple[int, Any, str]]) -> None:
        self.requests: list[dict[str, Any]] = []
        self._handler = handler
        server = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args: Any) -> None:  # keep test output quiet
                pass

            def _serve(self) -> None:
                parsed = urlparse(self.path)
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                server.requests.append({"method": self.command, "path": parsed.path,
                                        "query": parse_qs(parsed.query), "body": raw,
                                        "headers": {k.lower(): v for k, v in self.headers.items()}})
                status, body, content_type = server._handler(
                    self.command, parsed.path, parse_qs(parsed.query), raw, self.headers)
                payload = body if isinstance(body, bytes) else json.dumps(body).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            do_GET = do_POST = _serve

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)

    def __enter__(self) -> "FakeServer":
        self._thread.start()
        return self

    def __exit__(self, *_exc: Any) -> None:
        self._server.shutdown()
        self._server.server_close()

    @property
    def url(self) -> str:
        host, port = self._server.server_address[:2]
        return f"http://{host}:{port}"


class FakeEmberling(FakeServer):
    """Emberling API fake: Runs are created, end at once with a scripted result per sample."""

    def __init__(self, scripts: dict[str, dict[str, Any]], artifacts: dict[str, str] | None = None) -> None:
        self.scripts = scripts
        self.artifacts = artifacts or {}
        self.runs: dict[str, dict[str, Any]] = {}
        self.fail_create = False
        super().__init__(self._handle)

    def _handle(self, method: str, path: str, query: dict[str, list[str]], raw: bytes, headers: Any):
        if method == "POST" and path == "/api/runs":
            if self.fail_create:
                return 500, {"error": {"code": "INTERNAL", "message": "injected"}}, "application/json"
            body = json.loads(raw)
            task = body["input"]["task"]
            script = self.scripts[task["requirement"]]
            run_id = f"run_{len(self.runs) + 1:03d}"
            self.runs[run_id] = {"body": body, "script": script}
            return 201, {"id": run_id, "workflowId": body["workflowId"],
                         "definitionVersion": body["definitionVersion"], "status": "RUNNING", "lastSeq": 1}, "application/json"
        if method == "GET" and path == "/api/runs":
            items = [self._list_item(run_id) for run_id in self.runs
                     if self.runs[run_id]["body"]["workflowId"] == query.get("workflowId", [None])[0]]
            return 200, {"items": items, "nextCursor": None, "prevCursor": None,
                         "counts": {"all": len(items), "running": 0, "paused": 0, "completed": 0, "failed": 0}}, "application/json"
        match = re.fullmatch(r"/api/runs/(run_\d+)", path)
        if method == "GET" and match:
            return 200, self._snapshot(match.group(1)), "application/json"
        match = re.fullmatch(r"/api/runs/(run_\d+)/nodes/(nr_agent_\d+)/agent", path)
        if method == "GET" and match:
            return 200, self._trace(match.group(1)), "application/json"
        match = re.fullmatch(r"/api/runs/(run_\d+)/artifacts/([^/]+)/content", path)
        if method == "GET" and match:
            text = self.artifacts.get(match.group(2))
            if text is None:
                return 404, {"error": {"code": "ARTIFACT_NOT_FOUND", "message": "no"}}, "application/json"
            return 200, text.encode("utf-8"), "text/plain; charset=utf-8"
        return 404, {"error": {"code": "NOT_FOUND", "message": path}}, "application/json"

    def _snapshot(self, run_id: str) -> dict[str, Any]:
        script = self.runs[run_id]["script"]
        body = self.runs[run_id]["body"]
        outcome = script["outcome"]
        run_status = "FAILED" if outcome in ("INCOMPLETE", "FAILED") else "COMPLETED"
        number = run_id.split("_")[1]
        agent_output = None
        agent_error = None
        if outcome in ("CLIP_DELIVERED", "CANNOT_SATISFY"):
            report = {"outcome": "DELIVERED" if outcome == "CLIP_DELIVERED" else "CANNOT_SATISFY",
                      "clipSha256": script.get("clipSha256"), "selectionReason": "scripted",
                      "cannotSatisfyReason": script.get("cannotSatisfyReason")}
            agent_output = {"text": json.dumps({"outcome": outcome, "checks": [], "missing": [], "evidence": [],
                                                "agentReport": report}, sort_keys=True)}
        elif outcome == "INCOMPLETE":
            agent_error = {"code": "TASK_INCOMPLETE", "message": "the task is not complete"}
        else:
            agent_error = {"code": script.get("errorCode", "MODEL_ERROR"), "message": "injected failure"}
        node_runs = [
            {"id": f"nr_transcript_{number}", "runId": run_id, "nodeId": "node_transcript", "nodeType": "media_transcript",
             "status": "SUCCEEDED", "input": None,
             "output": {"transcript": {"artifactId": script.get("transcriptArtifactId", "artifact_transcript"),
                                       "sha256": "a" * 64, "lineCount": 3, "sentenceCount": 3, "language": "en",
                                       "durationMs": body["input"]["task"]["durationMs"]}},
             "error": None, "attemptCount": 1},
            {"id": f"nr_agent_{number}", "runId": run_id, "nodeId": "node_agent", "nodeType": "agent",
             "status": "SUCCEEDED" if agent_error is None else "FAILED", "input": None, "output": agent_output,
             "error": agent_error, "attemptCount": 1},
        ]
        return {"run": {"id": run_id, "workflowId": body["workflowId"], "definitionVersion": body["definitionVersion"],
                        "status": run_status, "input": body["input"], "output": None,
                        "error": None if run_status == "COMPLETED" else {"code": "NODE_FAILED", "message": "node_agent failed"},
                        "lastSeq": 9, "startedAt": "2026-11-07T10:00:00Z", "completedAt": "2026-11-07T10:03:30Z",
                        "tokenUsage": None},
                "nodeRuns": node_runs, "lastSeq": 9}

    def _trace(self, run_id: str) -> dict[str, Any]:
        script = self.runs[run_id]["script"]
        number = run_id.split("_")[1]
        facts = []
        for i, verdict in enumerate(script.get("mediaChecked", [])):
            facts.append({"id": f"fact_mc_{i}", "factType": "media_checked", "subject": "t" * 64,
                          "bindings": {"kind": "TIMELINE", "ruleVersion": "v1"}, "verdict": verdict,
                          "basisFactId": None, "toolAttemptId": "ta_1", "createdAt": "2026-11-07T10:01:00Z"})
        for i, verdict in enumerate(script.get("contentReviewed", [])):
            facts.append({"id": f"fact_cr_{i}", "factType": "content_reviewed", "subject": "t" * 64,
                          "bindings": {"policyVersion": "v1"}, "verdict": verdict,
                          "basisFactId": None, "toolAttemptId": "ta_2", "createdAt": "2026-11-07T10:01:30Z"})
        if script.get("clipSha256"):
            facts.append({"id": "fact_clip", "factType": "clip_rendered", "subject": script["clipSha256"],
                          "bindings": {"timelineSha256": script.get("timelineSha256", "t" * 64)}, "verdict": None,
                          "basisFactId": None, "toolAttemptId": "ta_3", "createdAt": "2026-11-07T10:02:00Z"})
        outcome = script["outcome"]
        completion = None
        if outcome in ("CLIP_DELIVERED", "CANNOT_SATISFY", "INCOMPLETE"):
            completion = {"outcome": outcome, "checks": [], "evaluatedAt": "2026-11-07T10:03:00Z",
                          "missing": [{"code": "CLIP_NOT_CHECKED", "description": "no passing clip check"}]
                          if outcome == "INCOMPLETE" else []}
        turns = [{"id": f"turn_{i}", "turnNo": i, "status": "COMPLETED", "cost": {"amount": "0.10", "currency": "USD", "source": "m"}}
                 for i in range(1, script.get("turns", 3) + 1)]
        return {"agentRun": {"id": f"ar_{number}", "nodeRunId": f"nr_agent_{number}",
                             "termination": "FINAL_RESPONSE" if completion else "MODEL_ERROR",
                             "currentTurnNo": len(turns), "completion": completion, "goal": "DELIVER_CLIP",
                             "error": None},
                "turns": turns, "facts": {"items": facts, "truncated": False},
                "generationBudget": {"maxGenerationCalls": None, "generationCallsUsed": 0},
                "workspace": None, "changeRequests": {"items": [], "truncated": False}}

    def _list_item(self, run_id: str) -> dict[str, Any]:
        script = self.runs[run_id]["script"]
        body = self.runs[run_id]["body"]
        return {"id": run_id, "workflowId": body["workflowId"], "workflowName": "demo",
                "definitionVersion": body["definitionVersion"], "latestVersion": body["definitionVersion"],
                "status": "COMPLETED", "createdAt": "2026-11-07T10:00:00Z", "startedAt": "2026-11-07T10:00:00Z",
                "completedAt": "2026-11-07T10:03:30Z", "error": None, "failedStep": None, "waitingSteps": [],
                "cost": script.get("cost", {"totals": [{"amount": "0.30", "currency": "USD"}], "unknownCalls": 0})}


class FakeMedia(FakeServer):
    """Media service fake: uploads are hashed in memory; clips are served by digest."""

    def __init__(self, clips: dict[str, bytes] | None = None) -> None:
        import hashlib

        self.uploads: list[dict[str, Any]] = []
        self.clips = clips or {}
        self._hashlib = hashlib
        super().__init__(self._handle)

    def _handle(self, method: str, path: str, query: dict[str, list[str]], raw: bytes, headers: Any):
        if method == "POST" and path == "/v1/sources":
            content_type = headers.get("Content-Type", "")
            boundary = content_type.split("boundary=")[-1].encode()
            file_bytes = b""
            crop = None
            for part in raw.split(b"--" + boundary):
                if b'name="file"' in part:
                    file_bytes = part.split(b"\r\n\r\n", 1)[1].rstrip(b"\r\n")
                elif b'name="cropCenterX"' in part:
                    crop = part.split(b"\r\n\r\n", 1)[1].strip().decode()
            digest = self._hashlib.sha256(file_bytes).hexdigest()
            self.uploads.append({"sha256": digest, "cropCenterX": crop, "size": len(file_bytes)})
            return 201, {"sourceId": "src_" + digest[:8], "sha256": digest, "durationMs": 480000,
                         "mediaType": "video/webm"}, "application/json"
        match = re.fullmatch(r"/v1/clips/([0-9a-f]{64})", path)
        if method == "GET" and match and match.group(1) in self.clips:
            return 200, self.clips[match.group(1)], "video/mp4"
        return 404, {"error": "not found"}, "application/json"


class FakeOpenRouter(FakeServer):
    """OpenRouter fake answering a scripted JSON verdict and recording the authorization header."""

    def __init__(self, answer: str | Callable[[dict[str, Any]], str], *, fail: bool = False) -> None:
        self.answer = answer
        self.fail = fail
        super().__init__(self._handle)

    def _handle(self, method: str, path: str, query: dict[str, list[str]], raw: bytes, headers: Any):
        if method == "POST" and path == "/chat/completions":
            if self.fail:
                return 429, {"error": {"message": "rate limited"}}, "application/json"
            body = json.loads(raw)
            text = self.answer(body) if callable(self.answer) else self.answer
            return 200, {"id": "gen-1", "model": body["model"],
                         "choices": [{"message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                         "usage": {"prompt_tokens": 1200, "completion_tokens": 90, "total_tokens": 1290, "cost": 0.0213}}, "application/json"
        return 404, {"error": "not found"}, "application/json"
