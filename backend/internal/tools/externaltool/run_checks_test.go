package externaltool_test

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
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

// canonicalTimeline is a timeline as the media service canonicalises it; the Tool keeps it
// byte for byte.
const canonicalTimeline = `{"durationMs":42000,"ruleVersion":"v1","segments":[{"endMs":12000,"from":"S12","startMs":3000,"text":"the method works","to":"S13"}],"sourceId":"src_demo","sourceSha256":"` + demoSourceSha + `","targetDurationMs":null,"transcriptSha256":"` + demoTranscriptSha + `"}`

const (
	demoSourceSha     = "1111111111111111111111111111111111111111111111111111111111111111"
	demoTranscriptSha = "2222222222222222222222222222222222222222222222222222222222222222"
	demoClipSha       = "3333333333333333333333333333333333333333333333333333333333333333"
)

func timelineSha() string {
	sum := sha256.Sum256([]byte(canonicalTimeline))
	return hex.EncodeToString(sum[:])
}

func timelineCheckArguments() json.RawMessage {
	return json.RawMessage(`{"kind":"TIMELINE","timeline":{"sourceId":"src_demo","transcriptSha256":"` + demoTranscriptSha + `","segments":[{"from":"S12","to":"S13"}]}}`)
}

func clipCheckArguments() json.RawMessage {
	return json.RawMessage(`{"kind":"CLIP","clipSha256":"` + demoClipSha + `"}`)
}

// timelineCheckResult is the service's CHECK_TIMELINE result with the canonical timeline
// and its true digest, unless the test overrides members.
func timelineCheckResult(overrides map[string]any) json.RawMessage {
	result := map[string]any{
		"kind": "TIMELINE", "passed": false, "ruleVersion": "v1",
		"subjectSha256": timelineSha(), "timelineSha256": timelineSha(), "durationMs": 42000,
		"timeline": canonicalTimeline,
		"findings": []map[string]any{{"code": "DURATION_OUT_OF_RANGE", "level": "BLOCKING", "message": "42.0 s is outside 30 to 60 s", "segmentIndex": 0}},
	}
	for key, value := range overrides {
		if value == nil {
			delete(result, key)
			continue
		}
		result[key] = value
	}
	encoded, _ := json.Marshal(result)
	return encoded
}

// fakeArtifactTextStore keeps every text it is given and answers with the reference a
// content-addressed store would, so a test can check the content reached storage verbatim.
type fakeArtifactTextStore struct {
	texts []string
	err   error
}

func (f *fakeArtifactTextStore) WriteText(_ context.Context, r io.Reader) (domain.ArtifactRef, error) {
	if f.err != nil {
		return domain.ArtifactRef{}, f.err
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return domain.ArtifactRef{}, err
	}
	f.texts = append(f.texts, string(content))
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	id, err := domain.ArtifactIDForSHA256(digest)
	if err != nil {
		return domain.ArtifactRef{}, err
	}
	return domain.ArtifactRef{ArtifactID: id, MediaType: domain.ArtifactMediaTypeTextPlain, SizeBytes: int64(len(content)), SHA256: digest}, nil
}

func mediaAction(toolName string, arguments json.RawMessage) registry.ToolAction {
	action := echoAction()
	action.ToolName = toolName
	action.Arguments = arguments
	return action
}

// validateAgainst reports whether value passes the schema, as the Registry checks it.
func validateAgainst(t *testing.T, schema json.RawMessage, value json.RawMessage) error {
	t.Helper()
	compiled, err := registry.CompileSchema(schema)
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatalf("decode value: %v", err)
	}
	return registry.ValidateValue(compiled, decoded)
}

