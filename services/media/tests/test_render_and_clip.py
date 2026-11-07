"""Timeline check, render and clip check on the speech fixture, with real FFmpeg."""

from __future__ import annotations

import hashlib
import json
import shutil

import pytest

from app import rules_v1


@pytest.fixture
def chain(service):
    """Uploads the fixture, transcribes it and checks a timeline over every sentence."""
    source = service.upload()
    transcript = service.run("ext_chain_tr", "TRANSCRIBE", {"sourceId": source["sourceId"]})["result"]
    last = f"S{transcript['sentenceCount']}"
    material_end_ms = int(float(transcript["material"].splitlines()[-1].split("\t")[2]) * 1000)
    check = service.run(
        "ext_chain_ct",
        "CHECK_TIMELINE",
        {
            "sourceId": source["sourceId"],
            "transcriptSha256": transcript["transcriptSha256"],
            "targetDurationMs": material_end_ms,
            "segments": [{"from": "S1", "to": last}],
        },
    )
    assert check["status"] == "SUCCEEDED", check
    return source, transcript, check["result"]


def test_check_timeline_result_is_canonical_and_passes(service, chain) -> None:
    source, transcript, result = chain
    assert result["kind"] == "TIMELINE"
    assert result["ruleVersion"] == rules_v1.RULE_VERSION
    assert result["passed"] is True, result["findings"]
    text = result["timeline"]
    assert hashlib.sha256(text.encode("utf-8")).hexdigest() == result["subjectSha256"] == result["timelineSha256"]
    timeline = json.loads(text)
    assert list(timeline) == ["cropCenterX", "durationMs", "ruleVersion", "segments", "sourceId", "sourceSha256", "targetDurationMs", "transcriptSha256"]
    assert timeline["cropCenterX"] == 0.5
    assert timeline["ruleVersion"] == rules_v1.RULE_VERSION
    assert list(timeline["segments"][0]) == ["endMs", "from", "startMs", "text", "to"]
    first_line_text = transcript["material"].splitlines()[0].split("\t")[3]
    assert timeline["segments"][0]["text"].startswith(first_line_text)
    assert timeline["sourceSha256"] == source["sha256"]
    assert timeline["transcriptSha256"] == transcript["transcriptSha256"]
    assert timeline["durationMs"] == result["durationMs"] > 0
    stored = service.settings.data_dir / "timelines" / f"{result['timelineSha256']}.json"
    assert stored.read_text("utf-8") == text

    again = service.run(
        "ext_chain_ct2",
        "CHECK_TIMELINE",
        {
            "sourceId": source["sourceId"],
            "transcriptSha256": transcript["transcriptSha256"],
            "targetDurationMs": timeline["targetDurationMs"],
            "segments": [{"from": "S1", "to": f"S{transcript['sentenceCount']}"}],
        },
    )["result"]
    assert again["timelineSha256"] == result["timelineSha256"]


def test_check_timeline_reports_unknown_overlap_and_duration(service, chain) -> None:
    source, transcript, _ = chain
    view = service.run(
        "ext_bad_tl",
        "CHECK_TIMELINE",
        {
            "sourceId": source["sourceId"],
            "transcriptSha256": transcript["transcriptSha256"],
            "segments": [{"from": "S1", "to": "S2"}, {"from": "S2", "to": "S2"}, {"from": "S1", "to": "S999"}],
        },
    )
    assert view["status"] == "SUCCEEDED"
    result = view["result"]
    assert result["passed"] is False
    codes = {(f["code"], f.get("segmentIndex")) for f in result["findings"]}
    assert (rules_v1.TIMELINE_SEGMENTS_OVERLAP, 1) in codes
    assert (rules_v1.TIMELINE_SENTENCE_UNKNOWN, 2) in codes
    assert (rules_v1.TIMELINE_DURATION_OUT_OF_RANGE, None) in codes
    assert all(f["level"] in ("BLOCKING", "WARNING") for f in result["findings"])
    # The timeline is still recorded so the verdict refers to a stored subject.
    assert (service.settings.data_dir / "timelines" / f"{result['timelineSha256']}.json").exists()


def test_check_timeline_target_out_of_range_is_blocking(service, chain) -> None:
    source, transcript, _ = chain
    result = service.run(
        "ext_short_tl",
        "CHECK_TIMELINE",
        {
            "sourceId": source["sourceId"],
            "transcriptSha256": transcript["transcriptSha256"],
            "targetDurationMs": 30_000,
            "segments": [{"from": "S1", "to": "S1"}],
        },
    )["result"]
    assert result["passed"] is False
    assert [f["code"] for f in result["findings"]] == [rules_v1.TIMELINE_DURATION_OUT_OF_RANGE]
    assert result["findings"][0]["level"] == "BLOCKING"


