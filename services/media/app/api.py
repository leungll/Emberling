"""HTTP routes. Handlers parse input, call the stores and worker, and map results."""

from __future__ import annotations

from typing import Literal
from urllib.parse import urlsplit

from fastapi import APIRouter, File, Form, HTTPException, UploadFile
from fastapi.responses import FileResponse, JSONResponse
from pydantic import BaseModel, ConfigDict, Field, field_validator

from .ffmpeg import FFmpeg
from .render import CLIP_MEDIA_TYPE, Renderer
from .sources import SourceRejected, SourceStore
from .storage import is_sha256_hex
from .tasks import TASK_ID_PATTERN, TaskStore
from .transcribe import Transcriber
from .worker import Worker

_MAX_CALLBACK_URL = 2048
_MAX_TOKEN = 4096


class TaskRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    taskId: str = Field(pattern=TASK_ID_PATTERN.pattern)
    type: Literal["TRANSCRIBE", "CHECK_TIMELINE", "CHECK_CLIP", "RENDER"]
    input: dict
    callbackUrl: str = Field(min_length=1, max_length=_MAX_CALLBACK_URL)
    callbackToken: str = Field(min_length=1, max_length=_MAX_TOKEN)

    @field_validator("callbackUrl")
    @classmethod
    def _absolute_http(cls, value: str) -> str:
        parts = urlsplit(value)
        if parts.scheme not in ("http", "https") or not parts.netloc:
            raise ValueError("callbackUrl must be an absolute http or https URL")
        return value


class Services:
    """Everything the routes need, built once by the application factory."""

    def __init__(
        self,
        ffmpeg: FFmpeg,
        transcriber: Transcriber,
        sources: SourceStore,
        tasks: TaskStore,
        worker: Worker,
        renderer: Renderer,
    ) -> None:
        self.ffmpeg = ffmpeg
        self.transcriber = transcriber
        self.sources = sources
        self.tasks = tasks
        self.worker = worker
        self.renderer = renderer


def build_router(services: Services) -> APIRouter:
    router = APIRouter()

    @router.get("/healthz")
    def healthz() -> JSONResponse:
        problems = services.ffmpeg.readiness_problems()
        whisper = services.transcriber.state
        if whisper != "ready":
            problems.append(f"whisper model {services.transcriber.model_name}: {whisper}")
        body = {"status": "ok" if not problems else "unavailable", "problems": problems, "whisper": whisper}
        return JSONResponse(body, status_code=200 if not problems else 503)

    @router.post("/v1/sources")
    def create_source(
        file: UploadFile = File(...),
        cropCenterX: float | None = Form(default=None),
    ) -> JSONResponse:
        if cropCenterX is not None and not 0.0 <= cropCenterX <= 1.0:
            raise HTTPException(400, "cropCenterX must be between 0 and 1")
        try:
            record = services.sources.save_upload(file.file, file.content_type, cropCenterX)
        except SourceRejected as exc:
            raise HTTPException(400, str(exc)) from exc
        return JSONResponse(record.to_dict(), status_code=200)

    @router.post("/v1/tasks")
    def create_task(request: TaskRequest) -> JSONResponse:
        task, created = services.tasks.submit(request.taskId, request.type, request.input, request.callbackUrl)
        if not created:
            if task.type != request.type or task.input != request.input:
                raise HTTPException(409, f"task {request.taskId} already exists with a different type or input")
            return JSONResponse(task.view(), status_code=200)
        services.worker.enqueue(task.taskId, request.callbackToken)
        return JSONResponse(task.view(), status_code=202)

    @router.get("/v1/tasks/{task_id}")
    def get_task(task_id: str) -> JSONResponse:
        task = services.tasks.get(task_id)
        if task is None:
            raise HTTPException(404, "unknown taskId")
        return JSONResponse(task.view())

    @router.get("/v1/clips/{sha256}")
    def get_clip(sha256: str) -> FileResponse:
        if not is_sha256_hex(sha256):
            raise HTTPException(404, "unknown clip")
        path = services.renderer.clip_path(sha256)
        if not path.exists():
            raise HTTPException(404, "unknown clip")
        return FileResponse(path, media_type=CLIP_MEDIA_TYPE, filename=f"{sha256}.mp4")

    return router
