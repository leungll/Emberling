"""Shared fixtures: a real ffmpeg, the tiny.en model loaded once, and a service per test."""

from __future__ import annotations

import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app.callback import CallbackDeliverer
from app.config import Settings
from app.ffmpeg import FFmpeg
from app.main import create_app
from app.transcribe import Transcriber

FIXTURES = Path(__file__).parent / "fixtures"
SPEECH_MP4 = FIXTURES / "speech.mp4"
TEST_MODEL = "tiny.en"
TASK_TIMEOUT_S = 300.0


@pytest.fixture(scope="session")
def ffmpeg() -> FFmpeg:
    tool = FFmpeg(os.environ.get("MEDIA_FFMPEG_BIN", "ffmpeg"), os.environ.get("MEDIA_FFPROBE_BIN", "ffprobe"))
    problems = tool.readiness_problems()
    if problems:
        pytest.fail("ffmpeg is not usable for the tests: " + "; ".join(problems))
    return tool


@pytest.fixture(scope="session")
def transcriber(ffmpeg: FFmpeg) -> Transcriber:
    model = Transcriber(TEST_MODEL, ffmpeg)
    model.load()
    return model


class ServiceUnderTest:
    def __init__(self, client: TestClient, settings: Settings) -> None:
        self.client = client
        self.settings = settings
        self.services = client.app.state.services
        self._stopped = False

    def stop(self) -> None:
        """Shuts the service down as a process exit would; safe to call once."""
        if not self._stopped:
            self._stopped = True
            self.client.__exit__(None, None, None)

    def upload(self, path: Path = SPEECH_MP4, crop_center_x: float | None = None) -> dict:
        data = {} if crop_center_x is None else {"cropCenterX": str(crop_center_x)}
        with open(path, "rb") as handle:
            response = self.client.post(
                "/v1/sources", files={"file": (path.name, handle, "video/mp4")}, data=data
            )
        assert response.status_code == 200, response.text
        return response.json()

    def submit(self, task_id: str, task_type: str, task_input: dict, callback_url: str = "http://127.0.0.1:9/cb"):
        return self.client.post(
            "/v1/tasks",
            json={
                "taskId": task_id,
                "type": task_type,
                "input": task_input,
                "callbackUrl": callback_url,
                "callbackToken": "test-token-" + task_id,
            },
        )

    def run(self, task_id: str, task_type: str, task_input: dict, callback_url: str = "http://127.0.0.1:9/cb") -> dict:
        """Submits and waits for the terminal state; returns the status view."""
        response = self.submit(task_id, task_type, task_input, callback_url)
        assert response.status_code == 202, response.text
        return self.wait(task_id)

    def wait(self, task_id: str) -> dict:
        task = self.services.tasks.wait_terminal(task_id, TASK_TIMEOUT_S)
        assert task is not None and task.status in ("SUCCEEDED", "FAILED"), f"task {task_id} did not finish"
        response = self.client.get(f"/v1/tasks/{task_id}")
        assert response.status_code == 200
        return response.json()


@pytest.fixture
def make_service(tmp_path: Path, ffmpeg: FFmpeg, transcriber: Transcriber):
    """Starts a service on a fresh data directory; may be called again for a restart."""
    opened: list[ServiceUnderTest] = []

    def start(data_dir: Path | None = None, callbacks: CallbackDeliverer | None = None) -> ServiceUnderTest:
        settings = Settings(
            data_dir=data_dir or tmp_path / "data",
            whisper_model=TEST_MODEL,
            ffmpeg_bin=ffmpeg.ffmpeg_bin,
            ffprobe_bin=ffmpeg.ffprobe_bin,
            port=9201,
            public_url="http://media.test",
        )
        app = create_app(settings, transcriber=transcriber, callbacks=callbacks)
        client = TestClient(app)
        client.__enter__()
        started = ServiceUnderTest(client, settings)
        opened.append(started)
        return started

    yield start
    for started in reversed(opened):
        started.stop()


@pytest.fixture
def service(make_service) -> ServiceUnderTest:
    return make_service()


class CallbackReceiver:
    """A local HTTP receiver that records deliveries and can fail the first N of them."""

    def __init__(self, fail_first: int = 0, fail_status: int = 500) -> None:
        self.deliveries: list[dict] = []
        self.fail_first = fail_first
        self.fail_status = fail_status
        self._lock = threading.Lock()
        self._delivered = threading.Condition()
        receiver = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802 - http.server naming
                length = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(length) or b"{}")
                with receiver._lock:
                    attempt = len(receiver.deliveries) + 1
                    receiver.deliveries.append(
                        {"path": self.path, "token": self.headers.get("X-Emberling-Callback-Token"), "body": body}
                    )
                    status = receiver.fail_status if attempt <= receiver.fail_first else 200
                self.send_response(status)
                self.send_header("Content-Length", "0")
                self.end_headers()
                with receiver._delivered:
                    receiver._delivered.notify_all()

            def log_message(self, *args) -> None:
                return

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    @property
    def url(self) -> str:
        host, port = self._server.server_address[:2]
        return f"http://{host}:{port}/callback"

    def wait_for(self, count: int, timeout_s: float = 30.0) -> None:
        with self._delivered:
            self._delivered.wait_for(lambda: len(self.deliveries) >= count, timeout=timeout_s)

    def close(self) -> None:
        self._server.shutdown()
        self._server.server_close()


@pytest.fixture
def callback_receiver():
    receivers: list[CallbackReceiver] = []

    def start(fail_first: int = 0, fail_status: int = 500) -> CallbackReceiver:
        receiver = CallbackReceiver(fail_first, fail_status)
        receivers.append(receiver)
        return receiver

    yield start
    for receiver in receivers:
        receiver.close()
