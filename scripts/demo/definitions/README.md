# Live demo Definitions

The Definitions in this directory run against real models. They are for manual demos
only: `make demo` and CI never load them, and they cost real money every time they run.
The deterministic demos use the fixtures under `backend/test/fixtures/definitions/` and
the mock models instead.

## Photo Effect Template (Live)

`photo_effect_template_live.json` has the same graph as the deterministic photo effect
template: a creative brief and a photo set feed one Agent, whose answer goes through the
media result check to the output. The Agent is driven by a real decision model, its
`generate_image` Tool uses a real image model and its `review_asset` Tool uses a real
vision model. The Agent works in rounds of whole-set tuning: it writes one settings
group (prompt, aspect ratio and resolution), generates every photo with it and reviews
each image. When any review fails, it revises the settings from the review reasons and
regenerates the whole set, for at most three rounds. It then finishes with the last
round's images, one per photo, together with that round's settings and their digest.

The Agent node selects:

| Field | Value | Why |
|---|---|---|
| `modelId` | `opus-5.5` | Decision model; must be an `id` in `EMBERLING_MODELS` |
| `imageModelId` | `seedream-5.0-pro` | Image model; must be an `id` in `EMBERLING_IMAGE_MODELS` |
| `reviewModelId` | `vision-review` | Review model; must be an `id` in `EMBERLING_MODELS` that declares `visionInput`, and a model other than the image model. The configuration below maps it to `anthropic/claude-sonnet-5.5`, a verified working example |
| `timeoutMs` | `1800000` | 30 minutes covers three rounds of slow image calls and reviews plus the decision turns |
| `maxGenerationCalls` | `10` | Three rounds of three photos, with one call to spare |
| `maxTurns` | `24` | One generation and one review per call, plus the final answer |

The Agent also declares an object-form `outputSchema`: its final answer is an object with
`examples`, one `{photoAssetId, assetRef}` pair per photo from the last round, that
round's `settings` (`prompt`, `aspectRatio` and `resolution`, all required) and the
`settingsDigest` `generate_image` returned for them. The decision model is asked for that object directly through strict
structured output, and the Backend validates it against the Schema before the Agent
succeeds. The media result check then receives the object's canonical JSON text. An
answer of the wrong shape fails the Agent as `INVALID_ACTION` instead of reaching the
media result check, and the rejected answer stays visible in the Agent Trace. The media
result check recomputes the digest of the delivered settings and refuses the delivery
with `SETTINGS_MISMATCH` when it differs from `settingsDigest`, or when any delivered
image was generated with other settings, such as an image kept from an earlier round.
It also refuses the delivery with `STALE_EXAMPLE` when a delivered image did not pass
review while a later image generated for the same photo did pass.
Because the answer must name all three settings fields, the Agent is asked to set all
three in every `generate_image` call, using `"3:4"` when the brief names no shape.

An object-form `outputSchema` must stay inside the strict output subset: every object
lists all of its properties in `required` and sets `additionalProperties` to `false`; the
only keywords are `type`, `properties`, `items`, `required`, `enum`, `description`,
`title` and `additionalProperties`; an optional value is a type union with `"null"`; the
Schema is at most five levels deep, with at most 100 properties in all and 16 KiB. Range
and length keywords such as `minLength`, `maximum` or `pattern` are rejected when the
Definition is saved.

Versions of this Definition saved before it declared an `outputSchema` asked the model to
write the answer as a JSON string that contains the JSON object. Save the current file as
a new version rather than rerunning an older one: the model is no longer asked to encode
its answer, so an older version's instructions contradict the request it receives.

Model IDs are resolved exactly as written. If the Backend has no model registered under
one of these IDs, saving the Definition fails with `MODEL_NOT_FOUND`; no other model is
substituted. To use another registered image model, such as `gpt-image-2.5`, change
`imageModelId` in the file or pick it in Studio's model selector before saving.

## Configure the Backend

Set these in your shell or an untracked `.env` file next to `deploy/compose.yaml`. The
values below are placeholders: use your own OpenRouter key, and never commit it.

