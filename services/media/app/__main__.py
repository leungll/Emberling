"""Runs the service with uvicorn: `python -m app`."""

import logging

import uvicorn

from .config import Settings
from .main import create_app

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
# HTTP client libraries log every request line, callback URLs included; the service's own
# callback log lines name only the host and path.
for noisy in ("httpx", "httpcore", "huggingface_hub"):
    logging.getLogger(noisy).setLevel(logging.WARNING)
settings = Settings.from_env()
uvicorn.run(create_app(settings), host="0.0.0.0", port=settings.port, log_level="info")
