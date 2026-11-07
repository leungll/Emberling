"""Application factory: wires the stores, worker and routes, and owns their lifetime."""

from __future__ import annotations

import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI

from .api import Services, build_router
from .callback import CallbackDeliverer
from .clipcheck import ClipChecker
from .config import Settings
from .executors import TaskExecutors
from .ffmpeg import FFmpeg
from .render import Renderer
from .sources import SourceStore
from .storage import DataDir
from .tasks import TaskStore
from .timeline import TimelineStore
from .transcribe import Transcriber
from .transcripts import TranscriptStore
from .worker import Worker

log = logging.getLogger(__name__)


def create_app(
    settings: Settings,
    transcriber: Transcriber | None = None,
    callbacks: CallbackDeliverer | None = None,
) -> FastAPI:
    data = DataDir(settings.data_dir)
    ffmpeg = FFmpeg(settings.ffmpeg_bin, settings.ffprobe_bin)
    transcriber = transcriber or Transcriber(settings.whisper_model, ffmpeg)
    sources = SourceStore(data, ffmpeg)
    transcripts = TranscriptStore(data)
    timelines = TimelineStore(data)
    renderer = Renderer(data, ffmpeg, sources, transcripts, timelines, settings.public_url)
    clip_checker = ClipChecker(ffmpeg, data.clips, timelines, transcripts)
    executors = TaskExecutors(sources, transcriber, transcripts, timelines, renderer, clip_checker)
    tasks = TaskStore(data)
    worker = Worker(tasks, executors.by_type, callbacks or CallbackDeliverer(), transcriber)
    services = Services(ffmpeg, transcriber, sources, tasks, worker, renderer)

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        removed = data.sweep_tmp()
        if removed:
            log.info("removed %d leftover temporary entries", removed)
        recovered = tasks.load_all()
        queued = [t for t in recovered if t.status == "QUEUED"]
        if queued:
            log.info("re-queued %d unfinished task(s) found on disk", len(queued))
        worker.start(recovered)
        try:
            yield
        finally:
            worker.stop()

    app = FastAPI(title="Emberling media service", lifespan=lifespan)
    app.include_router(build_router(services))
    app.state.services = services
    return app
