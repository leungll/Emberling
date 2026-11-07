"""Runs the service with uvicorn: `python -m app`."""

import logging

import uvicorn

from .config import Settings
from .main import create_app

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
settings = Settings.from_env()
uvicorn.run(create_app(settings), host="0.0.0.0", port=settings.port, log_level="info")
