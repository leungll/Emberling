package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// Context Manifest derivation: the pure decision of what one model request carried.
//
// The Manifest is derived from the request content and from where each message came
// from, never from a second read of the Tool's output or the workspace. Which Tool
// produced a message is known from the message itself; whether that Tool is a workspace
// Tool comes from its registered Metadata, resolved by the caller; and the Tool Attempt
// behind a tool message is the committed Attempt the caller resolved. Nothing here reads
// storage.

// Fixed labels of the materials that are not named by a Tool.
const (
	manifestLabelInstructions = "Instructions"
	manifestLabelTaskInput    = "Task input"
	manifestLabelState        = "Agent state"
)

// manifestLabelEllipsis ends a label that was cut to domain.ManifestLabelMaxRunes.
const manifestLabelEllipsis = "…"

// ManifestMessage is one message of a model request together with the facts the caller
// resolved about its source. Content is the exact bytes that enter the request.
type ManifestMessage struct {
	Role    string
	Content json.RawMessage
	// ToolName names the Tool whose result a tool message carries; nil otherwise.
	ToolName *string
	// ToolActionID is the committed Action the tool message answers; nil otherwise.
	ToolActionID *string
	// ToolUsesWorkspace is the Tool's registered declaration that it works on the Agent
	// Run's workspace. Only such a Tool's message can be recorded as a workspace file.
	ToolUsesWorkspace bool
	// ToolMaterialPointer is the Tool's registered declaration of where its result names
	// the material it read by range; empty for a Tool that returns no material. Only such
	// a Tool's message can be recorded as a material range.
	ToolMaterialPointer string
	// AttemptID and AttemptNo identify the Tool Attempt whose result the message carries.
	// AttemptID is empty when the message was returned to the Agent without an Attempt,
	// such as a refusal decided before any Attempt existed.
	AttemptID string
	AttemptNo int
}

// ManifestInput is everything DeriveTurnManifest reads: the request content and the
// identifiers of the records it came from.
type ManifestInput struct {
	AgentRunID string
	NodeRunID  string
	// Instructions is the frozen system prompt exactly as the request carries it.
	Instructions string
	Messages     []ManifestMessage
	// StateVersionID and StateVersion name the Agent State version the request carries;
	// State is its value exactly as the request carries it.
	StateVersionID string
	StateVersion   int
	State          json.RawMessage
}

// workspaceFileMessage is the top-level shape a workspace Tool's result must have for its
// message to be recorded as a workspace file: the path read and the patch sequence it was
// read at. Any other field is ignored here.
type workspaceFileMessage struct {
	Path     *string          `json:"path"`
	PatchSeq *json.RawMessage `json:"patchSeq"`
}

// DeriveTurnManifest lists the materials of one model request: the instructions, every
// user message as task input, every tool message as a tool result or a workspace file,
// the material range a tool message of a material Tool carried, the Agent State, and, as
// omitted items, every Execution Artifact a user or tool message refers to. A task input refers to an artifact when an upstream node, such as the
// transcript node, handed the Agent material by reference rather than inline; the
// reference is listed so a reviewer can see which material the Agent started from
// without its content ever having entered the request. Assistant messages are the
// model's own earlier decisions, not material, and are not listed.
//
// An included item's digest and length are computed over the bytes that enter the
// request. The Manifest never copies content: a label is a fixed word or a path, and the
// only values taken from a message are identifiers and digests.
func DeriveTurnManifest(in ManifestInput) []domain.ManifestItem {
	items := make([]domain.ManifestItem, 0, len(in.Messages)+2)
	items = append(items, includedItem(domain.ManifestInstructions, manifestLabelInstructions,
		in.AgentRunID, "", []byte(in.Instructions)))

	seenArtifacts := map[string]bool{}
	for _, message := range in.Messages {
		switch message.Role {
		case AgentRoleUser:
			items = append(items, includedItem(domain.ManifestTaskInput, manifestLabelTaskInput,
				in.NodeRunID, "", message.Content))
			items = appendArtifactItems(items, message.Content, seenArtifacts)
		case AgentRoleTool:
			items = append(items, toolMessageItem(message))
			if item, ok := materialRangeItem(message); ok {
				// The material's content entered the request, so its artifact is the range
				// item, not an omitted reference as well.
				seenArtifacts[item.Ref] = true
				items = append(items, item)
			}
			items = appendArtifactItems(items, message.Content, seenArtifacts)
		}
	}

	items = append(items, includedItem(domain.ManifestState, manifestLabelState,
		in.StateVersionID, strconv.Itoa(in.StateVersion), in.State))
	return items
}

// toolMessageItem records one tool message. A message of a workspace Tool that names the
// file and the patch sequence it was read at is a workspace file, versioned by that
// sequence; every other tool message is a tool result, versioned by its Attempt number.
func toolMessageItem(message ManifestMessage) domain.ManifestItem {
	if message.ToolUsesWorkspace {
		if path, patchSeq, ok := workspaceFileOf(message.Content); ok {
			return includedItem(domain.ManifestWorkspaceFile, path, path, patchSeq, message.Content)
		}
	}
	toolName := ""
	if message.ToolName != nil {
		toolName = *message.ToolName
	}
	ref, version := message.AttemptID, ""
	if ref != "" {
		version = strconv.Itoa(message.AttemptNo)
	} else if message.ToolActionID != nil {
		ref = *message.ToolActionID
	}
	return includedItem(domain.ManifestToolResult, toolName+" result", ref, version, message.Content)
}