func TestRunChecksSpec_DeclaresTheCheckContract(t *testing.T) {
	spec := externaltool.RunChecksSpec(&fakeArtifactTextStore{})
	reg := mustRegistration(t, spec, &fakeTaskClient{})
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	m := reg.Metadata
	if m.Name != externaltool.RunChecksToolName || m.Produces == nil || m.Produces.FactType != externaltool.FactTypeMediaChecked {
		t.Fatalf("metadata = %+v, want run_checks producing media_checked", m)
	}
	if m.Produces.SubjectPointer != (domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/subjectSha256"}) ||
		m.Produces.VerdictPointer == nil || m.Produces.VerdictPointer.Pointer != "/passed" {
		t.Errorf("produces = %+v, want the subject at /subjectSha256 and the verdict at /passed", m.Produces)
	}
	if m.Produces.BindArguments["kind"].Pointer != "/kind" || m.Produces.BindArguments["ruleVersion"].Pointer != "/ruleVersion" {
		t.Errorf("bindings = %+v, want kind and ruleVersion from the result", m.Produces.BindArguments)
	}
	if strings.Join(m.CountedFailedFacts, ",") != "media_checked,content_reviewed" {
		t.Errorf("countedFailedFacts = %v, want media_checked and content_reviewed", m.CountedFailedFacts)
	}
	if m.Poll == nil || m.Poll.IntervalMs*int64(m.Poll.MaxPolls) < 20*60*1000 {
		t.Errorf("poll = %+v, want about twenty minutes of polling", m.Poll)
	}
	if len(m.Requires) != 0 {
		t.Errorf("requires = %+v, want none: a check guards nothing", m.Requires)
	}
}

func TestRunChecksSpec_InputSchema_AcceptsOneKindAndRejectsTheRest(t *testing.T) {
	schema := externaltool.RunChecksSpec(nil).Metadata.InputSchema
	for name, value := range map[string]string{
		"timeline":             string(timelineCheckArguments()),
		"timeline with target": `{"kind":"TIMELINE","timeline":{"sourceId":"src","transcriptSha256":"` + demoTranscriptSha + `","targetDurationMs":45000,"segments":[{"from":"S1","to":"S1"}]}}`,
		"timeline null target": `{"kind":"TIMELINE","timeline":{"sourceId":"src","transcriptSha256":"` + demoTranscriptSha + `","targetDurationMs":null,"segments":[{"from":"S1","to":"S1"}]}}`,
		"clip":                 string(clipCheckArguments()),
	} {
		if err := validateAgainst(t, schema, json.RawMessage(value)); err != nil {
			t.Errorf("%s: %v, want accepted", name, err)
		}
	}
	for name, value := range map[string]string{
		"unknown kind":          `{"kind":"AUDIO","clipSha256":"` + demoClipSha + `"}`,
		"timeline kind w/ clip": `{"kind":"TIMELINE","clipSha256":"` + demoClipSha + `"}`,
		"clip kind w/ timeline": `{"kind":"CLIP","timeline":{"sourceId":"src","transcriptSha256":"` + demoTranscriptSha + `","segments":[{"from":"S1","to":"S1"}]}}`,
		"both subjects":         `{"kind":"CLIP","clipSha256":"` + demoClipSha + `","timeline":{}}`,
		"empty segments":        `{"kind":"TIMELINE","timeline":{"sourceId":"src","transcriptSha256":"` + demoTranscriptSha + `","segments":[]}}`,
		"segment without to":    `{"kind":"TIMELINE","timeline":{"sourceId":"src","transcriptSha256":"` + demoTranscriptSha + `","segments":[{"from":"S1"}]}}`,
		"uppercase clip digest": `{"kind":"CLIP","clipSha256":"` + strings.ToUpper(timelineSha()) + `"}`,
		"extra member":          `{"kind":"CLIP","clipSha256":"` + demoClipSha + `","force":true}`,
		"timeline content":      `{"kind":"TIMELINE","timeline":{"sourceId":"src","transcriptSha256":"` + demoTranscriptSha + `","segments":[{"from":"S1","to":"S1"}],"durationMs":1}}`,
	} {
		if err := validateAgainst(t, schema, json.RawMessage(value)); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}
}

func TestRunChecks_MapInput_SelectsTaskTypeAndSendsTheSubject(t *testing.T) {
	client := &fakeTaskClient{}
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(&fakeArtifactTextStore{}), client))

	if _, err := executor.Execute(context.Background(), mediaAction(externaltool.RunChecksToolName, timelineCheckArguments())); err != nil {
		t.Fatalf("Execute(timeline): %v", err)
	}
	if _, err := executor.Execute(context.Background(), mediaAction(externaltool.RunChecksToolName, clipCheckArguments())); err != nil {
		t.Fatalf("Execute(clip): %v", err)
	}
	if len(client.submissions) != 2 {
		t.Fatalf("submissions = %d, want two", len(client.submissions))
	}
	timeline := client.submissions[0]
	if timeline.taskType != externaltool.TaskTypeCheckTimeline {
		t.Errorf("timeline task type = %s, want %s", timeline.taskType, externaltool.TaskTypeCheckTimeline)
	}
	wantTimelineInput := `{"sourceId":"src_demo","transcriptSha256":"` + demoTranscriptSha + `","targetDurationMs":null,"segments":[{"from":"S12","to":"S13"}]}`
	if string(timeline.input) != wantTimelineInput {
		t.Errorf("timeline input = %s, want %s with a null target duration", timeline.input, wantTimelineInput)
	}
	clip := client.submissions[1]
	if clip.taskType != externaltool.TaskTypeCheckClip || string(clip.input) != `{"clipSha256":"`+demoClipSha+`"}` {
		t.Errorf("clip submission = %s %s, want %s with the clip digest alone", clip.taskType, clip.input, externaltool.TaskTypeCheckClip)
	}
}

