#!/usr/bin/env python3
"""Build the blind review page and the blinding records for a batch.

Reads batches/<id>/runs.jsonl, shuffles every run with the manifest's random seed and
writes batches/<id>/blind/index.html, blind/sheet.jsonl (one row per blinded item,
without the variant) and blind/key.jsonl (blind id to Run id, variant and annotation,
read only by the report step). Delivered clips are downloaded into blind/clips/ unless
--no-download is given, in which case the page streams them from the media service.

The reviewer fills labels.jsonl by hand (or by exporting the page's form, which prints
one label per item as JSON). Human scoring is locked before the judge runs.
"""

from __future__ import annotations

import argparse
import html
import json
import sys
from pathlib import Path
from typing import Any

from blinding import build_blind_mapping
from media_api import MediaClient
from records import (
    FAILURE_TAGS,
    LABEL_SCHEMA,
    read_json,
    read_jsonl,
    validate_blind_key_row,
    validate_blind_sheet_row,
    validate_manifest,
    validate_run,
    write_jsonl,
)

HERE = Path(__file__).resolve().parent

DIMENSIONS = (
    ("notBroken", "Not broken", "Plays, duration, subtitles and cut points look right"),
    ("asRequested", "As requested", "Meets the requirement without distorting the speaker's meaning"),
    ("contentQuality", "Done well: content", "Understandable without the source; the opening draws you in"),
    ("presentationQuality", "Done well: presentation", "Pacing, framing and subtitles look good"),
)
VERDICT_DIMENSIONS = ("asRequested", "contentQuality")

STYLE = """
body { font-family: system-ui, sans-serif; max-width: 980px; margin: 2rem auto; padding: 0 1rem; color: #222; }
section.item { border: 1px solid #ccc; border-radius: 8px; padding: 1rem 1.25rem; margin: 1.5rem 0; }
video { width: 270px; height: 480px; background: #000; }
.row { display: flex; gap: 1.5rem; align-items: flex-start; flex-wrap: wrap; }
.form { flex: 1; min-width: 320px; }
fieldset { border: none; padding: 0; margin: 0 0 0.75rem; }
legend { font-weight: 600; }
small { color: #555; }
textarea { width: 100%; min-height: 3rem; }
.tags label { display: inline-block; margin-right: 0.75rem; }
pre { background: #f5f5f5; padding: 0.75rem; overflow-x: auto; white-space: pre-wrap; }
"""

SCRIPT = """
function collect() {
  const out = [];
  document.querySelectorAll('section.item').forEach(section => {
    const id = section.dataset.blindId;
    const kind = section.dataset.kind;
    const scores = {};
    const verdicts = {};
    ['notBroken', 'asRequested', 'contentQuality', 'presentationQuality'].forEach(dim => {
      const picked = section.querySelector(`input[name="${id}-${dim}"]:checked`);
      scores[dim] = kind === 'clip' && picked ? Number(picked.value) : null;
    });
    ['asRequested', 'contentQuality'].forEach(dim => {
      const picked = section.querySelector(`input[name="${id}-${dim}-verdict"]:checked`);
      verdicts[dim] = kind === 'clip' && picked ? picked.value === 'pass' : null;
    });
    const tags = Array.from(section.querySelectorAll('input[type="checkbox"][data-tag]:checked')).map(el => el.dataset.tag);
    const match = section.querySelector(`input[name="${id}-reason-match"]:checked`);
    out.push({
      schemaVersion: 'LABEL_SCHEMA',
      blindId: id,
      reviewer: document.getElementById('reviewer').value || 'author',
      scores: scores,
      verdicts: verdicts,
      issues: section.querySelector('textarea').value,
      failureTags: tags,
      checkGap: !!section.querySelector('input[data-check-gap]:checked'),
      reasonMatchesAnnotation: kind === 'no_clip' && match ? match.value === 'yes' : null,
    });
  });
  document.getElementById('export').textContent = out.map(o => JSON.stringify(o)).join('\\n');
}
""".replace("LABEL_SCHEMA", LABEL_SCHEMA)


def _radio_group(name: str, values: list[tuple[str, str]]) -> str:
    return " ".join(
        f'<label><input type="radio" name="{html.escape(name)}" value="{html.escape(value)}"> {html.escape(label)}</label>'
        for value, label in values
    )