// workspaceFileOf reads the top-level path and patchSeq of a workspace Tool's message.
// patchSeq must be a non-negative integer; anything else means the message does not name
// a workspace file version and is recorded as a plain tool result.
func workspaceFileOf(content json.RawMessage) (path, patchSeq string, ok bool) {
	var shape workspaceFileMessage
	if err := json.Unmarshal(content, &shape); err != nil || shape.Path == nil || shape.PatchSeq == nil {
		return "", "", false
	}
	var seq float64
	if err := json.Unmarshal(*shape.PatchSeq, &seq); err != nil || seq < 0 || seq != math.Trunc(seq) || seq > math.MaxInt32 {
		return "", "", false
	}
	if *shape.Path == "" {
		return "", "", false
	}
	return *shape.Path, strconv.Itoa(int(seq)), true
}

// materialVersionPrefixLen is how many leading hex characters of a material's content
// digest version a MATERIAL_RANGE item: enough to tell artifact versions apart in a
// Trace, while the item's own SHA256 stays the digest of the text itself.
const materialVersionPrefixLen = 12

// materialObject is the shape the declared material pointer must resolve to: the artifact
// read, its content digest and the lines returned. Any other member is ignored here.
type materialObject struct {
	ArtifactID string `json:"artifactId"`
	SHA256     string `json:"sha256"`
	StartLine  int    `json:"startLine"`
	LineCount  int    `json:"lineCount"`
	TotalLines int    `json:"totalLines"`
}

// materialRangeItem records the material a tool message of a material Tool carried: the
// object at the declared pointer names the artifact and the range, and the "text" member
// beside it is the text that entered the request, which the item digests. A message
// without a complete material object, such as a NOT_FOUND result, names no material.
func materialRangeItem(message ManifestMessage) (domain.ManifestItem, bool) {
	if message.ToolMaterialPointer == "" {
		return domain.ManifestItem{}, false
	}
	var doc any
	if err := json.Unmarshal(message.Content, &doc); err != nil {
		return domain.ManifestItem{}, false
	}
	tokens := domain.JSONPointerTokens(message.ToolMaterialPointer)
	materialValue, found := lookupJSONPointer(doc, tokens)
	if !found {
		return domain.ManifestItem{}, false
	}
	parent, _ := lookupJSONPointer(doc, tokens[:len(tokens)-1])
	parentObject, ok := parent.(map[string]any)
	if !ok {
		return domain.ManifestItem{}, false
	}
	text, ok := parentObject["text"].(string)
	if !ok {
		return domain.ManifestItem{}, false
	}
	encoded, err := json.Marshal(materialValue)
	if err != nil {
		return domain.ManifestItem{}, false
	}
	var material materialObject
	if err := json.Unmarshal(encoded, &material); err != nil || material.ArtifactID == "" || material.SHA256 == "" ||
		material.StartLine < 1 || material.LineCount < 0 || material.TotalLines < 0 {
		return domain.ManifestItem{}, false
	}
	item := includedItem(domain.ManifestMaterialRange, material.ArtifactID, material.ArtifactID,
		material.SHA256[:min(len(material.SHA256), materialVersionPrefixLen)], []byte(text))
	item.Range = &domain.ManifestRange{StartLine: material.StartLine, LineCount: material.LineCount, TotalLines: material.TotalLines}
	return item, true
}

// artifactReference is the shape by which a tool message refers to an Execution Artifact:
// the ArtifactRef a Tool result carries, content-addressed by its digest.
type artifactReference struct {
	ArtifactID string `json:"artifactId"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// appendArtifactItems adds one omitted ARTIFACT item for every distinct Execution Artifact
// the message refers to, anywhere in its JSON. The artifact's content never entered the
// request; its digest and size describe the content the reference names, and the Artifact
// ID is the reference the content route takes.
func appendArtifactItems(items []domain.ManifestItem, content json.RawMessage, seen map[string]bool) []domain.ManifestItem {
	var value any
	if err := json.Unmarshal(content, &value); err != nil {
		return items
	}
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if id, ok := v["artifactId"].(string); ok && id != "" && !seen[id] {
				seen[id] = true
				ref := artifactReference{ArtifactID: id}
				if digest, ok := v["sha256"].(string); ok {
					ref.SHA256 = digest
				}
				if size, ok := v["sizeBytes"].(float64); ok && size >= 0 && size == math.Trunc(size) {
					ref.SizeBytes = int64(size)
				}
				reason := domain.ManifestReferenceOnly
				items = append(items, domain.ManifestItem{
					Kind:          domain.ManifestArtifact,
					Label:         truncateManifestLabel(id),
					Ref:           id,
					SHA256:        ref.SHA256,
					Length:        int(ref.SizeBytes),
					Included:      false,
					OmittedReason: &reason,
				})
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(value)
	return items
}

func includedItem(kind domain.ManifestItemKind, label, ref, version string, content []byte) domain.ManifestItem {
	sum := sha256.Sum256(content)
	return domain.ManifestItem{
		Kind:     kind,
		Label:    truncateManifestLabel(label),
		Ref:      ref,
		Version:  version,
		SHA256:   hex.EncodeToString(sum[:]),
		Length:   len(content),
		Included: true,
	}
}

// truncateManifestLabel cuts a label to the Manifest label bound, counted in runes, and
// ends a cut label with an ellipsis so the bound includes the ellipsis itself.
func truncateManifestLabel(label string) string {
	if utf8.RuneCountInString(label) <= domain.ManifestLabelMaxRunes {
		return label
	}
	runes := []rune(label)
	return string(runes[:domain.ManifestLabelMaxRunes-1]) + manifestLabelEllipsis
}
