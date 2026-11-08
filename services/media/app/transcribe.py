"""Transcription with faster-whisper and sentence splitting.

The model yields words with timestamps. Words are grouped into sentences at terminal
punctuation or at a pause longer than a fixed threshold, and the sentences are numbered
S1, S2, ... in order. The material text an Agent reads has one line per sentence; the
word timestamps stay in the transcript record for the tools that cut and subtitle.
"""

from __future__ import annotations

import threading
from dataclasses import asdict, dataclass, field
from pathlib import Path

from .ffmpeg import FFmpeg

LANGUAGE = "en"
SENTENCE_PAUSE_MS = 800
_TERMINAL_PUNCTUATION = (".", "?", "!")
_BEAM_SIZE = 5


@dataclass(frozen=True)
class Word:
    text: str
    startMs: int
    endMs: int


@dataclass(frozen=True)
class Sentence:
    id: str
    startMs: int
    endMs: int
    text: str
    words: tuple[Word, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class Transcript:
    model: str
    sourceSha256: str
    language: str
    durationMs: int
    sentences: tuple[Sentence, ...]

    @property
    def material(self) -> str:
        return material_text(self.sentences)

    def to_dict(self) -> dict:
        return {
            "model": self.model,
            "sourceSha256": self.sourceSha256,
            "language": self.language,
            "durationMs": self.durationMs,
            "sentences": [asdict(s) for s in self.sentences],
        }

    @classmethod
    def from_dict(cls, data: dict) -> "Transcript":
        sentences = tuple(
            Sentence(
                id=s["id"],
                startMs=s["startMs"],
                endMs=s["endMs"],
                text=s["text"],
                words=tuple(Word(**w) for w in s["words"]),
            )
            for s in data["sentences"]
        )
        return cls(
            model=data["model"],
            sourceSha256=data["sourceSha256"],
            language=data["language"],
            durationMs=data["durationMs"],
            sentences=sentences,
        )


def material_text(sentences: tuple[Sentence, ...] | list[Sentence]) -> str:
    """One line per sentence: id, start and end in seconds, text; tab separated."""
    return "".join(
        f"{s.id}\t{s.startMs / 1000:.3f}\t{s.endMs / 1000:.3f}\t{s.text}\n" for s in sentences
    )


def split_sentences(words: list[Word], pause_ms: int = SENTENCE_PAUSE_MS) -> list[Sentence]:
    sentences: list[Sentence] = []
    current: list[Word] = []

    def flush() -> None:
        if not current:
            return
        sentences.append(
            Sentence(
                id=f"S{len(sentences) + 1}",
                startMs=current[0].startMs,
                endMs=current[-1].endMs,
                text=" ".join(w.text for w in current),
                words=tuple(current),
            )
        )
        current.clear()

    for index, word in enumerate(words):
        current.append(word)
        ends_sentence = word.text.endswith(_TERMINAL_PUNCTUATION)
        next_word = words[index + 1] if index + 1 < len(words) else None
        long_pause = next_word is not None and next_word.startMs - word.endMs >= pause_ms
        if ends_sentence or long_pause:
            flush()
    flush()
    return sentences


class Transcriber:
    """Owns the whisper model. Loading is explicit so readiness can report it."""

    def __init__(self, model_name: str, ffmpeg: FFmpeg) -> None:
        self.model_name = model_name
        self._ffmpeg = ffmpeg
        self._model = None
        self._lock = threading.Lock()
        self._error: str | None = None

    @property
    def state(self) -> str:
        if self._model is not None:
            return "ready"
        if self._error is not None:
            return "failed"
        return "loading"

    def load(self) -> None:
        with self._lock:
            if self._model is not None:
                return
            try:
                from faster_whisper import WhisperModel

                self._model = WhisperModel(self.model_name, device="cpu", compute_type="int8")
                self._error = None
            except Exception as exc:  # the model name or download is the only variable here
                self._error = f"{type(exc).__name__}: {exc}"
                raise

    def transcribe(self, path: Path, source_sha256: str, duration_ms: int) -> Transcript:
        self.load()
        audio = self._ffmpeg.decode_audio(path)
        with self._lock:
            segments, info = self._model.transcribe(
                audio,
                language=LANGUAGE,
                word_timestamps=True,
                beam_size=_BEAM_SIZE,
                temperature=0.0,
            )
            words: list[Word] = []
            for segment in segments:
                for word in segment.words or ():
                    text = word.word.strip()
                    if not text:
                        continue
                    words.append(
                        Word(
                            text=text,
                            startMs=int(round(word.start * 1000)),
                            endMs=int(round(word.end * 1000)),
                        )
                    )
        return Transcript(
            model=self.model_name,
            sourceSha256=source_sha256,
            language=info.language,
            durationMs=duration_ms,
            sentences=tuple(split_sentences(words)),
        )
