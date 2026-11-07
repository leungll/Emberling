package externaltool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

func renderArguments() json.RawMessage {
	return json.RawMessage(`{"timelineSha256":"` + timelineSha() + `"}`)
}

func renderResultPayload() string {
	return `{"status":"SUCCEEDED","result":{"clipSha256":"` + demoClipSha + `","clipUrl":"http://media.internal/clips/` + demoClipSha + `.mp4",` +
		`"sizeBytes":1048576,"mediaType":"video/mp4","durationMs":42000,"timelineSha256":"` + timelineSha() + `","renderer":"ffmpeg"}}`
}

func TestRenderTimelineSpec_RequiresAPassedCheckOfTheNamedTimeline(t *testing.T) {
	reg := mustRegistration(t, externaltool.RenderTimelineSpec(), &fakeTaskClient{})
	tools := registry.NewToolRegistry()
	if err := tools.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	m := reg.Metadata
	if m.Name != externaltool.RenderTimelineToolName || len(m.Requires) != 1 {
		t.Fatalf("metadata = %+v, want render_timeline with one requirement", m)
	}
	req := m.Requires[0]
	if req.FactType != externaltool.FactTypeMediaChecked || req.SubjectArgument != "/timelineSha256" || req.RequireVerdict == nil || !*req.RequireVerdict {
		t.Errorf("requirement = %+v, want media_checked about /timelineSha256 with verdict true", req)
	}
	if !m.ReturnUnmetRequirement {
		t.Error("returnUnmetRequirement = false, want the unmet check handed back to the Agent")
	}
	if m.Produces == nil || m.Produces.FactType != externaltool.FactTypeClipRendered ||
		m.Produces.SubjectPointer != (domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/clipSha256"}) ||
		m.Produces.BindArguments["timelineSha256"] != (domain.FactPointer{Source: domain.FactPointerArguments, Pointer: "/timelineSha256"}) ||
		m.Produces.VerdictPointer != nil {
		t.Errorf("produces = %+v, want clip_rendered about /clipSha256 binding the argument timelineSha256 and no verdict", m.Produces)
	}
	if strings.Join(m.CountedFailedFacts, ",") != "media_checked,content_reviewed" {
		t.Errorf("countedFailedFacts = %v, want media_checked and content_reviewed", m.CountedFailedFacts)
	}
	// Registered beside its producer, the requirement resolves.
	if err := tools.Register(mustRegistration(t, externaltool.RunChecksSpec(&fakeArtifactTextStore{}), &fakeTaskClient{})); err != nil {
		t.Fatalf("Register run_checks: %v", err)
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Errorf("ValidateFactDeclarations: %v", err)
	}
}

func TestRenderTimelineSpec_InputSchema_AcceptsOnlyATimelineDigest(t *testing.T) {
	schema := externaltool.RenderTimelineSpec().Metadata.InputSchema
	if err := validateAgainst(t, schema, renderArguments()); err != nil {
		t.Errorf("digest alone: %v, want accepted", err)
	}
	for name, value := range map[string]string{
		"no digest":        `{}`,
		"malformed digest": `{"timelineSha256":"abc"}`,
		"uppercase digest": `{"timelineSha256":"` + strings.ToUpper(timelineSha()) + `"}`,
		"timeline content": `{"timelineSha256":"` + timelineSha() + `","timeline":{}}`,
		"clip digest":      `{"clipSha256":"` + demoClipSha + `"}`,
	} {
		if err := validateAgainst(t, schema, json.RawMessage(value)); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}
}

func TestRenderTimeline_MapInput_SendsTheDigestAlone(t *testing.T) {
	client := &fakeTaskClient{}
	executor := pollable(t, mustRegistration(t, externaltool.RenderTimelineSpec(), client))

	if _, err := executor.Execute(context.Background(), mediaAction(externaltool.RenderTimelineToolName, renderArguments())); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(client.submissions) != 1 || client.submissions[0].taskType != externaltool.TaskTypeRender ||
		string(client.submissions[0].input) != string(renderArguments()) {
		t.Fatalf("submissions = %+v, want one RENDER task with the digest as input", client.submissions)
	}
	if _, err := executor.Execute(context.Background(), mediaAction(externaltool.RenderTimelineToolName, json.RawMessage(`{}`))); err == nil || len(client.submissions) != 1 {
		t.Errorf("Execute without a digest = %v with %d submissions, want an error and nothing sent", err, len(client.submissions))
	}
}

func TestRenderTimeline_MapResult_KeepsTheDeclaredClipMembersOnly(t *testing.T) {
	executor := pollable(t, mustRegistration(t, externaltool.RenderTimelineSpec(), &fakeTaskClient{}))

	result, err := executor.OnCallback(context.Background(), echoState("ext_3"), []byte(renderResultPayload()))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if err := validateAgainst(t, externaltool.RenderTimelineSpec().Metadata.OutputSchema, result.Output); err != nil {
		t.Fatalf("output %s violates the output schema: %v", result.Output, err)
	}
	want := `{"clipSha256":"` + demoClipSha + `","clipUrl":"http://media.internal/clips/` + demoClipSha + `.mp4","sizeBytes":1048576,"mediaType":"video/mp4","durationMs":42000,"timelineSha256":"` + timelineSha() + `"}`
	if string(result.Output) != want {
		t.Errorf("output = %s, want %s", result.Output, want)
	}
	if len(result.Artifacts) != 0 {
		t.Errorf("artifacts = %+v, want none: the clip stays with the service", result.Artifacts)
	}
}

func TestRenderTimeline_MapResult_IncompleteResult_IsRejectedByTheOutputSchema(t *testing.T) {
	executor := pollable(t, mustRegistration(t, externaltool.RenderTimelineSpec(), &fakeTaskClient{}))
	payload := `{"status":"SUCCEEDED","result":{"clipSha256":"` + demoClipSha + `","clipUrl":"http://media.internal/c.mp4","mediaType":"video/mp4","timelineSha256":"` + timelineSha() + `"}}`

	result, err := executor.OnCallback(context.Background(), echoState("ext_3"), []byte(payload))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if err := validateAgainst(t, externaltool.RenderTimelineSpec().Metadata.OutputSchema, result.Output); err == nil {
		t.Fatalf("output %s passed the output schema without size and duration", result.Output)
	}
}
