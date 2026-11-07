#!/usr/bin/env bash
# Embeds the shared video-editing prompt into the four video-edit Definitions: the two
# live Definitions in this directory and the two deterministic fixtures under
# backend/test/fixtures/definitions/. The prompt lives once, in video_edit_prompt.md; the
# reviewed variants append the one paragraph of video_edit_review_rules.md. Run it after
# editing either file, then save the Definitions again: a saved version keeps the
# instructions it was saved with. The contract tests fail when a Definition's embedded
# instructions differ from these files.
#
# Usage: scripts/demo/definitions/embed-video-edit-prompt.sh
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../../.." && pwd)
prompt_file="$here/video_edit_prompt.md"
review_file="$here/video_edit_review_rules.md"
fixtures="$root/backend/test/fixtures/definitions"

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }

embed() {
  local file=$1 reviewed=$2 tmp
  tmp=$(mktemp)
  jq --indent 2 --rawfile prompt "$prompt_file" --rawfile review "$review_file" --argjson reviewed "$reviewed" '
    ($prompt | rtrimstr("\n")) as $base
    | (if $reviewed then $base + "\n\n" + ($review | rtrimstr("\n")) else $base end) as $instructions
    | .nodes |= map(if .id == "node_agent" then .config.instructions = $instructions else . end)' "$file" >"$tmp"
  mv "$tmp" "$file"
}

embed "$here/video_edit_basic_live.json" false
embed "$here/video_edit_reviewed_live.json" true
embed "$fixtures/video_edit_basic.json" false
embed "$fixtures/video_edit_reviewed.json" true