```sh
# Required by the Compose stack itself; any local random values work.
export EMBERLING_MODEL_PROVIDER_API_KEY=<local-random-value>
export EMBERLING_CALLBACK_SIGNING_SECRET=<local-random-value>

# Real decision model.
export EMBERLING_MODEL_BASE_URL=https://openrouter.ai/api/v1
export EMBERLING_MODEL_API_KEY=<your-openrouter-api-key>
export EMBERLING_MODEL_OPENROUTER_REQUIRE_PARAMETERS=true
export EMBERLING_MODELS='[
  {"id":"opus-5.5","model":"anthropic/claude-opus-5.5","displayName":"Opus 5.5"},
  {"id":"vision-review","model":"anthropic/claude-sonnet-5.5","displayName":"Vision Review",
   "visionInput":true},
  {"id":"text-review","model":"anthropic/claude-sonnet-5.5","displayName":"Text Review"}
]'

# Real image models for generate_image.
export EMBERLING_IMAGE_BASE_URL=https://openrouter.ai/api/v1
export EMBERLING_IMAGE_API_KEY=<your-openrouter-api-key>
export EMBERLING_IMAGE_MODELS='[
  {"id":"seedream-5.0-pro","model":"bytedance-seed/seedream-5-0-pro","displayName":"Seedream 5.0 Pro",
   "description":"Strong at stylized and artistic image editing and creation. Slower, about 1–2 minutes per image."},
  {"id":"gpt-image-2.5","model":"openai/gpt-image-2.5-sunburst","displayName":"GPT Image 2.5",
   "description":"Preserves source details and subject identity; well suited to precise, instruction-driven edits. Faster, about 20 seconds per image."}
]'
```

The base URL, key and model list of each group must be set together, or the Backend
refuses to start. A single image call is bounded by `EMBERLING_IMAGE_TIMEOUT` (5 minutes
by default). A timed-out or interrupted image call is never resent; the Agent fails
instead.

Start the stack:

```sh
docker compose -f deploy/compose.yaml --profile mock up -d --build
```

`GET http://localhost:8080/api/models` should list `opus-5.5`, `vision-review`,
`text-review`, `seedream-5.0-pro` and `gpt-image-2.5`. The `vision-review` entry above is
a verified working example, not the only choice: any registered model that accepts image
input and differs from the image model works, so no model judges its own output.
`text-review` is the review model of the reviewed video Definition below; it reads
transcript excerpts only, so it declares no `visionInput`.

## Run it

Upload only photos you are allowed to send to the image provider: the Agent sends each
photo's bytes to the upstream model. Two or three photos fit the generation budget.

```sh
# Save the Definition.
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary @scripts/demo/definitions/photo_effect_template_live.json \
  http://localhost:8080/api/definitions)
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")

# Upload the photo set.
photos='[]'
for file in photo1.jpg photo2.jpg photo3.jpg; do
  ref=$(curl -fsS -F "file=@$file;type=image/jpeg" http://localhost:8080/api/assets)
  photos=$(jq -c --argjson ref "$ref" '. + [$ref]' <<<"$photos")
done

# Start the Run.
jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson photos "$photos" \
  '{workflowId: $wf, definitionVersion: $v,
    input: {brief: "A hand-made clay stop-motion look", photos: $photos}}' |
  curl -fsS -X POST -H 'Content-Type: application/json' --data-binary @- \
    http://localhost:8080/api/runs | jq -r '.id'
```

