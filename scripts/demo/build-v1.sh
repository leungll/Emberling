#!/usr/bin/env bash
# Builds the serving template from a finished tuning Run: the settings group the Agent
# delivered with its FINAL become a plain image_input -> prompt_template ->
# image_generation -> media_output workflow (plus the text_input that prompt_template's
# required `text` port and media_output's caption need, the same structure as
# backend/test/fixtures/definitions/serving_template.json), saved as a new Definition
# version through the public API.
#
# Usage: scripts/demo/build-v1.sh RUN_ID [WORKFLOW_ID]
#
#   RUN_ID       a COMPLETED Run whose Agent node delivered a FINAL with `settings`
#                ({prompt, aspectRatio, resolution}), read from the Agent NodeRun's output.
#   WORKFLOW_ID  the serving Workflow to save the new version into (PUT, baseVersion is
#                its current latest). Without it a new Workflow is created (POST).
#
# Environment:
#   EMBERLING_DEMO_V1_IMAGE_MODEL  the image_generation modelId (default image-model-v1,
#                                  the Mock Provider's model; the live demo uses the task
#                                  gateway model, for example gateway-seedream-5.0-pro).
#   EMBERLING_DEMO_V1_NAME         the Definition name (default "Photo Effect Serving
#                                  Template (from <RUN_ID>)").
#   EMBERLING_DEMO_V1_WIDTH        the width used when the settings name no resolution
#                                  (default 1024); the output says so.
#
# Field mapping. The node config takes `modelId` and `width` only, so:
#   prompt      -> prompt_template.template, followed by " {{text}}" for the user request;
#   resolution  -> image_generation.width: 1K=1024, 2K=2048, 4K=4096; any other value
#                  stops the script, nothing is guessed; a missing resolution takes
#                  EMBERLING_DEMO_V1_WIDTH and is reported as such;
#   aspectRatio and every other settings member cannot be expressed in the node config.
#   They are listed in the output under `unmapped`, never dropped silently; the demo
#   says so when it shows the result.
#
# Prints one JSON object on stdout: {workflowId, version, settings, width, unmapped}.
# Writes WORKFLOW_ID and DEFINITION_VERSION to $DEMO_STATE when it is set.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools curl jq

run_id=${1:?usage: build-v1.sh RUN_ID [WORKFLOW_ID]}
workflow_id=${2:-}
[[ "$run_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "run id has unexpected characters: $run_id"
[[ -z "$workflow_id" || "$workflow_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "workflow id has unexpected characters: $workflow_id"
model_id=${EMBERLING_DEMO_V1_IMAGE_MODEL:-image-model-v1}
name=${EMBERLING_DEMO_V1_NAME:-"Photo Effect Serving Template (from $run_id)"}
default_width=${EMBERLING_DEMO_V1_WIDTH:-1024}
[[ "$default_width" =~ ^[1-9][0-9]*$ ]] || die "EMBERLING_DEMO_V1_WIDTH must be a positive integer"

log "reading the FINAL settings of run $run_id"
snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id") || die "run $run_id could not be read"
status=$(jq -r '.run.status' <<<"$snapshot")
[[ "$status" == COMPLETED ]] || die "run $run_id is $status; only a COMPLETED Run delivered a FINAL"
agent_output=$(jq -r '[.nodeRuns[] | select(.nodeType == "agent")] | first | .output.text // empty' <<<"$snapshot")
[[ -n "$agent_output" ]] || die "run $run_id has no Agent NodeRun with a text output"
settings=$(jq -c '.settings // empty' <<<"$agent_output" 2>/dev/null) || die "the Agent FINAL of run $run_id is not a JSON object"
[[ -n "$settings" && "$settings" != null ]] || die "the Agent FINAL of run $run_id has no settings member"

prompt=$(jq -r '.prompt // empty' <<<"$settings")
[[ -n "$prompt" ]] || die "settings.prompt is missing or empty"
resolution=$(jq -r '.resolution // empty' <<<"$settings")
width_source="resolution $resolution"
case "$resolution" in
1K) width=1024 ;;
2K) width=2048 ;;
4K) width=4096 ;;
"")
  width=$default_width
  width_source="default (the settings name no resolution)"
  ;;