func TestRunChecks_MapInput_KindWithoutItsSubject_SendsNothing(t *testing.T) {
	client := &fakeTaskClient{}
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(&fakeArtifactTextStore{}), client))
	for name, arguments := range map[string]string{
		"timeline without timeline": `{"kind":"TIMELINE"}`,
		"clip without digest":       `{"kind":"CLIP"}`,
		"unknown kind":              `{"kind":"AUDIO"}`,
	} {
		if _, err := executor.Execute(context.Background(), mediaAction(externaltool.RunChecksToolName, json.RawMessage(arguments))); err == nil {
			t.Errorf("%s: Execute succeeded, want an error", name)
		}
	}
	if len(client.submissions) != 0 {
		t.Errorf("submissions = %d, want none", len(client.submissions))
	}
}

func TestRunChecks_TimelineResult_KeepsTheCanonicalTimelineVerbatimAsArtifact(t *testing.T) {
	store := &fakeArtifactTextStore{}
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(store), &fakeTaskClient{}))
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": json.RawMessage(timelineCheckResult(nil))})

	result, err := executor.OnCallback(context.Background(), echoState("ext_1"), payload)
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if len(store.texts) != 1 || store.texts[0] != canonicalTimeline {
		t.Fatalf("stored texts = %q, want the canonical timeline byte for byte", store.texts)
	}
	wantID, _ := domain.ArtifactIDForSHA256(timelineSha())
	if len(result.Artifacts) != 1 || result.Artifacts[0].ArtifactID != wantID || result.Artifacts[0].SHA256 != timelineSha() {
		t.Fatalf("artifacts = %+v, want the one timeline artifact %s", result.Artifacts, wantID)
	}
	if err := validateAgainst(t, externaltool.RunChecksSpec(nil).Metadata.OutputSchema, result.Output); err != nil {
		t.Fatalf("output %s violates the output schema: %v", result.Output, err)
	}
	var output map[string]any
	_ = json.Unmarshal(result.Output, &output)
	if output["kind"] != "TIMELINE" || output["passed"] != false || output["ruleVersion"] != "v1" ||
		output["subjectSha256"] != timelineSha() || output["timelineSha256"] != timelineSha() ||
		output["timelineArtifactId"] != wantID || output["durationMs"] != float64(42000) {
		t.Errorf("output = %s, want the verdict, digests, artifact id and duration", result.Output)
	}
	if _, present := output["timeline"]; present {
		t.Errorf("output %s carries the timeline content; it belongs in the artifact only", result.Output)
	}
	findings, _ := output["findings"].([]any)
	if len(findings) != 1 {
		t.Errorf("findings = %v, want the one blocking finding", output["findings"])
	}
}

