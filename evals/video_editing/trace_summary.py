"""Pure functions that turn Emberling API payloads into evaluation facts.

Nothing here calls the network. The inputs are the Run snapshot (GET /api/runs/{id}) and
the Agent Trace (GET /api/runs/{id}/nodes/{nodeRunId}/agent) exactly as the API returns
them; the outputs are the fields a run record carries.
"""

from __future__ import annotations

import json
from datetime import datetime
from typing import Any

AGENT_NODE_TYPE = "agent"
TRANSCRIPT_NODE_TYPE = "media_transcript"
# An Execution Artifact id is derived from its content digest: the prefix plus the first
# 32 hex characters of the SHA-256. The media service hashes the canonical timeline the
# same way, so the timeline Artifact can be named from the timelineSha256 a fact binds.
ARTIFACT_ID_PREFIX = "artifact_"
ARTIFACT_ID_DIGEST_CHARS = 32

FACT_MEDIA_CHECKED = "media_checked"
FACT_CONTENT_REVIEWED = "content_reviewed"
FACT_CLIP_RENDERED = "clip_rendered"


def artifact_id_for_sha256(sha256: str) -> str:
    return ARTIFACT_ID_PREFIX + sha256[:ARTIFACT_ID_DIGEST_CHARS]


def find_node_run(snapshot: dict[str, Any], node_type: str) -> dict[str, Any] | None:
    for node_run in snapshot.get("nodeRuns", []):
        if node_run.get("nodeType") == node_type:
            return node_run
    return None


def _parse_port_object(output: Any) -> dict[str, Any] | None:
    """A node's single text port may carry canonical JSON; decode it when it does."""
    if not isinstance(output, dict):
        return None
    for value in output.values():
        if isinstance(value, dict):
            return value
        if isinstance(value, str):
            try:
                decoded = json.loads(value)
            except json.JSONDecodeError:
                continue
            if isinstance(decoded, dict):
                return decoded
    return None


def transcript_artifact_id(snapshot: dict[str, Any]) -> str | None:
    node_run = find_node_run(snapshot, TRANSCRIPT_NODE_TYPE)
    if node_run is None:
        return None
    output = node_run.get("output")
    if isinstance(output, dict):
        if isinstance(output.get("artifactId"), str):
            return output["artifactId"]
        for value in output.values():
            if isinstance(value, dict) and isinstance(value.get("artifactId"), str):
                return value["artifactId"]
    return None


def agent_report(snapshot: dict[str, Any]) -> dict[str, Any] | None:
    """The Agent's own FINAL output, published as agentReport on the Agent node's port."""
    node_run = find_node_run(snapshot, AGENT_NODE_TYPE)
    if node_run is None:
        return None
    port = _parse_port_object(node_run.get("output"))
    if port is None:
        return None
    report = port.get("agentReport")
    return report if isinstance(report, dict) else None


def count_facts(trace: dict[str, Any]) -> dict[str, int]:
    counts = {"mediaCheckedTotal": 0, "mediaCheckedFailed": 0, "contentReviewedTotal": 0, "contentReviewedFailed": 0}
    for fact in trace.get("facts", {}).get("items", []):
        fact_type = fact.get("factType")
        failed = fact.get("verdict") is False
        if fact_type == FACT_MEDIA_CHECKED:
            counts["mediaCheckedTotal"] += 1
            counts["mediaCheckedFailed"] += int(failed)
        elif fact_type == FACT_CONTENT_REVIEWED:
            counts["contentReviewedTotal"] += 1
            counts["contentReviewedFailed"] += int(failed)
    return counts


def clip_rendered_fact(trace: dict[str, Any], clip_sha256: str | None) -> dict[str, Any] | None:
    """The clip_rendered fact for the delivered clip, else the latest one recorded."""
    latest = None
    for fact in trace.get("facts", {}).get("items", []):
        if fact.get("factType") != FACT_CLIP_RENDERED:
            continue
        if clip_sha256 and fact.get("subject") == clip_sha256:
            return fact
        latest = fact
    return latest


def _bindings(fact: dict[str, Any] | None) -> dict[str, Any]:
    if fact is None:
        return {}
    raw = fact.get("bindings")
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, str):
        try:
            decoded = json.loads(raw)
            return decoded if isinstance(decoded, dict) else {}
        except json.JSONDecodeError:
            return {}
    return {}


