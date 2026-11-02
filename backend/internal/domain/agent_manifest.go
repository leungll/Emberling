package domain

import "time"

// ManifestItemKind names what kind of material one Context Manifest item records.
type ManifestItemKind string

const (
	// ManifestInstructions is the Agent Run's frozen system instructions.
	ManifestInstructions ManifestItemKind = "INSTRUCTIONS"
	// ManifestTaskInput is a user message: the Agent node's validated input.
	ManifestTaskInput ManifestItemKind = "TASK_INPUT"
	// ManifestState is the Agent State version the request carried.
	ManifestState ManifestItemKind = "STATE"
	// ManifestToolResult is one tool message, the result a Tool Attempt returned.
	ManifestToolResult ManifestItemKind = "TOOL_RESULT"
	// ManifestWorkspaceFile is a tool message of a workspace Tool that names the file it
	// read and the workspace patch it was read at.
	ManifestWorkspaceFile ManifestItemKind = "WORKSPACE_FILE"
	// ManifestArtifact is an Execution Artifact a tool message referred to. Its content
	// never enters a request; the item records how to open it.
	ManifestArtifact ManifestItemKind = "ARTIFACT"
)

func (k ManifestItemKind) IsValid() bool {
	switch k {
	case ManifestInstructions, ManifestTaskInput, ManifestState,
		ManifestToolResult, ManifestWorkspaceFile, ManifestArtifact:
		return true
	}
	return false
}

// ManifestOmission says why an item's content was left out of the model request.
type ManifestOmission string

// ManifestReferenceOnly marks material that reached the model only as a reference: the
// request carried its identifier, and the content is read through that reference.
const ManifestReferenceOnly ManifestOmission = "REFERENCE_ONLY"

// ManifestLabelMaxRunes bounds a Manifest item's human-readable label. A longer label is
// cut to this many runes, the last of them an ellipsis.
const ManifestLabelMaxRunes = 120

// ManifestItem is one entry of a Turn's Context Manifest: a material the model request
// carried or deliberately left out. It holds identifiers, a version, a digest and a
// length, never the material itself and never a credential.
//
// For an included item SHA256 and Length describe exactly the bytes that entered the
// request. For an omitted ARTIFACT they describe the referenced content itself, which is
// content-addressed, so a reviewer can tell which artifact version the reference named.
type ManifestItem struct {
	Kind  ManifestItemKind `json:"kind"`
	Label string           `json:"label"`
	// Ref identifies the source record: the Agent Run for instructions, the Agent NodeRun
	// for task input, the State Version for state, the Tool Attempt for a tool result,
	// the file path for a workspace file, the Artifact ID for an artifact.
	Ref string `json:"ref"`
	// Version is the source's version where it has one: the State Version number, the
	// Attempt number, the workspace patch sequence. Empty where the source is immutable.
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Length   int    `json:"length"`
	Included bool   `json:"included"`
	// OmittedReason is set only when Included is false.
	OmittedReason *ManifestOmission `json:"omittedReason,omitempty"`
}

// AgentTurnManifest is the Context Manifest of one Turn. It is written once, in the
// transaction that wins the Turn claim and builds the model request, and is immutable
// afterwards.
type AgentTurnManifest struct {
	TurnID     string         `json:"turnId"`
	AgentRunID string         `json:"agentRunId"`
	Items      []ManifestItem `json:"items"`
	CreatedAt  time.Time      `json:"createdAt"`
}
