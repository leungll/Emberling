package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func strPtr(s string) *string { return &s }

// manifestFixture is a two-round request: the task input, one tool round whose result
// refers to an artifact, and the current state.
func manifestFixture() ManifestInput {
	return ManifestInput{
		AgentRunID:   "ar_1",
		NodeRunID:    "nr_agent",
		Instructions: "Answer with one sentence.",
		Messages: []ManifestMessage{
			{Role: AgentRoleUser, Content: json.RawMessage(`{"question":"what is the answer?"}`)},
			{Role: AgentRoleAssistant, Content: json.RawMessage(`{"kind":"TOOL_CALL","toolName":"lookup"}`),
				ToolName: strPtr("lookup"), ToolActionID: strPtr("act_1")},
			{Role: AgentRoleTool, Content: json.RawMessage(`{"record":"forty-two","report":{"artifactId":"art_log","mediaType":"text/plain","sizeBytes":4096,"sha256":"feed"}}`),
				ToolName: strPtr("lookup"), ToolActionID: strPtr("act_1"), AttemptID: "ta_1", AttemptNo: 1},
		},
		StateVersionID: "asv_0",
		StateVersion:   0,
		State:          json.RawMessage(`{"progress":"started"}`),
	}
}

func itemsOfKind(items []domain.ManifestItem, kind domain.ManifestItemKind) []domain.ManifestItem {
	var out []domain.ManifestItem
	for _, item := range items {
		if item.Kind == kind {
			out = append(out, item)
		}
	}
	return out
}

// TestDeriveTurnManifest_EveryKind_DigestAndLengthMatchRequestBytes asserts, for each
// included kind, that the digest and length are those of exactly the bytes the request
// carries for that material, and that the omitted artifact carries the digest and size of
// the content its reference names.
func TestDeriveTurnManifest_EveryKind_DigestAndLengthMatchRequestBytes(t *testing.T) {
	in := manifestFixture()
	in.Messages = append(in.Messages, ManifestMessage{
		Role: AgentRoleTool, Content: json.RawMessage(`{"path":"go.mod","patchSeq":2,"content":"module demo"}`),
		ToolName: strPtr("read_file"), ToolActionID: strPtr("act_2"), ToolUsesWorkspace: true, AttemptID: "ta_2", AttemptNo: 1,
	})
	items := DeriveTurnManifest(in)

	cases := []struct {
		kind    domain.ManifestItemKind
		content string
	}{
		{domain.ManifestInstructions, in.Instructions},
		{domain.ManifestTaskInput, string(in.Messages[0].Content)},
		{domain.ManifestToolResult, string(in.Messages[2].Content)},
		{domain.ManifestWorkspaceFile, string(in.Messages[3].Content)},
		{domain.ManifestState, string(in.State)},
	}
	for _, tc := range cases {
		got := itemsOfKind(items, tc.kind)
		if len(got) != 1 {
			t.Fatalf("%s: %d items, want 1 (items: %+v)", tc.kind, len(got), items)
		}
		if got[0].SHA256 != digestOf(tc.content) {
			t.Errorf("%s: sha256 = %s, want digest of request bytes %s", tc.kind, got[0].SHA256, digestOf(tc.content))
		}
		if got[0].Length != len(tc.content) {
			t.Errorf("%s: length = %d, want %d", tc.kind, got[0].Length, len(tc.content))
		}
		if !got[0].Included {
			t.Errorf("%s: included = false, want true", tc.kind)
		}
	}
	artifacts := itemsOfKind(items, domain.ManifestArtifact)
	if len(artifacts) != 1 {
		t.Fatalf("ARTIFACT: %d items, want 1", len(artifacts))
	}
	if artifacts[0].SHA256 != "feed" || artifacts[0].Length != 4096 {
		t.Errorf("ARTIFACT: sha256/length = %s/%d, want the referenced content's feed/4096", artifacts[0].SHA256, artifacts[0].Length)
	}
}

