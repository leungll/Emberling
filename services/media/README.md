# Emberling media service

A small HTTP service that turns a talking-head video into a vertical clip, one
deterministic step at a time: upload the source, transcribe it into numbered
sentences, check a sentence selection against fixed rules, render the selection
with burned-in subtitles, and check the rendered clip. It is an external task
service: Emberling submits tasks, receives a callback when they finish, and can
poll for the result at any time. The service makes no decisions about what to
do next and calls no language model.

## Requirements

- Python 3.12 (pinned in `.python-version`; `uv` downloads it when missing)
- [uv](https://docs.astral.sh/uv/) for dependencies, locked in `uv.lock`
- FFmpeg and ffprobe with the `subtitles`, `blackdetect` and `silencedetect`
  filters and the `libx264` and `aac` encoders. The `subtitles` filter needs an
  FFmpeg built with libass; Debian's `ffmpeg` package has it, some Homebrew
  builds do not. The readiness probe reports what is missing.
- A font libass can render with (for example `fonts-dejavu-core` on Debian).

The transcription model is downloaded on first use into the Hugging Face cache
(`HF_HOME`); `small.en` is about 500 MB. Tests use `tiny.en`.

## Running

From the repository root:

```sh
make media-dev     # runs the service with uv
make media-test    # runs the test suite (real FFmpeg, tiny.en)
```

Or inside `services/media`: `uv run --frozen python -m app` and
`uv run --frozen pytest`. With Docker Compose, `docker compose -f
deploy/compose.yaml --profile media up` builds `deploy/Dockerfile.media` and
keeps data and the model on a named volume.

Configuration is read from the environment once at startup:

| Variable | Default | Meaning |
|---|---|---|
| `MEDIA_DATA_DIR` | `/data` | Root of all stored files |
| `MEDIA_WHISPER_MODEL` | `small.en` | faster-whisper model name |
| `MEDIA_FFMPEG_BIN` | `ffmpeg` | FFmpeg binary |
| `MEDIA_FFPROBE_BIN` | `ffprobe` | ffprobe binary |
| `MEDIA_PORT` | `9201` | Listening port |
| `MEDIA_PUBLIC_URL` | `http://localhost:9201` | Base of the clip URLs in results |

## API

| Route | Purpose |
|---|---|
| `POST /v1/sources` | Multipart upload of a video (`file`) with an optional `cropCenterX` form field in `[0, 1]`. Returns `sourceId`, `sha256`, `durationMs`, `mediaType`, `cropCenterX` and `url`, where `GET /v1/sources/{sourceId}/content` serves the bytes. The same bytes always yield the same `sourceId`. |
| `POST /v1/tasks` | Submit a task: `taskId` (any string of 1 to 128 characters), `type`, `input`, `callbackUrl`, `callbackToken`. A new task is answered `202`; a repeated `taskId` with the same type and input is answered `200` with the original task and is not run again; a repeated `taskId` with different content is answered `409`. |
| `GET /v1/tasks/{taskId}` | `{"taskId", "status"}` with `status` one of `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, plus `result` (an object) only when `SUCCEEDED` and `error` (`{"code", "message"}`) only when `FAILED`. Unknown tasks are `404`. |
| `GET /v1/sources/{sourceId}` | The same reference as the upload returns. Unknown ids are `404`. |
| `GET /v1/sources/{sourceId}/content` | The stored source video with the reference's `mediaType`. Unknown ids are `404`. |
| `GET /v1/clips/{sha256}` | The rendered clip as `video/mp4`. |
| `GET /healthz` | `200` when FFmpeg, ffprobe, the filters, the encoders and the transcription model are usable, otherwise `503` with the problems listed. |

Tasks run one at a time, in submission order, on a single worker thread.

### Callback

When a task finishes, the service sends one `POST` to `callbackUrl` with the
header `X-Emberling-Callback-Token: <callbackToken>` and the body

```json
{"externalTaskId": "<taskId>", "payload": {"status": "SUCCEEDED", "result": {"...": "the task's result object"}}}
```

A failed task carries `{"status": "FAILED", "error": {"code": "...", "message": "..."}}`
as the payload. The `result` and `error` objects are the same ones the status
route returns.
Delivery starts only after the task record is written. A transport error or a
`5xx` answer is retried with exponential backoff (1 s doubling to 30 s) for up
to ten minutes; any other `4xx` is taken as the receiver's final answer. The
task's state never depends on the callback: the status route always has the
result. The token is held in memory only, never persisted or logged, so a task
finished after a restart has no callback and is collected by polling.

### Task types

**`TRANSCRIBE`**, input `{"sourceId"}`; an unknown id fails the task with
`SOURCE_NOT_FOUND`. Runs faster-whisper on CPU with the
language fixed to English and word timestamps on, then splits the words into
sentences at terminal punctuation or a pause of at least 0.8 s and numbers them
`S1`, `S2`, ... The result is the material text, one line per sentence:

```text
S12	83.420	87.110	This approach does not work
S13	87.110	91.260	unless the sample is large enough
```

with `material`, `language`, `sentenceCount`, `lineCount`, `durationMs`,
`sourceId`, `sourceSha256`, `transcriptSha256` (the SHA-256 of the material
text), `model`, and the source's `url` and `mediaType` as the source reference
reports them. Word timestamps stay in the service. A transcript is cached
per source hash and model name.

**`CHECK_TIMELINE`**, input `{"sourceId", "transcriptSha256", "targetDurationMs":
int or null, "segments": [{"from": "S12", "to": "S13"}, ...]}`. An unknown
source or transcript, a transcript that belongs to another source, or a
sentence id that is not of the form `S<n>` fails the task with
`TIMELINE_INPUT_INVALID`; a well-formed id that does not exist is a blocking
finding. Each segment is resolved to the start of its first sentence and the
end of its last sentence, and the timeline is written as canonical JSON: sorted
keys, separators `,` and `:`, non-ASCII kept, no trailing newline, integer
milliseconds. Its SHA-256 is `timelineSha256`:

```json
{"cropCenterX":0.5,"durationMs":58940,"ruleVersion":"v1","segments":[{"endMs":85600,"from":"S1","startMs":26660,"text":"joined sentence text","to":"S5"}],"sourceId":"src_...","sourceSha256":"...","targetDurationMs":null,"transcriptSha256":"..."}
```

A segment that cannot be resolved keeps `null` times and an empty `text`. The
result carries `kind` `TIMELINE`, `ruleVersion`, `passed`, `subjectSha256` and
`timelineSha256` (equal), `durationMs`, `findings`, and `timeline`, the
canonical text verbatim, so a caller can store it and get the same hash. The
timeline records the source's `cropCenterX` as of the check, so a render depends
on nothing outside the timeline; changing a source's crop centre affects only
timelines checked afterwards.

**`RENDER`**, input `{"timelineSha256"}`; an unknown hash fails the task with
`TIMELINE_NOT_FOUND`. Cuts and concatenates the segments,
crops a fixed 9:16 window of full height centred on `cropCenterX`, scales to
1080x1920, burns subtitles of at most seven words timed by the word timestamps,
and encodes with libx264 and AAC using one thread, bit-exact flags and no
timestamp metadata, so the same timeline renders to the same bytes in the same
environment. The clip is stored as `clips/<sha256>.mp4` with its cue data beside
it. The result carries `clipSha256`, `clipUrl`, `sizeBytes`, `mediaType`,
`durationMs` and `timelineSha256`.

**`CHECK_CLIP`**, input `{"clipSha256"}`. Probes the clip, decodes it fully,
runs `blackdetect` and `silencedetect`, and compares the stored cue data with
the clip's timeline. An unknown hash fails the task with `CLIP_NOT_FOUND`. The
result carries `kind` `CLIP`, `ruleVersion`, `passed`, `subjectSha256`,
`timelineSha256` (the timeline the clip was rendered from) and `findings`.

A check that does not pass is still a `SUCCEEDED` task; the verdict is in
`passed` and `findings`. `FAILED` means the task could not be carried out.

### Rules v1

Rules live in `app/rules_v1.py` and every check result names the version. A
finding has a stable `code`, a `level` (`BLOCKING` fails the check, `WARNING`
is reported only), a `message` and, for timeline rules about one segment, a
`segmentIndex`. The service reports findings, never suggestions.

| Subject | Rule | Level | Code |
|---|---|---|---|
| Timeline | Every sentence reference exists | Blocking | `TIMELINE_SENTENCE_UNKNOWN` |
| Timeline | Segments do not overlap | Blocking | `TIMELINE_SEGMENTS_OVERLAP` |
| Timeline | A segment does not end before it starts | Blocking | `TIMELINE_SEGMENT_REVERSED` |
| Timeline | Every cut falls on a sentence boundary | Blocking | `TIMELINE_CUT_OFF_BOUNDARY` |
| Timeline | Total duration within 10% of `targetDurationMs`, or 30 to 60 s without a target | Blocking | `TIMELINE_DURATION_OUT_OF_RANGE` |
| Timeline | Segments keep the order of the source | Warning | `TIMELINE_ORDER_CHANGED` |
| Clip | Fully decodable | Blocking | `CLIP_NOT_DECODABLE` |
| Clip | Has a video and an audio stream | Blocking | `CLIP_MISSING_STREAM` |
| Clip | 1080x1920 with square pixels | Blocking | `CLIP_WRONG_DIMENSIONS` |
| Clip | Duration within 0.5 s of the timeline | Blocking | `CLIP_DURATION_MISMATCH` |
| Clip | Linked to a known timeline | Blocking | `CLIP_TIMELINE_UNKNOWN` |
| Clip | Every chosen sentence has cues, with text from the transcript, inside the sentence | Blocking | `CLIP_SUBTITLE_MISSING`, `CLIP_SUBTITLE_TEXT_MISMATCH`, `CLIP_SUBTITLE_TIMING` |
| Clip | No black run longer than 1 s | Blocking | `CLIP_BLACK_FRAMES` |
| Clip | No silent run longer than 2 s | Warning | `CLIP_SILENCE` |

Because segments are expressed in whole sentences, cuts always land on
boundaries; the rule verifies that equality and `TIMELINE_SEGMENT_REVERSED`
covers the one way a selection cannot be resolved to boundaries at all.

## Storage

Everything lives under `MEDIA_DATA_DIR`:

| Directory | Contents |
|---|---|
| `sources/` | Source bytes and a JSON record per content hash |
| `transcripts/` | Word-level transcript records by transcript hash, plus the per-source cache index |
| `timelines/` | Canonical timeline JSON by timeline hash |
| `clips/` | Rendered clips and their cue records by clip hash |
| `tasks/` | One JSON record per task |
| `tmp/` | Work in progress, swept at startup |

Every write goes to a temporary file first and is renamed into place, so an
interrupted write leaves no partial file. Task records survive a restart; a
task found `RUNNING` at startup is reset to `QUEUED` and run again, which gives
the same result because every task type is deterministic.

## Tests

`make media-test` runs pytest with the real FFmpeg binaries and the `tiny.en`
model, which is downloaded on first run. The fixture under `tests/fixtures/` is
a short synthesized English speech clip with a plain background. The suite
covers duplicate submissions, the transcript cache, a transcript of another
source, unknown and overlapping sentences, durations out of range, clips with
black frames, silence, wrong dimensions or a missing stream, rendering the same
timeline twice, interrupted writes, restart recovery and callback retry.
