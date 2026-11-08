// Package readmaterial implements the built-in `read_material` Tool: a synchronous,
// side-effect-free read of a range of lines of a text Execution Artifact linked to the
// Agent's Run. Transcripts, timelines, logs and documents are all read the same way; the
// Tool knows lines, not media.
//
// The Tool bounds each read itself, so a result never exceeds what one request may
// carry, and the Runtime records the range it returned in the Turn's Context Manifest by
// the registered material declaration. It owns no retry, state transition or Event.
package readmaterial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name.
const ToolName = "read_material"

// materialPointer names, in a successful result, the object that describes the material
// returned. The Context Manifest reads this declaration, never the Tool name.
const materialPointer = "/material"

// inputSchema is the sole contract for the Tool's arguments. Lines are numbered from one.
const inputSchema = `{
  "type": "object",
  "properties": {
    "artifactId": {"type": "string", "minLength": 1, "description": "The text artifact to read, as a result or task input named it."},
    "startLine": {"type": "integer", "minimum": 1, "description": "First line to return, counted from 1."},
    "lineCount": {"type": "integer", "minimum": 1, "description": "How many lines to return; at most 200 lines and 32 KiB are returned per call."}
  },
  "required": ["artifactId", "startLine", "lineCount"],
  "additionalProperties": false
}`

// outputSchema is the sole contract for the Tool's result: the lines read, or NOT_FOUND
// when the artifact is not a text artifact of this Run.
const outputSchema = `{
  "oneOf": [
    {
      "type": "object",
      "properties": {
        "material": {
          "type": "object",
          "properties": {
            "artifactId": {"type": "string"},
            "sha256": {"type": "string"},
            "startLine": {"type": "integer", "minimum": 1},
            "lineCount": {"type": "integer", "minimum": 0},
            "totalLines": {"type": "integer", "minimum": 0}
          },
          "required": ["artifactId", "sha256", "startLine", "lineCount", "totalLines"],
          "additionalProperties": false
        },
        "text": {"type": "string"},
        "truncated": {"type": "boolean"}
      },
      "required": ["material", "text", "truncated"],
      "additionalProperties": false
    },
    {
      "type": "object",
      "properties": {"status": {"const": "NOT_FOUND"}},
      "required": ["status"],
      "additionalProperties": false
    }
  ]
}`

// Registration returns the `read_material` ToolRegistration over the given ports. The
// Tool reads committed content and changes nothing, so it is always safe to repeat.
func Registration(ports Ports) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:            ToolName,
			Title:           "Read material",
			Description:     "Read a range of lines of a text artifact of this run",
			InputSchema:     json.RawMessage(inputSchema),
			OutputSchema:    json.RawMessage(outputSchema),
			SideEffect:      domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind:   domain.ToolExecutionSync,
			MaterialPointer: materialPointer,
			// The range read is shown; the text itself is material the Context Manifest
			// accounts for and never a run-card member.
			Display: []domain.ToolDisplayField{
				{Source: domain.FactPointerArguments, Pointer: "/artifactId", Title: "Material"},
				{Source: domain.FactPointerArguments, Pointer: "/startLine", Title: "Start line"},
				{Source: domain.FactPointerArguments, Pointer: "/lineCount", Title: "Line count"},
				{Source: domain.FactPointerResult, Pointer: "/material/totalLines", Title: "Total lines"},
				{Source: domain.FactPointerResult, Pointer: "/truncated", Title: "Truncated"},
			},
		},
		Executor: Executor{ports: ports},
	}
}

// Executor implements registry.ToolExecutor for the `read_material` Tool.
type Executor struct {
	ports Ports
}

// arguments is the decoded, schema-validated input of one call.
type arguments struct {
	ArtifactID string `json:"artifactId"`
	StartLine  int    `json:"startLine"`
	LineCount  int    `json:"lineCount"`
}

// material describes what a result returned. Field order is the result's key order.
type material struct {
	ArtifactID string `json:"artifactId"`
	SHA256     string `json:"sha256"`
	StartLine  int    `json:"startLine"`
	LineCount  int    `json:"lineCount"`
	TotalLines int    `json:"totalLines"`
}

// result is a successful read. Text holds the returned lines as stored.
type result struct {
	Material  material `json:"material"`
	Text      string   `json:"text"`
	Truncated bool     `json:"truncated"`
}

// notFound is the result for an artifact the Run cannot read as text: absent, linked to
// another Run, or not a text artifact. It says no more, so the model can correct the
// identifier without learning anything about other Runs' material.
type notFound struct {
	Status string `json:"status"`
}

// Execute reads the requested range. It returns a completed result or an error, never
// dispatches and never retries. Errors name the Tool and the artifact id only; the
// material never enters an error.
func (e Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: arguments are not a JSON object: %w", ToolName, err)
	}
	if args.ArtifactID == "" || args.StartLine < 1 || args.LineCount < 1 {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: artifactId, startLine >= 1 and lineCount >= 1 are required", ToolName)
	}
	if e.ports.Materials == nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: artifact %s: the material reader is not configured in this process", ToolName, args.ArtifactID)
	}

	artifact, content, err := e.ports.Materials.OpenText(ctx, action.RunID, args.ArtifactID)
	if errors.Is(err, domain.ErrNotFound) {
		return completed(notFound{Status: "NOT_FOUND"})
	}
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: artifact %s could not be opened: %w", ToolName, args.ArtifactID, err)
	}
	defer func() { _ = content.Close() }()
	if artifact.MediaType != domain.ArtifactMediaTypeTextPlain {
		return completed(notFound{Status: "NOT_FOUND"})
	}

	lines, err := readLines(content, args.StartLine, args.LineCount)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: artifact %s could not be read: %w", ToolName, args.ArtifactID, err)
	}
	return completed(result{
		Material: material{
			ArtifactID: artifact.ArtifactID, SHA256: artifact.SHA256,
			StartLine: args.StartLine, LineCount: lines.Lines, TotalLines: lines.TotalLines,
		},
		Text:      string(lines.Text),
		Truncated: lines.Truncated,
	})
}

func completed(output any) (registry.ToolExecutionResult, error) {
	encoded, err := json.Marshal(output)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result: %w", ToolName, err)
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: encoded}}, nil
}
