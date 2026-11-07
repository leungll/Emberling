package readmaterial

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	runID      = "run_1"
	fiveLines  = "S1\t0.000\t2.500\tHello and welcome.\nS2\t2.500\t6.000\tToday we talk about embers.\nS3\t6.000\t9.000\tThank you.\nS4\t9.000\t9.500\tBye.\nS5\t9.500\t9.900\tEnd.\n"
	secretLine = "token-SECRET-do-not-print"
)

// fakeReader serves text artifacts keyed by the Run that is linked to them, the way the
// Run-scoped artifact read does: an artifact another Run produced is not found.
type fakeReader struct {
	texts map[string]string // runID + "/" + artifactID -> content
	media map[string]string // artifactID -> media type override
	fail  map[string]error  // artifactID -> error the open fails with
}

func (f fakeReader) OpenText(_ context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error) {
	if err, ok := f.fail[artifactID]; ok {
		return domain.Artifact{}, nil, err
	}
	content, ok := f.texts[runID+"/"+artifactID]
	if !ok {
		return domain.Artifact{}, nil, fmt.Errorf("artifact %s: %w", artifactID, domain.ErrNotFound)
	}
	mediaType := domain.ArtifactMediaTypeTextPlain
	if override, ok := f.media[artifactID]; ok {
		mediaType = override
	}
	sum := sha256.Sum256([]byte(content))
	return domain.Artifact{ArtifactID: artifactID, MediaType: mediaType, SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:])},
		io.NopCloser(strings.NewReader(content)), nil
}

func newExecutor(texts map[string]string) Executor {
	return Executor{ports: Ports{Materials: fakeReader{texts: texts}}}
}

func action(arguments string) registry.ToolAction {
	return registry.ToolAction{RunID: runID, AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1",
		ToolName: ToolName, AttemptNo: 1, Arguments: json.RawMessage(arguments)}
}