*) die "settings.resolution $resolution has no width mapping (known: 1K, 2K, 4K)" ;;
esac
# Every member the node config cannot take, by name. aspectRatio is the expected one.
unmapped=$(jq -c '[to_entries[] | select(.key != "prompt" and .key != "resolution") | .key]' <<<"$settings")

definition=$(jq -n --arg name "$name" --arg prompt "$prompt" --arg model "$model_id" --argjson width "$width" '{
  name: $name,
  description: "Serving template built from the delivered tuning settings",
  nodes: [
    {id: "node_photo", type: "image_input", name: "User Photo", position: {x: 0, y: 200},
     config: {inputKey: "photo", label: "Photo", required: true,
              acceptedMediaTypes: ["image/png", "image/jpeg"], maxSizeBytes: 5242880}},
    {id: "node_request", type: "text_input", name: "User Request", position: {x: 0, y: 0},
     config: {inputKey: "request", label: "Request", required: true}},
    {id: "node_prompt", type: "prompt_template", name: "Effect Prompt", position: {x: 200, y: 0},
     config: {template: ($prompt + " {{text}}")}},
    {id: "node_image", type: "image_generation", name: "Image Generation", position: {x: 400, y: 100},
     config: {modelId: $model, width: $width},
     executionPolicy: {timeoutMs: 60000, maxAttempts: 2, backoff: "FIXED"}},
    {id: "node_output", type: "media_output", name: "Media Output", position: {x: 600, y: 100}, config: {}}
  ],
  edges: [
    {id: "edge_request_prompt", source: "node_request", sourceHandle: "text", target: "node_prompt", targetHandle: "text"},
    {id: "edge_photo_image", source: "node_photo", sourceHandle: "image", target: "node_image", targetHandle: "reference"},
    {id: "edge_prompt_image", source: "node_prompt", sourceHandle: "text", target: "node_image", targetHandle: "prompt"},
    {id: "edge_image_output", source: "node_image", sourceHandle: "image", target: "node_output", targetHandle: "image"},
    {id: "edge_prompt_output", source: "node_prompt", sourceHandle: "text", target: "node_output", targetHandle: "caption"}
  ]}')

if [[ -n "$workflow_id" ]]; then
  base=$(curl -fsS "$BACKEND_URL/api/definitions/$workflow_id" | jq -r '.version') || die "workflow $workflow_id could not be read"
  log "saving a new version of $workflow_id (base version $base)"
  saved=$(jq --argjson base "$base" '. + {baseVersion: $base}' <<<"$definition" |
    curl -fsS -X PUT -H 'Content-Type: application/json' --data-binary @- "$BACKEND_URL/api/definitions/$workflow_id") ||
    die "saving the new version failed; the Backend validates the graph and the model id"
else
  log "creating a new workflow"
  saved=$(curl -fsS -X POST -H 'Content-Type: application/json' --data-binary "$definition" "$BACKEND_URL/api/definitions") ||
    die "creating the workflow failed; the Backend validates the graph and the model id"
  workflow_id=$(jq -r '.workflowId' <<<"$saved")
fi
version=$(jq -r '.version' <<<"$saved")
log "saved $workflow_id version $version (model $model_id, width $width from $width_source)"
if [[ "$(jq 'length' <<<"$unmapped")" -gt 0 ]]; then
  log "settings members the node config cannot take, left out of the template: $(jq -r 'join(", ")' <<<"$unmapped")"
fi

jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson settings "$settings" --argjson width "$width" \
  --argjson unmapped "$unmapped" --arg model "$model_id" --arg source "$width_source" \
  '{workflowId: $wf, version: $v, modelId: $model, settings: $settings, width: $width, widthSource: $source, unmapped: $unmapped}'

if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'WORKFLOW_ID=%s\nDEFINITION_VERSION=%s\n' "$workflow_id" "$version" >>"$DEMO_STATE"
fi
