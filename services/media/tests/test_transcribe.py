"""Transcription through the service, with the real tiny.en model."""

from __future__ import annotations

import hashlib
import re
from pathlib import Path

from app.transcribe import Word, split_sentences

from .conftest import SPEECH_MP4

MATERIAL_LINE = re.compile(r"^S(\d+)\t(\d+\.\d{3})\t(\d+\.\d{3})\t(.+)$")


def test_split_sentences_on_punctuation_and_pauses() -> None:
    words = [
        Word("Hello", 0, 300),
        Word("there.", 300, 600),
        Word("Long", 700, 900),
        Word("pause", 900, 1100),
        Word("next", 2000, 2200),
        Word("words", 2200, 2400),
    ]
    sentences = split_sentences(words, pause_ms=800)
    assert [s.id for s in sentences] == ["S1", "S2", "S3"]
    assert [s.text for s in sentences] == ["Hello there.", "Long pause", "next words"]
    assert (sentences[1].startMs, sentences[1].endMs) == (700, 1100)


def test_transcribe_returns_numbered_material_and_metadata(service) -> None:
    source = service.upload()
    view = service.run("ext_tr1", "TRANSCRIBE", {"sourceId": source["sourceId"]})
    assert view["status"] == "SUCCEEDED", view
    result = view["result"]
    lines = result["material"].splitlines()
    assert lines, "material is empty"
    for number, line in enumerate(lines, start=1):
        match = MATERIAL_LINE.match(line)
        assert match, line
        assert int(match.group(1)) == number
        assert float(match.group(2)) <= float(match.group(3))
    assert result["sentenceCount"] == len(lines) == result["lineCount"]
    assert result["language"] == "en"
    assert result["durationMs"] == source["durationMs"]
    assert result["sourceSha256"] == source["sha256"]
    assert result["model"] == "tiny.en"
    assert result["transcriptSha256"] == hashlib.sha256(result["material"].encode("utf-8")).hexdigest()
    assert "river" in result["material"].lower()
    # The source the transcript belongs to is addressable from the result itself.
    assert result["sourceId"] == source["sourceId"]
    assert result["url"] == source["url"] == f"http://media.test/v1/sources/{source['sourceId']}/content"
    assert result["mediaType"] == source["mediaType"]


def test_transcribe_same_source_and_model_hits_the_cache(service, monkeypatch) -> None:
    source = service.upload()
    first = service.run("ext_tr_a", "TRANSCRIBE", {"sourceId": source["sourceId"]})["result"]

    def must_not_run(*args, **kwargs):
        raise AssertionError("the model ran although the transcript was cached")

    monkeypatch.setattr(service.services.transcriber, "transcribe", must_not_run)
    second = service.run("ext_tr_b", "TRANSCRIBE", {"sourceId": source["sourceId"]})["result"]
    assert second == first


def test_transcript_of_another_source_fails_the_timeline_check(service, ffmpeg, tmp_path: Path) -> None:
    source_a = service.upload()
    other = tmp_path / "other.mp4"
    ffmpeg.run(["-y", "-v", "error", "-i", str(SPEECH_MP4), "-t", "3", "-c", "copy", str(other)])
    source_b = service.upload(other)
    assert source_a["sourceId"] != source_b["sourceId"]

    transcript = service.run("ext_tr_mis", "TRANSCRIBE", {"sourceId": source_a["sourceId"]})["result"]
    view = service.run(
        "ext_ct_mis",
        "CHECK_TIMELINE",
        {
            "sourceId": source_b["sourceId"],
            "transcriptSha256": transcript["transcriptSha256"],
            "segments": [{"from": "S1", "to": "S1"}],
        },
    )
    assert view["status"] == "FAILED"
    assert view["error"]["code"] == "TIMELINE_INPUT_INVALID"
    assert list((service.settings.data_dir / "timelines").iterdir()) == []


def test_unparsable_sentence_id_fails_the_timeline_check(service) -> None:
    source = service.upload()
    transcript = service.run("ext_tr_bad", "TRANSCRIBE", {"sourceId": source["sourceId"]})["result"]
    view = service.run(
        "ext_ct_bad",
        "CHECK_TIMELINE",
        {"sourceId": source["sourceId"], "transcriptSha256": transcript["transcriptSha256"], "segments": [{"from": "S1", "to": "sentence two"}]},
    )
    assert view["status"] == "FAILED"
    assert view["error"]["code"] == "TIMELINE_INPUT_INVALID"


def test_get_source_returns_the_reference(service) -> None:
    source = service.upload(crop_center_x=0.25)
    response = service.client.get(f"/v1/sources/{source['sourceId']}")
    assert response.status_code == 200
    assert response.json() == source
    assert set(source) == {"sourceId", "sha256", "durationMs", "mediaType", "cropCenterX", "url"}
    assert source["url"] == f"{service.settings.public_url}/v1/sources/{source['sourceId']}/content"
    assert service.client.get("/v1/sources/src_" + "0" * 32).status_code == 404


def test_source_content_is_served_with_its_media_type(service) -> None:
    source = service.upload()
    response = service.client.get(f"/v1/sources/{source['sourceId']}/content")
    assert response.status_code == 200
    assert response.headers["content-type"] == source["mediaType"]
    assert hashlib.sha256(response.content).hexdigest() == source["sha256"]
    assert service.client.get("/v1/sources/src_" + "0" * 32 + "/content").status_code == 404
    # The persisted record stays free of the served URL, which depends on deployment.
    records = list((service.settings.data_dir / "sources").glob("*.json"))
    assert len(records) == 1 and "url" not in records[0].read_text("utf-8")


def test_upload_same_content_returns_same_reference(service) -> None:
    first = service.upload()
    second = service.upload(crop_center_x=0.3)
    assert second["sourceId"] == first["sourceId"]
    assert second["sha256"] == first["sha256"]
    assert second["cropCenterX"] == 0.3
    assert len(list((service.settings.data_dir / "sources").glob("*.json"))) == 1