type decoded struct {
	Status   string `json:"status"`
	Material *struct {
		ArtifactID string `json:"artifactId"`
		SHA256     string `json:"sha256"`
		StartLine  int    `json:"startLine"`
		LineCount  int    `json:"lineCount"`
		TotalLines int    `json:"totalLines"`
	} `json:"material"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// execute runs one call, checks the result against the registered OutputSchema and
// decodes it.
func execute(t *testing.T, executor Executor, arguments string) decoded {
	t.Helper()
	result, err := executor.Execute(context.Background(), action(arguments))
	if err != nil {
		t.Fatalf("Execute(%s) error = %v, want nil", arguments, err)
	}
	if result.Kind != registry.ToolResultCompleted || result.Result == nil {
		t.Fatalf("Execute(%s) = %+v, want a completed result", arguments, result)
	}
	schema, err := registry.CompileSchema(Registration(Ports{}).Metadata.OutputSchema)
	if err != nil {
		t.Fatalf("compile output schema: %v", err)
	}
	var value any
	if err := json.Unmarshal(result.Result.Output, &value); err != nil {
		t.Fatalf("decode %s: %v", result.Result.Output, err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		t.Fatalf("result %s does not satisfy the OutputSchema: %v", result.Result.Output, err)
	}
	var out decoded
	if err := json.Unmarshal(result.Result.Output, &out); err != nil {
		t.Fatalf("decode %s: %v", result.Result.Output, err)
	}
	return out
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestReadMaterial_RangeInside_ReturnsThoseLinesWithTerminators(t *testing.T) {
	executor := newExecutor(map[string]string{runID + "/art_t": fiveLines})
	out := execute(t, executor, `{"artifactId":"art_t","startLine":2,"lineCount":2}`)
	want := "S2\t2.500\t6.000\tToday we talk about embers.\nS3\t6.000\t9.000\tThank you.\n"
	if out.Material == nil || out.Text != want || out.Truncated {
		t.Fatalf("result = %+v, want lines 2..3 untruncated", out)
	}
	m := out.Material
	if m.ArtifactID != "art_t" || m.SHA256 != digestOf(fiveLines) || m.StartLine != 2 || m.LineCount != 2 || m.TotalLines != 5 {
		t.Fatalf("material = %+v, want art_t lines 2..3 of 5 with the artifact digest", m)
	}
}

// The same call twice gives the same bytes, so a retried Attempt reads what the first did.
func TestReadMaterial_SameArguments_SameBytes(t *testing.T) {
	executor := newExecutor(map[string]string{runID + "/art_t": fiveLines})
	first, _ := executor.Execute(context.Background(), action(`{"artifactId":"art_t","startLine":1,"lineCount":5}`))
	second, _ := executor.Execute(context.Background(), action(`{"artifactId":"art_t","startLine":1,"lineCount":5}`))
	if string(first.Result.Output) != string(second.Result.Output) {
		t.Fatalf("outputs differ:\n%s\n%s", first.Result.Output, second.Result.Output)
	}
}

func TestReadMaterial_RangeBeyondEnd_ReturnsWhatExistsUntruncated(t *testing.T) {
	executor := newExecutor(map[string]string{runID + "/art_t": fiveLines})
	out := execute(t, executor, `{"artifactId":"art_t","startLine":4,"lineCount":10}`)
	if out.Material == nil || out.Material.LineCount != 2 || out.Material.TotalLines != 5 || out.Truncated ||
		out.Text != "S4\t9.000\t9.500\tBye.\nS5\t9.500\t9.900\tEnd.\n" {
		t.Fatalf("result = %+v, want the last two lines, not truncated", out)
	}
}

func TestReadMaterial_StartPastEnd_ReturnsNoLinesAndTheTotal(t *testing.T) {
	executor := newExecutor(map[string]string{runID + "/art_t": fiveLines, runID + "/art_empty": ""})
	out := execute(t, executor, `{"artifactId":"art_t","startLine":6,"lineCount":1}`)
	if out.Material == nil || out.Material.StartLine != 6 || out.Material.LineCount != 0 || out.Material.TotalLines != 5 || out.Text != "" || out.Truncated {
		t.Fatalf("result = %+v, want zero lines of 5", out)
	}
	out = execute(t, executor, `{"artifactId":"art_empty","startLine":1,"lineCount":1}`)
	if out.Material == nil || out.Material.LineCount != 0 || out.Material.TotalLines != 0 || out.Text != "" || out.Truncated {
		t.Fatalf("result on an empty artifact = %+v, want zero lines of 0", out)
	}
}

// A last line without a terminator is still a line, returned as it is stored.
func TestReadMaterial_LastLineWithoutNewline_CountedAndReturnedAsStored(t *testing.T) {
	executor := newExecutor(map[string]string{runID + "/art_t": "one\ntwo"})
	out := execute(t, executor, `{"artifactId":"art_t","startLine":1,"lineCount":5}`)
	if out.Material == nil || out.Material.LineCount != 2 || out.Material.TotalLines != 2 || out.Text != "one\ntwo" || out.Truncated {
		t.Fatalf("result = %+v, want both lines", out)
	}
}

func TestReadMaterial_MoreThanMaxLinesRequested_ReturnsTheBoundAndMarksTruncated(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= MaxLines+50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	executor := newExecutor(map[string]string{runID + "/art_long": b.String()})
	out := execute(t, executor, fmt.Sprintf(`{"artifactId":"art_long","startLine":1,"lineCount":%d}`, MaxLines+50))
	if out.Material == nil || out.Material.LineCount != MaxLines || out.Material.TotalLines != MaxLines+50 || !out.Truncated {
		t.Fatalf("result material = %+v truncated=%v, want %d lines of %d, truncated", out.Material, out.Truncated, MaxLines, MaxLines+50)
	}
	if strings.Count(out.Text, "\n") != MaxLines || !strings.HasSuffix(out.Text, fmt.Sprintf("line %d\n", MaxLines)) {
		t.Fatalf("text has %d lines, want exactly the first %d", strings.Count(out.Text, "\n"), MaxLines)
	}
	// Asking for more than the bound is not truncation when the artifact ends first.
	out = execute(t, executor, fmt.Sprintf(`{"artifactId":"art_long","startLine":%d,"lineCount":%d}`, MaxLines+1, MaxLines+50))
	if out.Material == nil || out.Material.LineCount != 50 || out.Truncated {
		t.Fatalf("result = %+v, want the remaining 50 lines, not truncated", out)
	}
}

// The byte bound never splits a line: the line that would cross it is dropped whole, the
// text ends at the previous line terminator and the result is marked truncated.
func TestReadMaterial_ByteBoundMidLine_DropsThePartialLine(t *testing.T) {
	line := strings.Repeat("x", 10<<10-1) + "\n" // 10 KiB per line
	executor := newExecutor(map[string]string{
		runID + "/art_wide": strings.Repeat(line, 5),
		runID + "/art_huge": strings.Repeat("y", MaxBytes+1) + "\nshort\n",
	})
	out := execute(t, executor, `{"artifactId":"art_wide","startLine":1,"lineCount":5}`)
	if out.Material == nil || out.Material.LineCount != 3 || out.Material.TotalLines != 5 || !out.Truncated || len(out.Text) != 3*len(line) || !strings.HasSuffix(out.Text, "\n") {
		t.Fatalf("material = %+v truncated=%v textLen=%d, want three whole lines and truncated", out.Material, out.Truncated, len(out.Text))
	}
	// Lines after the dropped one are not returned either: a range is contiguous.
	if strings.Count(out.Text, "\n") != 3 {
		t.Fatalf("text has %d lines, want 3", strings.Count(out.Text, "\n"))
	}
	out = execute(t, executor, `{"artifactId":"art_huge","startLine":1,"lineCount":2}`)
	if out.Material == nil || out.Material.LineCount != 0 || out.Material.TotalLines != 2 || out.Text != "" || !out.Truncated {
		t.Fatalf("result = %+v, want no lines of 2 and truncated when the first line exceeds the bound", out)
	}
}

func TestReadMaterial_UnknownOtherRunOrBinaryArtifact_NotFoundResult(t *testing.T) {
	reader := fakeReader{
		texts: map[string]string{"run_other/art_theirs": fiveLines, runID + "/art_png": "\x89PNG"},
		media: map[string]string{"art_png": "image/png"},
	}
	executor := Executor{ports: Ports{Materials: reader}}
	for name, artifactID := range map[string]string{"unknown": "art_missing", "other run": "art_theirs", "not text": "art_png"} {
		t.Run(name, func(t *testing.T) {
			out := execute(t, executor, `{"artifactId":"`+artifactID+`","startLine":1,"lineCount":1}`)
			if out.Status != "NOT_FOUND" || out.Material != nil || out.Text != "" {
				t.Fatalf("result = %+v, want {\"status\":\"NOT_FOUND\"} and nothing else", out)
			}
		})
	}
}

func TestReadMaterial_InvalidUTF8_ToolErrorWithoutTheText(t *testing.T) {
	executor := newExecutor(map[string]string{runID + "/art_bad": secretLine + "\n\xff\xfe broken\n"})
	result, err := executor.Execute(context.Background(), action(`{"artifactId":"art_bad","startLine":1,"lineCount":2}`))
	if err == nil || result.Result != nil {
		t.Fatalf("Execute() = %+v, %v; want a Tool error", result, err)
	}
	if strings.Contains(err.Error(), secretLine) || strings.Contains(err.Error(), "broken") {
		t.Fatalf("error carries the material: %v", err)
	}
	if !strings.Contains(err.Error(), "art_bad") {
		t.Fatalf("error = %v, want it to name the artifact", err)
	}
}

func TestReadMaterial_StorageFailure_ToolError(t *testing.T) {
	reader := fakeReader{fail: map[string]error{"art_corrupt": errors.New("artifact content is missing or does not match its metadata")}}
	executor := Executor{ports: Ports{Materials: reader}}
	if _, err := executor.Execute(context.Background(), action(`{"artifactId":"art_corrupt","startLine":1,"lineCount":1}`)); err == nil {
		t.Fatal("Execute() error = nil, want the storage failure as a Tool error")
	}
	if _, err := (Executor{}).Execute(context.Background(), action(`{"artifactId":"art_t","startLine":1,"lineCount":1}`)); err == nil {
		t.Fatal("Execute() without a reader port: error = nil, want a Tool error")
	}
}

func TestReadMaterial_InvalidArguments_ToolError(t *testing.T) {
	executor := newExecutor(map[string]string{runID + "/art_t": fiveLines})
	for name, arguments := range map[string]string{
		"not an object": `[]`, "start zero": `{"artifactId":"art_t","startLine":0,"lineCount":1}`,
		"no lines": `{"artifactId":"art_t","startLine":1,"lineCount":0}`, "empty id": `{"artifactId":"","startLine":1,"lineCount":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := executor.Execute(context.Background(), action(arguments)); err == nil {
				t.Fatalf("Execute(%s) error = nil, want a Tool error", arguments)
			}
		})
	}
}