The saved Definition and its Runs also appear in Studio at `http://localhost:5173`, where
you can follow the Run, its Agent Trace and each generated image. The output node
delivers one image with its caption, the first photo's result; the whole set, one image
per photo with its review, is in the Agent Trace and the Agent's final answer. `GET
/api/runs/<run-id>` reports the Run status from the command line.

## Time and cost

With Seedream 5.0 Pro each image takes about one to two minutes, so one round of three
photos takes 5 to 10 minutes with its reviews, and three rounds can approach the 30
minute deadline. GPT Image 2.5 is faster. Every Run spends real money on the provider
account: up to ten image generations, a review for each, and up to twenty-four decision
calls. Set a spending limit on the provider account before running
it.

## Photo Restyle (Live, Task Gateway)

`image_generation_gateway_live.json` is the workflow path to a real image model: a
creative brief goes through a prompt template, and together with one reference photo
into an `image_generation` node, whose image reaches the output with the brief as its
caption. The node selects `gateway-seedream-5.0-pro`, a model served by the task gateway.

The task gateway is a small demo service that wraps a synchronous Images API model as an
asynchronous task, speaking exactly the protocol of the Mock Provider: it accepts the
task with an idempotency key, generates the image in the background, and reports it in a
callback. The same Definition therefore runs against the Mock Provider by selecting
`image-model-v1` instead; only the model ID differs. The gateway keeps everything in
memory, so a gateway restart loses its tasks, and such a task ends when its Attempt
deadline passes. The node's `width` is not sent upstream: the Images API takes no width,
so the generated image has the model's own default size.

Configure the gateway in addition to the image group above. Both services read the same
gateway model list, whose ids must start with `gateway-`:

```sh
export EMBERLING_TASK_GATEWAY_BASE_URL=http://taskgateway:9104
export EMBERLING_TASK_GATEWAY_MODELS='[
  {"id":"gateway-seedream-5.0-pro","model":"bytedance-seed/seedream-5-0-pro","displayName":"Seedream 5.0 Pro (gateway)"}
]'

docker compose -f deploy/compose.yaml --profile mock --profile gateway up -d --build
```

`GET http://localhost:8080/api/models` should list `gateway-seedream-5.0-pro`. Save the
Definition, upload one photo and start a Run:

```sh
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary @scripts/demo/definitions/image_generation_gateway_live.json \
  http://localhost:8080/api/definitions)
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
ref=$(curl -fsS -F "file=@photo.jpg;type=image/jpeg" http://localhost:8080/api/assets)

jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson ref "$ref" \
  '{workflowId: $wf, definitionVersion: $v,
    input: {brief: "A hand-made clay stop-motion look", reference: $ref}}' |
  curl -fsS -X POST -H 'Content-Type: application/json' --data-binary @- \
    http://localhost:8080/api/runs | jq -r '.id'
```

The image NodeRun's Callback Binding names the provider `task-gateway-v1`, and the output
image is an `EXTERNAL` reference to `http://localhost:9104/v1/images/...`, which opens in
the browser until the gateway restarts. Each Run makes one upstream image call.

## Video Edit (Live)

`video_edit_basic_live.json` and `video_edit_reviewed_live.json` cut one recorded talk
into a short vertical clip: a task (the source video, the requirement and the goal
`DELIVER_CLIP`) goes through the media transcript node, which transcribes the source at
the media service and keeps the transcript as a material, into one Agent, whose
delivery report reaches the output. The Agent reads the transcript with `read_material`,
writes its plan into the state, selects sentence ranges, checks the timeline with
`run_checks`, renders the checked timeline with `render_timeline` and checks the clip.
The reviewed variant also reviews each checked timeline with `review_artifact` before
rendering: an independent review model reads the selected sentences with their
neighbours and reports whether the clip satisfies the requirement and whether a selected
sentence, cut off from its context, distorts what the speaker meant.

The Agent node selects:

