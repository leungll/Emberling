import hashlib
import json
import os
import stat

import pytest

import prepare_dataset
from fakes import FakeMedia
from fixtures import dataset_rows
from records import RecordError, read_json, write_jsonl


def _fake_ffmpeg(tmp_path):
    """A stand-in ffmpeg that copies its input to its output and records the arguments."""
    script = tmp_path / "ffmpeg"
    log = tmp_path / "ffmpeg.log"
    script.write_text("#!/bin/sh\n"
                      f"printf '%s\\n' \"$*\" >> '{log}'\n"
                      "in=''; out=''\n"
                      "while [ $# -gt 0 ]; do\n"
                      "  if [ \"$1\" = -i ]; then in=$2; shift; fi\n"
                      "  out=$1; shift\n"
                      "done\n"
                      "cat \"$in\" > \"$out\"\n")
    script.chmod(script.stat().st_mode | stat.S_IEXEC)
    return script, log


def _sources(tmp_path, rows, *, corrupt=None):
    sources = tmp_path / "sources"
    sources.mkdir()
    for row in rows:
        content = ("video " + row["slug"]).encode()
        if corrupt == row["sampleId"]:
            content += b"x"
        (sources / row["localFileName"]).write_bytes(content)
        row["sourceSha256"] = hashlib.sha256(("video " + row["slug"]).encode()).hexdigest()
    return sources


def _dataset(tmp_path, rows, monkeypatch):
    here = tmp_path / "evals"
    (here / "dataset").mkdir(parents=True)
    write_jsonl(here / "dataset" / "v1.jsonl", rows)
    monkeypatch.setattr(prepare_dataset, "HERE", here)
    return here


def test_prepare_verifies_hashes_cuts_declared_ranges_and_records_references(tmp_path, monkeypatch):
    rows = dataset_rows()
    sources = _sources(tmp_path, rows)
    here = _dataset(tmp_path, rows, monkeypatch)
    ffmpeg, log = _fake_ffmpeg(tmp_path)
    with FakeMedia() as media:
        assert prepare_dataset.main(["--sources-dir", str(sources), "--data-dir", str(tmp_path / "data"),
                                    "--media-url", media.url, "--ffmpeg", str(ffmpeg)]) == 0
        assert len(media.uploads) == 6
        assert all(u["cropCenterX"] == "0.5" for u in media.uploads)
    prepared = read_json(here / "dataset" / "v1.prepared.json")
    assert prepared["schemaVersion"] == "prepared/v1"
    assert set(prepared["samples"]) == {"01", "02", "03", "04", "05", "06"}
    assert prepared["samples"]["01"]["preparedFileName"] == "01-deborah-engineer.webm"
    assert prepared["samples"]["05"]["preparedFileName"] == "05-martin-rees.cut-60s-480s.webm"
    assert prepared["samples"]["06"]["preparedFileName"] == "06-alan-guth.cut-120s-480s.webm"
    assert prepared["samples"]["05"]["sourceId"].startswith("src_")
    assert prepared["samples"]["05"]["durationMs"] == 480000
    calls = log.read_text().splitlines()
    assert len(calls) == 2
    assert "-ss 60 -i" in calls[0] and "-t 480" in calls[0] and "-c copy" in calls[0]
    assert "-ss 120 -i" in calls[1]
    # Idempotent: a second pass cuts nothing again and leaves the same references.
    with FakeMedia() as media:
        prepare_dataset.main(["--sources-dir", str(sources), "--data-dir", str(tmp_path / "data"),
                              "--media-url", media.url, "--ffmpeg", str(ffmpeg)])
    assert len(log.read_text().splitlines()) == 2
    assert read_json(here / "dataset" / "v1.prepared.json")["samples"] == prepared["samples"]


def test_prepare_refuses_a_source_whose_digest_differs(tmp_path, monkeypatch):
    rows = dataset_rows()
    sources = _sources(tmp_path, rows, corrupt="03")
    _dataset(tmp_path, rows, monkeypatch)
    ffmpeg, _ = _fake_ffmpeg(tmp_path)
    with FakeMedia() as media:
        with pytest.raises(RecordError, match="sample 03: source digest"):
            prepare_dataset.main(["--sources-dir", str(sources), "--data-dir", str(tmp_path / "data"),
                                  "--media-url", media.url, "--ffmpeg", str(ffmpeg)])
        assert len(media.uploads) == 2  # samples 01 and 02 were uploaded before the mismatch stopped the run


def test_prepare_reports_a_missing_source_file(tmp_path, monkeypatch):
    rows = dataset_rows()
    sources = _sources(tmp_path, rows)
    os.remove(sources / rows[1]["localFileName"])
    _dataset(tmp_path, rows, monkeypatch)
    ffmpeg, _ = _fake_ffmpeg(tmp_path)
    with FakeMedia() as media:
        with pytest.raises(FileNotFoundError, match="sample 02"):
            prepare_dataset.main(["--sources-dir", str(sources), "--data-dir", str(tmp_path / "data"),
                                  "--media-url", media.url, "--ffmpeg", str(ffmpeg), "--only", "02"])