// TestDeriveTurnManifest_SourceIdentifiers_NameTheRecordsBehindEachItem checks the
// references and versions: the Agent Run for instructions, the Agent NodeRun for task
// input, the Attempt for a tool result, the State Version for state.
func TestDeriveTurnManifest_SourceIdentifiers_NameTheRecordsBehindEachItem(t *testing.T) {
	items := DeriveTurnManifest(manifestFixture())
	want := map[domain.ManifestItemKind][2]string{
		domain.ManifestInstructions: {"ar_1", ""},
		domain.ManifestTaskInput:    {"nr_agent", ""},
		domain.ManifestToolResult:   {"ta_1", "1"},
		domain.ManifestState:        {"asv_0", "0"},
	}
	for kind, refVersion := range want {
		got := itemsOfKind(items, kind)
		if len(got) != 1 || got[0].Ref != refVersion[0] || got[0].Version != refVersion[1] {
			t.Errorf("%s: ref/version = %+v, want %s/%q", kind, got, refVersion[0], refVersion[1])
		}
	}
	if got := itemsOfKind(items, domain.ManifestToolResult); len(got) == 1 && got[0].Label != "lookup result" {
		t.Errorf("TOOL_RESULT label = %q, want %q", got[0].Label, "lookup result")
	}
	// Assistant messages are the model's own decisions, not material.
	for _, item := range items {
		if strings.Contains(item.Label, "TOOL_CALL") || item.Ref == "act_1" {
			t.Errorf("assistant message recorded as material: %+v", item)
		}
	}
}

// TestDeriveTurnManifest_ArtifactReference_OmittedWithReasonAndEntry: the artifact a tool
// message refers to is an omitted item whose reason is REFERENCE_ONLY and whose ref is
// the Artifact ID the content route takes; every included item has no omitted reason.
func TestDeriveTurnManifest_ArtifactReference_OmittedWithReasonAndEntry(t *testing.T) {
	items := DeriveTurnManifest(manifestFixture())

	artifacts := itemsOfKind(items, domain.ManifestArtifact)
	if len(artifacts) != 1 {
		t.Fatalf("ARTIFACT items = %d, want 1 (items: %+v)", len(artifacts), items)
	}
	art := artifacts[0]
	if art.Included {
		t.Errorf("artifact included = true, want false")
	}
	if art.OmittedReason == nil || *art.OmittedReason != domain.ManifestReferenceOnly {
		t.Errorf("artifact omittedReason = %v, want REFERENCE_ONLY", art.OmittedReason)
	}
	if art.Ref != "art_log" || art.Label != "art_log" {
		t.Errorf("artifact ref/label = %s/%s, want art_log", art.Ref, art.Label)
	}
	for _, item := range items {
		if item.Included && item.OmittedReason != nil {
			t.Errorf("included item %s carries omittedReason %q", item.Kind, *item.OmittedReason)
		}
	}
}

// TestDeriveTurnManifest_SameArtifactTwice_ListedOnce: a reference repeated within the
// request is one omitted item.
func TestDeriveTurnManifest_SameArtifactTwice_ListedOnce(t *testing.T) {
	in := manifestFixture()
	in.Messages = append(in.Messages, ManifestMessage{
		Role: AgentRoleTool, Content: json.RawMessage(`{"items":[{"artifactId":"art_log","sha256":"feed","sizeBytes":4096},{"artifactId":"art_other","sha256":"beef","sizeBytes":7}]}`),
		ToolName: strPtr("lookup"), ToolActionID: strPtr("act_3"), AttemptID: "ta_3", AttemptNo: 1,
	})
	artifacts := itemsOfKind(DeriveTurnManifest(in), domain.ManifestArtifact)
	if len(artifacts) != 2 || artifacts[0].Ref != "art_log" || artifacts[1].Ref != "art_other" {
		t.Fatalf("ARTIFACT items = %+v, want art_log then art_other, each once", artifacts)
	}
}