@pytest.fixture
def rendered(service, chain):
    _, _, timeline_result = chain
    render = service.run("ext_render", "RENDER", {"timelineSha256": timeline_result["timelineSha256"]})
    assert render["status"] == "SUCCEEDED", render
    return timeline_result, render["result"]


def test_render_same_timeline_twice_gives_same_clip(service, rendered) -> None:
    timeline_result, first = rendered
    assert first["mediaType"] == "video/mp4"
    assert first["clipUrl"] == f"http://media.test/v1/clips/{first['clipSha256']}"
    assert first["timelineSha256"] == timeline_result["timelineSha256"]
    assert abs(first["durationMs"] - timeline_result["durationMs"]) <= 500
    clip = service.client.get(f"/v1/clips/{first['clipSha256']}")
    assert clip.status_code == 200
    assert hashlib.sha256(clip.content).hexdigest() == first["clipSha256"]
    assert len(clip.content) == first["sizeBytes"]

    second = service.run("ext_render_2", "RENDER", {"timelineSha256": timeline_result["timelineSha256"]})["result"]
    assert second["clipSha256"] == first["clipSha256"]
    assert second["sizeBytes"] == first["sizeBytes"]


def test_changing_the_source_crop_does_not_change_an_existing_timeline_render(service, rendered) -> None:
    timeline_result, first = rendered
    moved = service.upload(crop_center_x=0.2)
    assert moved["cropCenterX"] == 0.2
    again = service.run("ext_render_moved", "RENDER", {"timelineSha256": timeline_result["timelineSha256"]})["result"]
    assert again["clipSha256"] == first["clipSha256"]


    source_id = json.loads(timeline_result["timeline"])["sourceId"]
    new_check = service.run(
        "ext_ct_moved",
        "CHECK_TIMELINE",
        {
            "sourceId": source_id,
            "transcriptSha256": json.loads(timeline_result["timeline"])["transcriptSha256"],
            "targetDurationMs": json.loads(timeline_result["timeline"])["targetDurationMs"],
            "segments": [{"from": s["from"], "to": s["to"]} for s in json.loads(timeline_result["timeline"])["segments"]],
        },
    )["result"]
    assert json.loads(new_check["timeline"])["cropCenterX"] == 0.2
    assert new_check["timelineSha256"] != timeline_result["timelineSha256"]
    # The fixture is a solid colour, so a moved window renders the same pixels; only the
    # timeline identity and the render's independence from the source record are asserted.
    moved_render = service.run("ext_render_new", "RENDER", {"timelineSha256": new_check["timelineSha256"]})["result"]
    assert moved_render["timelineSha256"] == new_check["timelineSha256"]


def test_rendered_clip_passes_the_clip_check(service, rendered) -> None:
    _, render = rendered
    result = service.run("ext_cc_ok", "CHECK_CLIP", {"clipSha256": render["clipSha256"]})["result"]
    assert result["kind"] == "CLIP"
    assert result["ruleVersion"] == rules_v1.RULE_VERSION
    assert result["subjectSha256"] == render["clipSha256"]
    assert result["timelineSha256"] == render["timelineSha256"]
    assert set(result) == {"kind", "passed", "ruleVersion", "subjectSha256", "timelineSha256", "findings"}
    assert result["passed"] is True, result["findings"]


def test_render_refuses_a_timeline_with_unresolved_segments(service, chain) -> None:
    source, transcript, _ = chain
    bad = service.run(
        "ext_unres",
        "CHECK_TIMELINE",
        {"sourceId": source["sourceId"], "transcriptSha256": transcript["transcriptSha256"], "segments": [{"from": "S1", "to": "S999"}]},
    )["result"]
    view = service.run("ext_unres_render", "RENDER", {"timelineSha256": bad["timelineSha256"]})
    assert view["status"] == "FAILED"
    assert view["error"]["code"] == "RENDER_FAILED"


