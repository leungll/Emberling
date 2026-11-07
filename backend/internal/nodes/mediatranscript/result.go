package mediatranscript

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// Task statuses the service reports in a terminal payload.
const (
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// Failure codes this node records on the Attempt.
const (
	// defaultFailureCode is used when the service reports FAILED without naming a code,
	// so a persisted failure always carries one.
	defaultFailureCode = "TRANSCRIPT_TASK_FAILED"
	// CodeSourceMismatch fails the Attempt when the result names another source than the
	// task the node dispatched: a different source id or hash, or a duration further from
	// the task's than the tolerance.
	CodeSourceMismatch = "TRANSCRIPT_SOURCE_MISMATCH"
	// CodeMaterialMismatch fails the Attempt when the material does not match the digest
	// the service reported for it, or is empty.
	CodeMaterialMismatch = "TRANSCRIPT_MATERIAL_MISMATCH"
)

// durationToleranceMs is how far the duration the service measured may differ from the
// duration the task declared before the result is treated as belonging to another source:
// one second, which covers container rounding and probe differences but not a different
// cut of the video.
const durationToleranceMs = 1000

// maxFailureTextBytes bounds the service-supplied failure code and message, which reach
// the Attempt's error and Trace.
const maxFailureTextBytes = 512

// terminalPayload is the service's report of a finished task, as a callback payload and
// as the body of a terminal status answer alike.
type terminalPayload struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// transcribeResult is the result object of a SUCCEEDED TRANSCRIBE task. Material is the
// whole transcript, one line per sentence; it is written to artifact storage and never
// copied anywhere else.
type transcribeResult struct {
	SourceID         string `json:"sourceId"`
	SourceSHA256     string `json:"sourceSha256"`
	TranscriptSHA256 string `json:"transcriptSha256"`
	Language         string `json:"language"`
	SentenceCount    int    `json:"sentenceCount"`
	LineCount        int    `json:"lineCount"`
	DurationMs       int64  `json:"durationMs"`
	Model            string `json:"model"`
	Material         string `json:"material"`
}

// Transcript is the `transcript` output port: a reference to the material and its
// structure, with the source it belongs to. The field set and order are fixed; the
// material itself is read through the artifact id.
type Transcript struct {
	ArtifactID    string `json:"artifactId"`
	SHA256        string `json:"sha256"`
	LineCount     int    `json:"lineCount"`
	SentenceCount int    `json:"sentenceCount"`
	Language      string `json:"language"`
	DurationMs    int64  `json:"durationMs"`
	SourceID      string `json:"sourceId"`
	SourceSHA256  string `json:"sourceSha256"`
}

// interpret turns a terminal payload into the node's output, a *registry.ProviderFailure
// or a plain error. It is the one place a finished task is read, for callbacks and polls
// alike, so both produce identical outputs and failures.
func (e *Executor) interpret(ctx context.Context, state registry.NodeAsyncState, body terminalPayload) (registry.NodeOutput, error) {
	taskID := state.ExternalTask.ExternalTaskID
	switch body.Status {
	case statusSucceeded:
		if !hasValue(body.Result) {
			return registry.NodeOutput{}, fmt.Errorf("media_transcript: payload for task %s reports %s without a result", taskID, statusSucceeded)
		}
		var result transcribeResult
		if err := json.Unmarshal(body.Result, &result); err != nil {
			return registry.NodeOutput{}, fmt.Errorf("media_transcript: result of task %s is not a transcript result object", taskID)
		}
		return e.publish(ctx, state, result)
	case statusFailed:
		failure := domain.ExecutionError{Code: defaultFailureCode, Message: "the media service reported the transcript task failed"}
		if body.Error != nil {
			if body.Error.Code != "" {
				failure.Code = truncateUTF8(body.Error.Code, maxFailureTextBytes)
			}
			if body.Error.Message != "" {
				failure.Message = truncateUTF8(body.Error.Message, maxFailureTextBytes)
			}
		}
		return registry.NodeOutput{}, &registry.ProviderFailure{Err: failure}
	default:
		return registry.NodeOutput{}, fmt.Errorf("media_transcript: payload for task %s carries status %q, want %s or %s", taskID, body.Status, statusSucceeded, statusFailed)
	}
}

// publish checks a successful result against the task the Attempt was dispatched with,
// stores the material and builds the output. A result that belongs to another source,
// or whose material does not match its own digest, is definite evidence that the task's
// outcome is unusable, so it fails the Attempt as a reported failure would, under a
// stable code; a storage failure is a plain error that leaves the NodeRun waiting, so a
// later delivery or poll of the same result can store it.
func (e *Executor) publish(ctx context.Context, state registry.NodeAsyncState, result transcribeResult) (registry.NodeOutput, error) {
	task, rawTask, err := readTask(state.Ports)
	if err != nil {
		return registry.NodeOutput{}, err
	}
	if reason := sourceMismatch(task, result); reason != "" {
		return registry.NodeOutput{}, &registry.ProviderFailure{Err: domain.ExecutionError{Code: CodeSourceMismatch, Message: reason}}
	}
	if reason := materialMismatch(result); reason != "" {
		return registry.NodeOutput{}, &registry.ProviderFailure{Err: domain.ExecutionError{Code: CodeMaterialMismatch, Message: reason}}
	}

	ref, err := e.artifacts.WriteText(ctx, strings.NewReader(result.Material))
	if err != nil {
		// The error names no storage location; the material is not quoted.
		return registry.NodeOutput{}, fmt.Errorf("media_transcript: store material of task %s: %w", state.ExternalTask.ExternalTaskID, err)
	}
	if ref.SHA256 != result.TranscriptSHA256 {
		return registry.NodeOutput{}, fmt.Errorf("media_transcript: stored material of task %s has digest %s, want %s", state.ExternalTask.ExternalTaskID, ref.SHA256, result.TranscriptSHA256)
	}

	transcript, err := json.Marshal(Transcript{
		ArtifactID:    ref.ArtifactID,
		SHA256:        ref.SHA256,
		LineCount:     result.LineCount,
		SentenceCount: result.SentenceCount,
		Language:      result.Language,
		DurationMs:    result.DurationMs,
		SourceID:      result.SourceID,
		SourceSHA256:  result.SourceSHA256,
	})
	if err != nil {
		return registry.NodeOutput{}, fmt.Errorf("media_transcript: encode transcript output: %w", err)
	}
	return registry.NodeOutput{
		Ports: map[string]json.RawMessage{
			TranscriptPort: transcript,
			TaskPort:       rawTask,
		},
		Artifacts: []domain.ArtifactRef{ref},
	}, nil
}

// sourceMismatch reports why the result does not belong to the task's source, or "".
// The message names the rule, never the hashes or the material.
func sourceMismatch(task taskObject, result transcribeResult) string {
	switch {
	case result.SourceID != task.SourceID:
		return "the transcript names another source id than the task"
	case result.SourceSHA256 != task.SourceSHA256:
		return "the transcript names another source hash than the task"
	case absDiff(result.DurationMs, *task.DurationMs) > durationToleranceMs:
		return fmt.Sprintf("the transcript's source duration differs from the task's by more than %d ms", durationToleranceMs)
	}
	return ""
}

// materialMismatch reports why the material cannot be trusted as the transcript the
// service describes, or "".
func materialMismatch(result transcribeResult) string {
	switch {
	case result.Material == "":
		return "the transcript material is empty"
	case result.LineCount <= 0 || result.SentenceCount <= 0:
		return "the transcript reports no lines or no sentences"
	case !isSHA256Hex(result.TranscriptSHA256):
		return "the transcript digest is not 64 lowercase hex characters"
	case sha256Hex(result.Material) != result.TranscriptSHA256:
		return "the transcript material does not match its reported digest"
	}
	return ""
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// isSHA256Hex reports whether s is a 64-character lowercase hex digest, the form every
// content hash in this node is compared in.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hasValue reports whether a decoded JSON member is present and not JSON null.
func hasValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

// truncateUTF8 cuts s to at most limit bytes without splitting a multi-byte rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