def _elapsed_seconds(run: dict[str, Any]) -> float | None:
    started, completed = run.get("startedAt"), run.get("completedAt")
    if not started or not completed:
        return None
    try:
        delta = _parse_time(completed) - _parse_time(started)
    except ValueError:
        return None
    return round(delta.total_seconds(), 3)


def _parse_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def _failure_reason(snapshot: dict[str, Any], trace: dict[str, Any] | None) -> str | None:
    parts: list[str] = []
    run_error = snapshot.get("run", {}).get("error")
    if isinstance(run_error, dict):
        parts.append(f"run {run_error.get('code')}: {run_error.get('message')}")
    for node_run in snapshot.get("nodeRuns", []):
        error = node_run.get("error")
        if isinstance(error, dict):
            parts.append(f"{node_run.get('nodeId')} {error.get('code')}: {error.get('message')}")
    if trace:
        completion = trace.get("agentRun", {}).get("completion")
        if isinstance(completion, dict):
            for missing in completion.get("missing", []) or []:
                parts.append(f"missing {missing.get('code')}: {missing.get('description')}")
        termination = trace.get("agentRun", {}).get("termination")
        if termination and termination != "FINAL_RESPONSE":
            parts.append(f"termination {termination}")
    return "; ".join(parts) or None


def summarize(snapshot: dict[str, Any], trace: dict[str, Any] | None, *, clip_url_for) -> dict[str, Any]:
    """Derive the outcome-bearing fields of a run record.

    Outcome: the completion verdict when the Agent Run has one; a Run that ended without
    a verdict is FAILED. The clip digest comes from the Agent's report when the clip was
    delivered, confirmed against the clip_rendered fact; the clip URL is the media
    service's address for that digest.
    """
    run = snapshot["run"]
    agent_node = find_node_run(snapshot, AGENT_NODE_TYPE)
    agent_run = (trace or {}).get("agentRun") or {}
    completion = agent_run.get("completion") if isinstance(agent_run, dict) else None
    outcome = completion.get("outcome") if isinstance(completion, dict) else None
    if outcome not in ("CLIP_DELIVERED", "CANNOT_SATISFY", "INCOMPLETE"):
        outcome = "FAILED"

    report = agent_report(snapshot) or {}
    clip_sha256 = None
    timeline_sha256 = None
    if outcome == "CLIP_DELIVERED":
        clip_sha256 = report.get("clipSha256") if isinstance(report.get("clipSha256"), str) else None
        fact = clip_rendered_fact(trace or {}, clip_sha256)
        if clip_sha256 is None and fact is not None:
            clip_sha256 = fact.get("subject")
        timeline = _bindings(fact).get("timelineSha256")
        timeline_sha256 = timeline if isinstance(timeline, str) else None
    else:
        fact = clip_rendered_fact(trace or {}, None)
        timeline = _bindings(fact).get("timelineSha256")
        timeline_sha256 = timeline if isinstance(timeline, str) else None

    cannot_reason = report.get("cannotSatisfyReason") if outcome == "CANNOT_SATISFY" else None
    counts = count_facts(trace or {})
    return {
        "agentNodeRunId": agent_node.get("id") if agent_node else None,
        "agentRunId": agent_run.get("id") if isinstance(agent_run, dict) else None,
        "outcome": outcome,
        "runStatus": run.get("status"),
        "clipSha256": clip_sha256,
        "clipUrl": clip_url_for(clip_sha256) if clip_sha256 else None,
        "failureReason": _failure_reason(snapshot, trace) if outcome in ("INCOMPLETE", "FAILED") else None,
        "cannotSatisfyReason": cannot_reason if isinstance(cannot_reason, str) else None,
        "elapsedSeconds": _elapsed_seconds(run),
        "revisions": {"mediaChecked": counts["mediaCheckedFailed"], "contentReviewed": counts["contentReviewedFailed"]},
        "checks": counts,
        "turnsUsed": len((trace or {}).get("turns", []) or []),
        "transcriptArtifactId": transcript_artifact_id(snapshot),
        "timelineArtifactId": artifact_id_for_sha256(timeline_sha256) if timeline_sha256 else None,
        "timelineSha256": timeline_sha256,
    }