def derive_clip(service, ffmpeg, clip_sha256: str, name: str, args: list[str], keep_cues: bool = True) -> str:
    """Transforms a rendered clip with ffmpeg and stores it beside the original."""
    clips = service.settings.data_dir / "clips"
    out = service.settings.data_dir / "tmp" / f"{name}.mp4"
    ffmpeg.run(["-y", "-v", "error", "-i", str(clips / f"{clip_sha256}.mp4"), *args, str(out)])
    sha256 = hashlib.sha256(out.read_bytes()).hexdigest()
    shutil.move(out, clips / f"{sha256}.mp4")
    if keep_cues:
        shutil.copy(clips / f"{clip_sha256}.cues.json", clips / f"{sha256}.cues.json")
    return sha256


def codes_of(service, task_id: str, clip_sha256: str) -> dict[str, str]:
    result = service.run(task_id, "CHECK_CLIP", {"clipSha256": clip_sha256})["result"]
    return {f["code"]: f["level"] for f in result["findings"]}


def test_black_frames_are_blocking(service, ffmpeg, rendered) -> None:
    _, render = rendered
    sha = derive_clip(
        service,
        ffmpeg,
        render["clipSha256"],
        "black",
        ["-vf", "drawbox=x=0:y=0:w=iw:h=ih:color=black:t=fill:enable='between(t,1,2.5)'", "-c:a", "copy"],
    )
    findings = codes_of(service, "ext_cc_black", sha)
    assert findings.get(rules_v1.CLIP_BLACK_FRAMES) == "BLOCKING"
    assert rules_v1.CLIP_SILENCE not in findings


def test_silence_is_a_warning(service, ffmpeg, rendered) -> None:
    _, render = rendered
    sha = derive_clip(
        service,
        ffmpeg,
        render["clipSha256"],
        "silent",
        ["-af", "volume=enable='between(t,1,3.5)':volume=0", "-c:v", "copy"],
    )
    findings = codes_of(service, "ext_cc_silent", sha)
    assert findings.get(rules_v1.CLIP_SILENCE) == "WARNING"
    assert rules_v1.CLIP_BLACK_FRAMES not in findings
    result = service.client.get("/v1/tasks/ext_cc_silent").json()["result"]
    assert result["passed"] is True


def test_wrong_aspect_is_blocking(service, ffmpeg, rendered) -> None:
    _, render = rendered
    sha = derive_clip(service, ffmpeg, render["clipSha256"], "wide", ["-vf", "scale=720:1280", "-c:a", "copy"])
    findings = codes_of(service, "ext_cc_wide", sha)
    assert findings.get(rules_v1.CLIP_WRONG_DIMENSIONS) == "BLOCKING"


def test_missing_audio_stream_is_blocking(service, ffmpeg, rendered) -> None:
    _, render = rendered
    sha = derive_clip(service, ffmpeg, render["clipSha256"], "mute", ["-an", "-c:v", "copy"])
    findings = codes_of(service, "ext_cc_mute", sha)
    assert findings.get(rules_v1.CLIP_MISSING_STREAM) == "BLOCKING"


def test_clip_without_cue_record_fails_subtitle_and_timeline_rules(service, ffmpeg, rendered) -> None:
    _, render = rendered
    sha = derive_clip(service, ffmpeg, render["clipSha256"], "nocues", ["-c", "copy", "-map_metadata", "0", "-metadata", "title=x"], keep_cues=False)
    findings = codes_of(service, "ext_cc_nocues", sha)
    assert findings.get(rules_v1.CLIP_TIMELINE_UNKNOWN) == "BLOCKING"


def test_subtitle_cue_outside_its_sentence_is_blocking(service, rendered) -> None:
    _, render = rendered
    cues_path = service.settings.data_dir / "clips" / f"{render['clipSha256']}.cues.json"
    record = json.loads(cues_path.read_text())
    record["cues"][0]["endMs"] += 60_000
    cues_path.write_text(json.dumps(record))
    findings = codes_of(service, "ext_cc_cue", render["clipSha256"])
    assert findings.get(rules_v1.CLIP_SUBTITLE_TIMING) == "BLOCKING"


def test_unknown_clip_fails_the_task(service) -> None:
    view = service.run("ext_cc_unknown", "CHECK_CLIP", {"clipSha256": "e" * 64})
    assert view["status"] == "FAILED" and view["error"]["code"] == "CLIP_NOT_FOUND"
    unknown_timeline = service.run("ext_render_unknown", "RENDER", {"timelineSha256": "e" * 64})
    assert unknown_timeline["error"]["code"] == "TIMELINE_NOT_FOUND"
    assert service.client.get("/v1/clips/" + "e" * 64).status_code == 404
    assert service.client.get("/v1/clips/not-a-hash").status_code == 404