func TestReadMaterial_RegistersWithMetadata(t *testing.T) {
	registration := Registration(Ports{})
	md := registration.Metadata
	if md.Name != ToolName || md.ExecutionKind != domain.ToolExecutionSync ||
		md.SideEffect.Kind != domain.SideEffectNone || md.SideEffect.Idempotency != domain.IdempotencySafe {
		t.Fatalf("metadata = %+v, want a SYNC, side-effect-free, safe-to-repeat Tool", md)
	}
	if md.MaterialPointer != "/material" {
		t.Fatalf("materialPointer = %q, want /material so the Manifest records the range by declaration", md.MaterialPointer)
	}
	if err := registry.NewToolRegistry().Register(registration); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	schema, err := registry.CompileSchema(md.InputSchema)
	if err != nil {
		t.Fatalf("compile input schema: %v", err)
	}
	for name, arguments := range map[string]string{
		"extra member": `{"artifactId":"a","startLine":1,"lineCount":1,"endLine":2}`,
		"start zero":   `{"artifactId":"a","startLine":0,"lineCount":1}`,
		"missing":      `{"artifactId":"a"}`,
	} {
		var value any
		_ = json.Unmarshal([]byte(arguments), &value)
		if err := registry.ValidateValue(schema, value); err == nil {
			t.Errorf("%s: arguments %s pass the InputSchema, want rejection", name, arguments)
		}
	}
}