func TestRunChecks_TimelineResult_DigestMismatch_IsAToolFailureWithoutAnArtifact(t *testing.T) {
	for name, overrides := range map[string]map[string]any{
		"tampered timeline": {"timeline": strings.Replace(canonicalTimeline, "S12", "S11", 1)},
		"other digest":      {"timelineSha256": demoClipSha, "subjectSha256": demoClipSha},
		"subject differs":   {"subjectSha256": demoClipSha},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeArtifactTextStore{}
			executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(store), &fakeTaskClient{}))
			payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": json.RawMessage(timelineCheckResult(overrides))})

			_, err := executor.OnCallback(context.Background(), echoState("ext_1"), payload)
			var failure *registry.ProviderFailure
			if !errors.As(err, &failure) || failure.Err.Code != "TIMELINE_DIGEST_MISMATCH" {
				t.Fatalf("OnCallback error = %v, want a TIMELINE_DIGEST_MISMATCH tool failure", err)
			}
			if len(store.texts) != 0 {
				t.Errorf("stored texts = %q, want none: a mismatching timeline never reaches storage", store.texts)
			}
			if strings.Contains(err.Error(), "S11") || strings.Contains(err.Error(), "segments") {
				t.Errorf("error %q quotes the timeline", err)
			}
		})
	}
}

func TestRunChecks_TimelineResult_WithoutTimeline_IsAToolFailure(t *testing.T) {
	for name, overrides := range map[string]map[string]any{
		"no timeline":        {"timeline": nil},
		"empty timeline":     {"timeline": ""},
		"no timeline digest": {"timelineSha256": nil},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeArtifactTextStore{}
			executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(store), &fakeTaskClient{}))
			payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": json.RawMessage(timelineCheckResult(overrides))})

			_, err := executor.OnCallback(context.Background(), echoState("ext_1"), payload)
			var failure *registry.ProviderFailure
			if !errors.As(err, &failure) || failure.Err.Code != "TIMELINE_RESULT_INCOMPLETE" {
				t.Fatalf("OnCallback error = %v, want a TIMELINE_RESULT_INCOMPLETE tool failure", err)
			}
			if len(store.texts) != 0 {
				t.Errorf("stored texts = %q, want none", store.texts)
			}
		})
	}
}

// The store failing is not evidence about the result: nothing is written and the Action
// keeps waiting for a later delivery, which the Runtime's poll supplies.
func TestRunChecks_TimelineResult_StoreFails_IsNotAToolFailure(t *testing.T) {
	store := &fakeArtifactTextStore{err: errors.New("disk full at /var/emberling/artifacts")}
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(store), &fakeTaskClient{}))
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": json.RawMessage(timelineCheckResult(nil))})

	_, err := executor.OnCallback(context.Background(), echoState("ext_1"), payload)
	var failure *registry.ProviderFailure
	if err == nil || errors.As(err, &failure) {
		t.Fatalf("OnCallback error = %v, want a plain error, not a tool failure", err)
	}
}

func TestRunChecks_TimelineResult_StoreRefusesContent_IsAToolFailure(t *testing.T) {
	store := &fakeArtifactTextStore{err: fmt.Errorf("artifact: %w", domain.ErrArtifactTooLarge)}
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(store), &fakeTaskClient{}))
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": json.RawMessage(timelineCheckResult(nil))})

	_, err := executor.OnCallback(context.Background(), echoState("ext_1"), payload)
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) || failure.Err.Code != "TIMELINE_TOO_LARGE" {
		t.Fatalf("OnCallback error = %v, want a TIMELINE_TOO_LARGE tool failure", err)
	}
}

