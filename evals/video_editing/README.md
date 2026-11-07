# Video-editing Agent evaluation

Tooling and records for evaluating the video-editing Agent demo: six interview videos,
each run once with the basic Definition and once with the reviewed Definition, scored by
one blinded human reviewer and by a text-only judge model, and summarised in a report.

The evaluation runs outside the Runtime. It creates and reads Runs through the public
Emberling REST API and never edits an execution record. Every file it writes is a
versioned JSON or JSONL record whose fields are fixed by `records.py`, so the records can
later be loaded into a database without reinterpretation.

## Honest limits

- Two runs per sample cannot show that one variant is better than the other; the batch
  exists to find failure modes and to start calibrating the judge.
- The reviewer is the author of the system. Blinding hides the variant and shuffles the
  order, which reduces bias but does not remove it, and the reviewer may still guess a
  variant from the content.
- The judge sees only text: the user requirement, the full transcript and the chosen
  sentences. The reviewer sees the clip and the source video. The same batch tunes and
  checks the judge, so its agreement rate is a preliminary calibration, not evidence that
  the judge is trustworthy.
- Four of the six videos come from one interview series with a similar style, which
  narrows the failure modes the batch can expose.

## Pipeline

```text
prepare_dataset.py -> run_batch.py -> make_blind_sheet.py -> (human labels) -> run_judge.py -> make_report.py
```

| Script | What it does |
|---|---|
| `prepare_dataset.py` | Verifies each local source video against its recorded SHA-256, cuts the declared continuous range with ffmpeg where a sample has one, uploads the prepared file to the media service and writes `dataset/v1.prepared.json`. Idempotent; downloads nothing. |
| `run_batch.py` | Reads a frozen `batches/<id>/manifest.json`, creates the basic and reviewed Run for every sample serially, waits for each to end and appends one line per Run to `runs.jsonl`. Resumable; an `eval` batch never runs a pair twice and never replaces a failed record. |
| `make_blind_sheet.py` | Shuffles every run with the manifest's seed and writes a static review page (`blind/index.html`), `blind/sheet.jsonl` (no variant, no Run id, no annotation) and `blind/key.jsonl` (the mapping used only at unblinding). Downloads delivered clips into `blind/clips/`. |
| `run_judge.py` | Refuses to run until `labels.jsonl` has a row for every blinded item. Builds the judge input from Emberling's artifact content route and calls the OpenRouter chat completions API; writes `judge.jsonl` with the raw answer, parsed scores, token usage and cost. |
| `make_report.py` | Unblinds and writes `batches/<id>/report.md`: outcomes, failure tags, human scores, judge agreement and every disagreement, check pass rate, revision trigger rate, task cost and judge cost separately, elapsed time, runs without a clip, and per-sample before/after for a regression batch. |

Every script accepts `--help`. All of them use the Python standard library plus `httpx`,
pinned with `uv` (`pyproject.toml`, `uv.lock`, `.python-version`). They do not import
anything from the Emberling backend.

## Records

| File | Schema | Content |
|---|---|---|
| `dataset/v1.jsonl` | `dataset/v1` | One row per sample: id, slug, source title and page, license and attribution, local file name and SHA-256, optional cut range, crop centre, and the annotation (requirement, whether it is expected to be satisfiable, sentences that mislead when cut alone, qualifiers that must be kept, who annotated and the annotation status). |
| `dataset/v1.prepared.json` | `prepared/v1` | Per sample: the media service `sourceId`, `sha256`, `durationMs` and `mediaType` of the prepared file. |
| `batches/<id>/manifest.json` | `batch-manifest/v1` | Batch id, type (`eval`, `regression`, `showcase`), dataset version, both Definition ids and versions, models (decision, review, judge), prompt version, Tool schema digest, rule version, random seed; a regression batch also names its base batch and hypothesis. |
| `batches/<id>/runs.jsonl` | `run/v1` | Per Run: sample, variant, Run id, Agent Run id, outcome (`CLIP_DELIVERED`, `CANNOT_SATISFY`, `INCOMPLETE`, `FAILED`), clip digest and URL, failure or cannot-satisfy reason, model cost as the API reports it, elapsed seconds, revisions (failed `media_checked` and failed `content_reviewed` facts), check counts, turns used, transcript and timeline Artifact ids, and the digest of the manifest it ran under. |
| `batches/<id>/blind/sheet.jsonl` | `blind-sheet/v1` | One row per blinded item: blind id, kind (`clip` or `no_clip`), requirement, source title and page, clip source or the stated outcome and reason. Never the variant. |
| `batches/<id>/blind/key.jsonl` | `blind-key/v1` | Blind id to Run id, sample, variant and annotation. Read only by the judge and report steps. |
| `batches/<id>/labels.jsonl` | `label/v1` | Per blinded item: reviewer, 1-5 scores for the four human dimensions, pass/fail verdicts for the two dimensions the judge also scores, free-text issues, failure tags, a check-gap flag, and for a run without a clip whether the stated reason matches the annotation. |
| `batches/<id>/judge.jsonl` | `judge/v1` | Per judged Run: judge version, input digest, model, raw answer, parsed verdicts and scores, token usage and cost. |

Human dimensions: not broken, as requested, done well (content), done well (presentation).
The judge scores as requested and done well (content) only. Agreement is computed on the
pass/fail verdicts of those two dimensions.

## Batch rules

- `eval`: frozen once it has run. The manifest digest is recorded with every run line; a
  changed manifest is refused and no pair is ever run again. Failed runs stay in the
  record and in every denominator.
- `regression`: names the base batch and the hypothesis behind the one change being
  verified. A pair may be run again with `--rerun sample:variant`; the old line is kept.
- `showcase`: runs for demonstration, outside the statistics.

## Running a batch

Prerequisites: Emberling (`http://localhost:8080` by default) with the two Definitions
saved, the media service (`http://localhost:9201` by default), `ffmpeg` on the path or
named with `--ffmpeg`, and the six source videos in a local directory (`--sources-dir`).

```sh
cd evals/video_editing
uv sync --frozen
uv run --frozen python prepare_dataset.py --sources-dir /path/to/sources --media-url http://localhost:9201
# write batches/<id>/manifest.json by hand (see the schema above), then:
uv run --frozen python run_batch.py <id>
uv run --frozen python make_blind_sheet.py <id>
# open batches/<id>/blind/index.html, score every item, paste the exported lines into
# batches/<id>/labels.jsonl, then:
EMBERLING_MODEL_API_KEY=... uv run --frozen python run_judge.py <id>
uv run --frozen python make_report.py <id>
```

The judge key is read from `EMBERLING_MODEL_API_KEY` only and is never written to a
record. Costs in the report are the model charges Emberling reports per Run; local
transcription and rendering are not counted. Judge cost is listed separately.

## Sources

The six videos are from Wikimedia Commons. Samples 1 to 4 are public domain; samples 5 and
6 are licensed CC BY 3.0 and the dataset carries their attribution and a note that the
clips are edited excerpts. The videos are not part of this repository; only their source
pages, licenses and digests are. Nothing here implies endorsement by the people in the
videos or by their publishers.

## Tests

```sh
make evals-test
```

The self-tests use fixed records and in-process fake HTTP services (Emberling, the media
service and OpenRouter) and need no running service. They cover schema validation, the
blind mapping round trip and that the sheet never leaks a variant, statistics with failed
runs in the denominator, agreement and disagreement listing, report rendering including the
regression before/after table, batch resumption and eval-batch immutability, the judge's
refusal before labels are locked, and that the judge key never reaches a file.