// TestDeriveTurnManifest_SensitiveContent_NeverCopiedIntoManifest: the instructions, the
// task input, a tool result and the state all carry marker strings; none of them may
// appear anywhere in the encoded Manifest.
func TestDeriveTurnManifest_SensitiveContent_NeverCopiedIntoManifest(t *testing.T) {
	const (
		instructionSecret = "operator-token-9f3a-SECRET"
		inputSecret       = "customer-ssn-123-45-6789"
		toolSecret        = "Authorization: Bearer tool-secret-key"
		stateSecret       = "state-secret-value"
		fileBody          = "const apiKey = 'live_sk_file_secret'"
	)
	in := ManifestInput{
		AgentRunID:   "ar_1",
		NodeRunID:    "nr_agent",
		Instructions: "Use the token " + instructionSecret + " when calling the service.",
		Messages: []ManifestMessage{
			{Role: AgentRoleUser, Content: json.RawMessage(`{"brief":"` + inputSecret + `"}`)},
			{Role: AgentRoleTool, Content: json.RawMessage(`{"header":"` + toolSecret + `","artifactId":"art_1","sha256":"ab","sizeBytes":1}`),
				ToolName: strPtr("lookup"), ToolActionID: strPtr("act_1"), AttemptID: "ta_1", AttemptNo: 1},
			{Role: AgentRoleTool, Content: json.RawMessage(`{"path":"config.go","patchSeq":1,"content":"` + fileBody + `"}`),
				ToolName: strPtr("read_file"), ToolActionID: strPtr("act_2"), ToolUsesWorkspace: true, AttemptID: "ta_2", AttemptNo: 1},
		},
		StateVersionID: "asv_1", StateVersion: 1,
		State: json.RawMessage(`{"note":"` + stateSecret + `"}`),
	}
	encoded, err := json.Marshal(DeriveTurnManifest(in))
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	for _, secret := range []string{instructionSecret, inputSecret, "Bearer", "tool-secret-key", stateSecret, "live_sk_file_secret", "apiKey"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("manifest JSON carries request content %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"config.go"`) {
		t.Errorf("manifest JSON lacks the workspace file path, which is the reference it is allowed to carry: %s", encoded)
	}
}

// TestDeriveTurnManifest_LongLabel_CutToBoundWithEllipsis: a label longer than the bound is
// cut to exactly the bound, counted in runes, and ends with an ellipsis.
func TestDeriveTurnManifest_LongLabel_CutToBoundWithEllipsis(t *testing.T) {
	longPath := "src/" + strings.Repeat("très-long-directory/", 12) + "file.go"
	if utf8.RuneCountInString(longPath) <= domain.ManifestLabelMaxRunes {
		t.Fatalf("fixture path is not longer than the bound")
	}
	in := manifestFixture()
	in.Messages = []ManifestMessage{{
		Role: AgentRoleTool, Content: json.RawMessage(`{"path":"` + longPath + `","patchSeq":0}`),
		ToolName: strPtr("read_file"), ToolActionID: strPtr("act_1"), ToolUsesWorkspace: true, AttemptID: "ta_1", AttemptNo: 1,
	}}
	files := itemsOfKind(DeriveTurnManifest(in), domain.ManifestWorkspaceFile)
	if len(files) != 1 {
		t.Fatalf("WORKSPACE_FILE items = %d, want 1", len(files))
	}
	label := files[0].Label
	if got := utf8.RuneCountInString(label); got != domain.ManifestLabelMaxRunes {
		t.Errorf("label runes = %d, want exactly %d", got, domain.ManifestLabelMaxRunes)
	}
	if !strings.HasSuffix(label, "…") {
		t.Errorf("label %q does not end with an ellipsis", label)
	}
	if !strings.HasPrefix(longPath, strings.TrimSuffix(label, "…")) {
		t.Errorf("label %q is not a prefix of the path", label)
	}
	// The reference keeps the full path: only the label is cut.
	if files[0].Ref != longPath {
		t.Errorf("ref = %q, want the full path", files[0].Ref)
	}
}

// TestDeriveTurnManifest_WorkspaceToolMessage_FileWhenPathAndPatchSeqPresent: a workspace
// Tool's message with top-level path and patchSeq is a WORKSPACE_FILE versioned by the
// patch sequence; the same Tool's message without them, and any non-workspace Tool's
// message with them, stay TOOL_RESULT.
func TestDeriveTurnManifest_WorkspaceToolMessage_FileWhenPathAndPatchSeqPresent(t *testing.T) {
	in := manifestFixture()
	in.Messages = []ManifestMessage{
		{Role: AgentRoleTool, Content: json.RawMessage(`{"path":"go.mod","patchSeq":3}`),
			ToolName: strPtr("read_file"), ToolActionID: strPtr("act_1"), ToolUsesWorkspace: true, AttemptID: "ta_1", AttemptNo: 1},
		{Role: AgentRoleTool, Content: json.RawMessage(`{"entries":["go.mod"]}`),
			ToolName: strPtr("list_files"), ToolActionID: strPtr("act_2"), ToolUsesWorkspace: true, AttemptID: "ta_2", AttemptNo: 2},
		{Role: AgentRoleTool, Content: json.RawMessage(`{"path":"go.mod","patchSeq":3}`),
			ToolName: strPtr("lookup"), ToolActionID: strPtr("act_3"), AttemptID: "ta_3", AttemptNo: 1},
		{Role: AgentRoleTool, Content: json.RawMessage(`{"path":"go.mod","patchSeq":"3"}`),
			ToolName: strPtr("read_file"), ToolActionID: strPtr("act_4"), ToolUsesWorkspace: true, AttemptID: "ta_4", AttemptNo: 1},
	}
	items := DeriveTurnManifest(in)
	// Items 1.. are the four tool messages in order (0 is the instructions).
	tools := items[1:5]
	if tools[0].Kind != domain.ManifestWorkspaceFile || tools[0].Ref != "go.mod" || tools[0].Version != "3" || tools[0].Label != "go.mod" {
		t.Errorf("workspace Tool with path and patchSeq: %+v, want WORKSPACE_FILE go.mod@3", tools[0])
	}
	if tools[1].Kind != domain.ManifestToolResult || tools[1].Ref != "ta_2" || tools[1].Version != "2" {
		t.Errorf("workspace Tool without path/patchSeq: %+v, want TOOL_RESULT of ta_2", tools[1])
	}
	if tools[2].Kind != domain.ManifestToolResult || tools[2].Ref != "ta_3" {
		t.Errorf("non-workspace Tool with path/patchSeq: %+v, want TOOL_RESULT", tools[2])
	}
	if tools[3].Kind != domain.ManifestToolResult {
		t.Errorf("workspace Tool with a non-numeric patchSeq: %+v, want TOOL_RESULT", tools[3])
	}
}

// TestDeriveTurnManifest_ToolMessageWithoutAttempt_RefersToAction: a refusal returned to
// the Agent before any Attempt existed is still a tool result; it refers to the Action.
func TestDeriveTurnManifest_ToolMessageWithoutAttempt_RefersToAction(t *testing.T) {
	in := manifestFixture()
	in.Messages = []ManifestMessage{{
		Role: AgentRoleTool, Content: json.RawMessage(`{"status":"SCOPE_DENIED"}`),
		ToolName: strPtr("write_file"), ToolActionID: strPtr("act_9"),
	}}
	items := DeriveTurnManifest(in)
	got := items[1]
	if got.Kind != domain.ManifestToolResult || got.Ref != "act_9" || got.Version != "" {
		t.Errorf("tool message without Attempt: %+v, want TOOL_RESULT ref act_9 and no version", got)
	}
}

// TestDeriveTurnManifest_TaskInputReferencesArtifact_ListedAsOmittedReference: a task
// input that hands the Agent material by reference, as the transcript node does, yields
// an omitted ARTIFACT item beside the included TASK_INPUT, carrying the declared digest
// and size and nothing of the input's other members.
func TestDeriveTurnManifest_TaskInputReferencesArtifact_ListedAsOmittedReference(t *testing.T) {
	in := ManifestInput{
		AgentRunID: "ar_1", NodeRunID: "nr_agent", Instructions: "Cut a clip.",
		Messages: []ManifestMessage{{Role: AgentRoleUser, Content: json.RawMessage(
			`{"transcript":{"artifactId":"art_material","sha256":"abcd","sizeBytes":1234,"lineCount":3},"task":{"requirement":"a short clip"}}`)}},
		StateVersionID: "asv_0", State: json.RawMessage(`{}`),
	}
	items := DeriveTurnManifest(in)

	var artifacts []domain.ManifestItem
	for _, item := range items {
		if item.Kind == domain.ManifestArtifact {
			artifacts = append(artifacts, item)
		}
	}
	if len(artifacts) != 1 {
		t.Fatalf("ARTIFACT items = %+v, want exactly one", artifacts)
	}
	got := artifacts[0]
	if got.Ref != "art_material" || got.SHA256 != "abcd" || got.Length != 1234 || got.Included ||
		got.OmittedReason == nil || *got.OmittedReason != domain.ManifestReferenceOnly {
		t.Fatalf("ARTIFACT item = %+v, want an omitted reference to art_material", got)
	}
	for _, item := range items {
		encoded, _ := json.Marshal(item)
		if strings.Contains(string(encoded), "a short clip") {
			t.Fatalf("manifest item copies task input content: %s", encoded)
		}
	}
}