| Field | Basic | Reviewed | Why |
|---|---|---|---|
| `modelId` | `opus-5.5` | `opus-5.5` | Decision model; must be an `id` in `EMBERLING_MODELS` |
| `reviewModelId` | none | `text-review` | Review model for `review_artifact`; must be an `id` in `EMBERLING_MODELS` with structured output, other than `modelId`, so no model reviews its own selection. The configuration above maps it to `anthropic/claude-sonnet-5.5`; it needs no `visionInput` |
| `allowedTools` | `read_material`, `run_checks`, `render_timeline` | the same plus `review_artifact` | The whole editing loop; nothing else is reachable |
| `maxFailedFacts` | `media_checked: 3` | `media_checked: 3`, `content_reviewed: 1` | Three failed technical checks, or one failed review in the reviewed variant, end the Agent with `REVISION_LIMIT_REACHED` instead of an open-ended revision loop |
| `maxTurns` | `30` | `30` | Up to three reads of a long transcript, a plan, a few check-and-revise rounds, the review, the render, the clip check and the final answer |
| `timeoutMs` | `1800000` | `1800000` | 30 minutes of execution time; the transcript node's own deadline is separate |
| `goalPointer` | `/goal` | `/goal` | The goal is read from the task, so the completion check is the goal-based `DELIVER_CLIP` check |

The transcript node sets an explicit `executionPolicy` (`timeoutMs` 600000, two
Attempts): the Attempt deadline is what ends a transcription the media service lost, so a
Definition must state it rather than rely on a default.

The Agent's `stateSchema` requires the plan (`goal`, `opening`, `mainSegments`) once a
plan is written, and its strict-subset `outputSchema` is the delivery report
`{outcome, clipSha256, selectionReason, cannotSatisfyReason}` with `outcome` either
`DELIVERED` or `CANNOT_SATISFY` and the fields that do not apply set to `null`. The
completion check accepts `CLIP_DELIVERED` only when a clip was rendered, its latest clip
check passed and, in the reviewed variant, the latest review of its timeline passed.

### The shared prompt and its version

Both Definitions embed the same instructions, kept once in `video_edit_prompt.md`; the
reviewed one appends the single paragraph of `video_edit_review_rules.md`. The prompt's
first line is its version, `Prompt version: video-edit/v2.`, so the version a saved
Definition ran with is read back from the Definition's own instructions, and the
evaluation batch records it with every Run. After editing either file, run

```sh
scripts/demo/definitions/embed-video-edit-prompt.sh
```

which rewrites the instructions of the two live Definitions and of the two deterministic
fixtures (`backend/test/fixtures/definitions/video_edit_basic.json` and
`video_edit_reviewed.json`, bound to the mock models), then save the Definitions again: a
saved version keeps the instructions it was saved with. The contract tests fail when an
embedded prompt differs from the files, so the four cannot drift apart unnoticed.

### Run it

The media service must be configured: `EMBERLING_MEDIA_SERVICE_URL` registers the
transcript node and the video Tools, and the compose `media` profile serves it at
`http://media:9201` (the first start downloads the transcription model):

```sh
EMBERLING_MEDIA_SERVICE_URL=http://media:9201 \
  docker compose -f deploy/compose.yaml --profile mock --profile media up -d --build

# Upload the source once; the reference is its content digest.
source=$(curl -fsS -F 'file=@talk.webm;type=video/webm' http://localhost:9201/v1/sources)

created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary @scripts/demo/definitions/video_edit_reviewed_live.json \
  http://localhost:8080/api/definitions)
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")

jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson source "$source" \
  '{workflowId: $wf, definitionVersion: $v,
    input: {task: {sourceId: $source.sourceId, sourceSha256: $source.sha256,
      durationMs: $source.durationMs, goal: "DELIVER_CLIP",
      requirement: "Cut a 45-second vertical clip on why she loves her work."}}}' |
  curl -fsS -X POST -H 'Content-Type: application/json' --data-binary @- \
    http://localhost:8080/api/runs | jq -r '.id'
```

The output node delivers the report; the clip is served by the media service at
`/v1/clips/<clipSha256>`. `make video-demo` runs the same graph with the mock models,
whose decisions are scripted, against the real media service, and `evals/video_editing`
runs the live Definitions over the evaluation dataset.

### Time and cost

A transcription of a five-minute talk takes a minute or two on the first Run of a source
and is cached afterwards. Each decision turn is one model call, so a Run makes up to
thirty decision calls and, in the reviewed variant, one review call per checked
timeline; the media work costs nothing beyond the machine running it.