def render_item(row: dict[str, Any]) -> str:
    blind_id = row["blindId"]
    e = html.escape
    parts = [f'<section class="item" data-blind-id="{e(blind_id)}" data-kind="{e(row["kind"])}">',
             f"<h2>{e(blind_id)}</h2>",
             f"<p><strong>Requirement:</strong> {e(row['requirement'])}</p>",
             f'<p><strong>Source:</strong> {e(row["sourceTitle"])} '
             f'(<a href="{e(row["sourceUrl"])}" target="_blank" rel="noopener">original video</a>)</p>']
    if row["kind"] == "clip":
        parts.append('<div class="row">')
        parts.append(f'<video controls preload="metadata" src="{e(row["clipSrc"])}"></video>')
        parts.append('<div class="form">')
        for key, label, hint in DIMENSIONS:
            parts.append(f"<fieldset><legend>{e(label)} <small>{e(hint)}</small></legend>")
            parts.append(_radio_group(f"{blind_id}-{key}", [(str(n), str(n)) for n in range(1, 6)]))
            if key in VERDICT_DIMENSIONS:
                parts.append("<br><small>Overall:</small> " + _radio_group(
                    f"{blind_id}-{key}-verdict", [("pass", "pass"), ("fail", "fail")]))
            parts.append("</fieldset>")
        parts.append('<fieldset><legend>Check gap</legend><label><input type="checkbox" data-check-gap> '
                     "The technical checks passed but I see a technical problem</label></fieldset>")
        parts.append("</div></div>")
    else:
        parts.append("<p><em>No clip.</em> The run ended with <code>" + e(row["statedOutcome"]) + "</code>. "
                     "Judge only whether the stated reason matches the annotation; the annotation is revealed "
                     "when the report unblinds.</p>")
        parts.append(f"<pre>{e(row.get('statedReason') or '(no reason recorded)')}</pre>")
        parts.append("<fieldset><legend>Does the stated reason match the annotation?</legend>" + _radio_group(
            f"{blind_id}-reason-match", [("yes", "yes"), ("no", "no")]) + "</fieldset>")
    parts.append('<fieldset class="tags"><legend>Failure tags</legend>' + " ".join(
        f'<label><input type="checkbox" data-tag="{e(tag)}"> {e(tag)}</label>' for tag in FAILURE_TAGS) + "</fieldset>")
    parts.append("<fieldset><legend>Issues</legend><textarea placeholder=\"What is wrong, if anything\"></textarea></fieldset>")
    parts.append("</section>")
    return "\n".join(parts)


def render_page(batch_id: str, sheet: list[dict[str, Any]]) -> str:
    clips = [row for row in sheet if row["kind"] == "clip"]
    no_clips = [row for row in sheet if row["kind"] == "no_clip"]
    body = [f"<h1>Blind review: batch {html.escape(batch_id)}</h1>",
            "<p>Watch each clip first and decide whether it can be followed on its own; then open the original "
            "video and check whether the clip distorts what the speaker meant. Variants are hidden; the order is "
            "shuffled with the batch seed.</p>",
            '<p><label>Reviewer: <input id="reviewer" value="author"></label></p>',
            f"<h2>Delivered clips ({len(clips)})</h2>"]
    body.extend(render_item(row) for row in clips)
    body.append(f"<h2>No clip ({len(no_clips)})</h2>")
    body.extend(render_item(row) for row in no_clips)
    body.append('<h2>Export</h2><p><button onclick="collect()">Collect labels</button> '
                "Copy the lines below into <code>labels.jsonl</code>.</p><pre id=\"export\"></pre>")
    return ("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">"
            f"<title>Blind review {html.escape(batch_id)}</title><style>{STYLE}</style></head>"
            f"<body>{''.join(body)}<script>{SCRIPT}</script></body></html>\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("batch_id")
    parser.add_argument("--batches-dir", type=Path, default=HERE / "batches")
    parser.add_argument("--dataset-dir", type=Path, default=HERE / "dataset")
    parser.add_argument("--media-url", default="http://localhost:9201")
    parser.add_argument("--no-download", action="store_true", help="point <video> at the media service instead")
    args = parser.parse_args(argv)

    batch_dir = args.batches_dir / args.batch_id
    manifest = validate_manifest(read_json(batch_dir / "manifest.json"))
    runs = [validate_run(run) for run in read_jsonl(batch_dir / "runs.jsonl")]
    if not runs:
        print("runs.jsonl is empty; run the batch first", file=sys.stderr)
        return 1
    rows = {row["sampleId"]: row for row in read_jsonl(args.dataset_dir / f"{manifest['datasetVersion']}.jsonl")}
    blind_dir = batch_dir / "blind"
    media = MediaClient(args.media_url)

    def clip_src_for(run: dict[str, Any]) -> str:
        if args.no_download:
            return run["clipUrl"] or media.clip_url(run["clipSha256"])
        destination = blind_dir / "clips" / f"{run['clipSha256']}.mp4"
        if not destination.exists():
            media.download_clip(run["clipSha256"], destination)
        return f"clips/{destination.name}"

    try:
        sheet, key = build_blind_mapping(runs, rows, manifest["randomSeed"], clip_src_for)
    finally:
        media.close()
    for row in sheet:
        validate_blind_sheet_row(row)
    for row in key:
        validate_blind_key_row(row)
    write_jsonl(blind_dir / "sheet.jsonl", sheet)
    write_jsonl(blind_dir / "key.jsonl", key)
    (blind_dir / "index.html").write_text(render_page(args.batch_id, sheet), encoding="utf-8")
    print(f"{len(sheet)} items -> {blind_dir / 'index.html'}")
    print(json.dumps({"clips": sum(r['kind'] == 'clip' for r in sheet), "noClip": sum(r['kind'] == 'no_clip' for r in sheet)}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