func TestRunChecks_ClipResult_WritesNoArtifactAndCarriesTheVerdict(t *testing.T) {
	store := &fakeArtifactTextStore{}
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(store), &fakeTaskClient{}))
	payload := `{"status":"SUCCEEDED","result":{"kind":"CLIP","passed":true,"ruleVersion":"v1","subjectSha256":"` + demoClipSha + `","timelineSha256":"` + timelineSha() + `","findings":[{"code":"SILENCE","level":"WARNING","message":"2.4 s of silence"}]}}`

	result, err := executor.OnCallback(context.Background(), echoState("ext_2"), []byte(payload))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if len(store.texts) != 0 || len(result.Artifacts) != 0 {
		t.Errorf("clip check stored %q and declared %+v, want no artifact", store.texts, result.Artifacts)
	}
	if err := validateAgainst(t, externaltool.RunChecksSpec(nil).Metadata.OutputSchema, result.Output); err != nil {
		t.Fatalf("output %s violates the output schema: %v", result.Output, err)
	}
	want := `{"kind":"CLIP","passed":true,"ruleVersion":"v1","subjectSha256":"` + demoClipSha + `","findings":[{"code":"SILENCE","level":"WARNING","message":"2.4 s of silence"}]}`
	if string(result.Output) != want {
		t.Errorf("output = %s, want %s", result.Output, want)
	}
}

// A result missing a required member is passed on unchanged: the OutputSchema rejects it
// and the Attempt fails with its Trace, instead of this Tool inventing a verdict.
func TestRunChecks_ClipResultWithoutVerdict_IsRejectedByTheOutputSchema(t *testing.T) {
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(&fakeArtifactTextStore{}), &fakeTaskClient{}))
	payload := `{"status":"SUCCEEDED","result":{"kind":"CLIP","ruleVersion":"v1","subjectSha256":"` + demoClipSha + `","findings":[]}}`

	result, err := executor.OnCallback(context.Background(), echoState("ext_2"), []byte(payload))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if err := validateAgainst(t, externaltool.RunChecksSpec(nil).Metadata.OutputSchema, result.Output); err == nil {
		t.Fatalf("output %s passed the output schema without a verdict", result.Output)
	}
}

func TestRunChecks_Findings_BoundedToTwentyWithShortMessages(t *testing.T) {
	findings := make([]map[string]any, 0, 25)
	for i := 0; i < 25; i++ {
		findings = append(findings, map[string]any{
			"code": strings.Repeat("C", 100), "level": "WARNING",
			"message": strings.Repeat("é", 600), "segmentIndex": i,
		})
	}
	store := &fakeArtifactTextStore{}
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(store), &fakeTaskClient{}))
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": json.RawMessage(timelineCheckResult(map[string]any{"findings": findings}))})

	result, err := executor.OnCallback(context.Background(), echoState("ext_1"), payload)
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if err := validateAgainst(t, externaltool.RunChecksSpec(nil).Metadata.OutputSchema, result.Output); err != nil {
		t.Fatalf("bounded output violates the output schema: %v", err)
	}
	var output struct {
		Findings []struct {
			Code         string `json:"code"`
			Message      string `json:"message"`
			SegmentIndex int    `json:"segmentIndex"`
		} `json:"findings"`
	}
	_ = json.Unmarshal(result.Output, &output)
	if len(output.Findings) != 20 || output.Findings[19].SegmentIndex != 19 {
		t.Fatalf("findings = %d (last segment %d), want the first 20 in the service's order", len(output.Findings), output.Findings[len(output.Findings)-1].SegmentIndex)
	}
	first := output.Findings[0]
	if len(first.Code) != 64 || len(first.Message) > 512 || !strings.HasSuffix(first.Message, "é") {
		t.Errorf("finding code %d bytes, message %d bytes ending %q: want 64 and at most 512 without a split rune", len(first.Code), len(first.Message), first.Message[len(first.Message)-2:])
	}
}

// A result with no findings member still yields an empty list, so the Agent reads [] and
// the fact extraction sees a complete result.
func TestRunChecks_Result_NullFindings_BecomeAnEmptyList(t *testing.T) {
	executor := pollable(t, mustRegistration(t, externaltool.RunChecksSpec(&fakeArtifactTextStore{}), &fakeTaskClient{}))
	payload := `{"status":"SUCCEEDED","result":{"kind":"CLIP","passed":true,"ruleVersion":"v1","subjectSha256":"` + demoClipSha + `"}}`

	result, err := executor.OnCallback(context.Background(), echoState("ext_2"), []byte(payload))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if !strings.Contains(string(result.Output), `"findings":[]`) {
		t.Errorf("output = %s, want an empty findings list", result.Output)
	}
}
