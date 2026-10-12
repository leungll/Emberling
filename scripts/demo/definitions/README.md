# Live demo Definitions

The Definitions in this directory run against real models. They are for manual demos
only: `make demo` and CI never load them, and they cost real money every time they run.
The deterministic demos use the fixtures under `backend/test/fixtures/definitions/` and
the mock models instead.

## Photo Effect Template (Live)

`photo_effect_template_live.json` has the same graph as the deterministic photo effect
template: a creative brief and a photo set feed one Agent, whose answer goes through the
media result check to the output. The Agent is driven by a real decision model and its
`generate_image` Tool uses a real image model. For each photo the Agent generates one
stylized image in the style the brief asks for, reviews it with `review_asset`,
regenerates a photo at most once when its review fails, and then finishes with one
example per photo, all made with the same settings.

The Agent node selects:

| Field | Value | Why |
|---|---|---|
| `modelId` | `opus-5.5` | Decision model; must be an `id` in `EMBERLING_MODELS` |
| `imageModelId` | `seedream-5.0-pro` | Image model; must be an `id` in `EMBERLING_IMAGE_MODELS` |
| `timeoutMs` | `1800000` | 30 minutes covers six slow image calls plus the decision turns |
| `maxGenerationCalls` | `6` | Room for three photos with one regeneration each |
| `maxTurns` | `16` | One generation and one review per call, plus the final answer |

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
  {"id":"opus-5.5","model":"anthropic/claude-opus-5.5","displayName":"Opus 5.5"}
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

`GET http://localhost:8080/api/models` should list `opus-5.5`, `seedream-5.0-pro` and
`gpt-image-2.5`.

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
you can follow the Run, its Agent Trace and each generated image. `GET
/api/runs/<run-id>` reports the Run status from the command line.

## Time and cost

With Seedream 5.0 Pro each image takes about one to two minutes, so three photos usually
finish in 5 to 15 minutes, well inside the 30 minute deadline. GPT Image 2.5 is faster.
Every Run spends real money on the provider account: up to six image generations and up
to sixteen decision calls. Set a spending limit on the provider account before running
it.
