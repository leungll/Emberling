package externaltool

import (
	"context"
	"io"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// The video Tools dispatch to the media task service. They are declared in this package
// because a Tool package may not import another Tool package, and every one of them is an
// external Tool built from a Spec.

// Fact types the video Tools establish. A check records media_checked about the hash it
// checked, with its verdict; a render records clip_rendered about the clip it produced.
const (
	FactTypeMediaChecked = "media_checked"
	FactTypeClipRendered = "clip_rendered"
	// FactTypeContentReviewed is established by the semantic review Tool, declared
	// elsewhere; the video Tools name it only as a fact type that bounds their calls.
	FactTypeContentReviewed = "content_reviewed"
)

// Task types the media service runs for the video Tools.
const (
	TaskTypeCheckTimeline = "CHECK_TIMELINE"
	TaskTypeCheckClip     = "CHECK_CLIP"
	TaskTypeRender        = "RENDER"
)

// Media tasks are polled every few seconds for at most twenty minutes: a render of a
// short clip finishes well within that, and a task the service lost is detected at the
// first poll after the loss.
const (
	mediaPollIntervalMs = 3_000
	mediaPollMaxPolls   = 400
)

// mediaCountedFailedFacts lists the fact types whose failed verdicts bound every video
// Tool: a technical check that did not pass and a semantic review that did not pass each
// count toward their own limit.
func mediaCountedFailedFacts() []string {
	return []string{FactTypeMediaChecked, FactTypeContentReviewed}
}

// sha256HexPattern is the JSON Schema pattern of a lowercase hex SHA-256 digest, which
// is how a timeline and a clip are identified.
const sha256HexPattern = `^[0-9a-f]{64}$`

// ArtifactTextStore is the port a video Tool writes a text Execution Artifact through,
// such as the canonical timeline a check returns. The store returns the content-addressed
// reference the result declares; the Runtime commits the reference's metadata with the
// result, after the content is already in storage.
type ArtifactTextStore interface {
	WriteText(ctx context.Context, r io.Reader) (domain.ArtifactRef, error)
}

// MediaSpecs returns the declarations of every Tool that dispatches to the media service.
// The process entry point registers them over one task client, under the media service's
// Provider ID.
func MediaSpecs(artifacts ArtifactTextStore) []Spec {
	return []Spec{RunChecksSpec(artifacts), RenderTimelineSpec()}
}
