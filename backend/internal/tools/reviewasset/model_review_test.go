package reviewasset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testReviewModel = "vision-model.1"
	testStyle       = "secret-style hand-made clay look"
	photoBytes      = "original-photo-bytes"
	candidateBytes  = "candidate-image-bytes"
)

type fakePhotos struct {
	mediaType string
	content   string
}

func (f fakePhotos) OpenPhoto(context.Context, string) (Image, error) {
	return Image{MediaType: f.mediaType, Content: io.NopCloser(strings.NewReader(f.content))}, nil
}

type fakeArtifacts struct {
	content string
	runID   string
}

func (f *fakeArtifacts) OpenArtifact(_ context.Context, runID, _ string) (Image, error) {
	f.runID = runID
	return Image{MediaType: "image/png", Content: io.NopCloser(strings.NewReader(f.content))}, nil
}

// fakeReviewModel answers every Generate with answer or err and records the requests.
type fakeReviewModel struct {
	capabilities []string
	answer       registry.ModelDecision
	// cost is what the Adapter reports beside the answer; nil reproduces a Provider that
	// reported no charge.
	cost     *domain.Cost
	err      error
	requests []registry.ModelRequest
}

func (m *fakeReviewModel) Models(context.Context) ([]registry.ModelRegistration, error) {
	return nil, nil
}

func (m *fakeReviewModel) Generate(_ context.Context, request registry.ModelRequest) (registry.ModelResponse, error) {
	m.requests = append(m.requests, request)
	if m.err != nil {
		return registry.ModelResponse{}, m.err
	}
	return registry.ModelResponse{Decision: m.answer, Cost: m.cost}, nil
}

func (m *fakeReviewModel) Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	if modelID != testReviewModel {
		return registry.ModelRegistration{}, nil, false
	}
	return registry.ModelRegistration{ModelMetadata: domain.ModelMetadata{ID: modelID, Capabilities: m.capabilities}}, m, true
}

func visionModel(output string) *fakeReviewModel {
	return &fakeReviewModel{
		capabilities: []string{domain.ModelCapabilityVisionInput},
		answer:       registry.ModelDecision{Kind: registry.DecisionFinal, Output: json.RawMessage(output)},
	}
}

func modelExecutor(model *fakeReviewModel, artifacts *fakeArtifacts) Executor {
	if artifacts == nil {
		artifacts = &fakeArtifacts{content: candidateBytes}
	}
	return Executor{ports: Ports{
		Photos:        fakePhotos{mediaType: "image/jpeg", content: photoBytes},
		Artifacts:     artifacts,
		Models:        model,
		MaxImageBytes: 1024,
	}}
}

func modelAction(arguments string) registry.ToolAction {
	a := action(arguments)
	a.RunID = "run_1"
	a.ReviewModelID = testReviewModel
	return a
}

const modelArguments = `{"assetRef":"artifact_1","photoAssetId":"asset_photo_1","styleRequirement":"` + testStyle + `"}`

// assertCleanError proves a failed review names none of the images, their data URLs or
// the style requirement.
func assertCleanError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("execute error = nil, want the review to fail")
	}
	for _, leak := range []string{photoBytes, candidateBytes, "data:image", "base64", testStyle} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q carries %q", err, leak)
		}
	}
}

func TestExecute_ReviewModel_SendsPhotoCandidateAndStyleWithoutTools(t *testing.T) {
	model := visionModel(`{"passed":false,"reasons":["The face is no longer recognisable."]}`)
	artifacts := &fakeArtifacts{content: candidateBytes}
	got, err := modelExecutor(model, artifacts).Execute(context.Background(), modelAction(modelArguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model requests = %d, want exactly one", len(model.requests))
	}
	request := model.requests[0]
	if request.ModelID != testReviewModel || len(request.Tools) != 0 || request.Instructions != reviewInstructions {
		t.Errorf("request model/tools/instructions = %q/%d/%t, want the review model, no Tools and the versioned instructions",
			request.ModelID, len(request.Tools), request.Instructions == reviewInstructions)
	}
	// The instructions must demand deliverable quality, not only a recognisable subject
	// and the style, and that demand belongs to the second policy version.
	if !strings.Contains(request.Instructions, "deformed") || !strings.Contains(request.Instructions, "generation artifacts") {
		t.Errorf("instructions = %q, want the deliverable-quality condition", request.Instructions)
	}
	if VisionPolicyVersion != "vision-review-policy-v2" {
		t.Errorf("policy version = %q, want vision-review-policy-v2 for the quality condition", VisionPolicyVersion)
	}
	if !bytes.Equal(request.FinalOutputSchema, []byte(verdictSchema)) {
		t.Errorf("final output schema = %s, want the verdict schema", request.FinalOutputSchema)
	}
	if len(request.Messages) != 1 || request.Messages[0].Role != "user" || len(request.Messages[0].Content) != 0 {
		t.Fatalf("messages = %+v, want one user message carried as parts", request.Messages)
	}
	parts := request.Messages[0].Parts
	if len(parts) != 5 || parts[1].Image == nil || parts[3].Image == nil {
		t.Fatalf("parts = %+v, want text, photo, text, candidate, style", parts)
	}
	if string(parts[1].Image.Data) != photoBytes || parts[1].Image.MediaType != "image/jpeg" {
		t.Errorf("photo part = %s, want the original photo unchanged", parts[1].Image)
	}
	if string(parts[3].Image.Data) != candidateBytes || parts[3].Image.MediaType != "image/png" {
		t.Errorf("candidate part = %s, want the candidate unchanged", parts[3].Image)
	}
	if !strings.Contains(parts[4].Text, testStyle) {
		t.Errorf("last part = %q, want the style requirement", parts[4].Text)
	}
	if artifacts.runID != "run_1" {
		t.Errorf("candidate read for run %q, want the action's Run", artifacts.runID)
	}

	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Passed || out.PolicyVersion != VisionPolicyVersion || out.ReviewModelID != testReviewModel || out.StyleDigest != StyleDigest(testStyle) {
		t.Errorf("result = %+v, want the model verdict under %s bound to the model and style digest", out, VisionPolicyVersion)
	}
	if len(out.Reasons) != 1 || out.Reasons[0] != "The face is no longer recognisable." {
		t.Errorf("reasons = %v, want the model's reason", out.Reasons)
	}
}

func TestExecute_ReviewModelReasons_BoundedAndTruncated(t *testing.T) {
	long := strings.Repeat("é", 250)
	model := visionModel(`{"passed":true,"reasons":["  ","` + long + `","b","c","d"]}`)
	got, err := modelExecutor(model, nil).Execute(context.Background(), modelAction(modelArguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	schema, err := registry.CompileSchema(json.RawMessage(outputSchema))
	if err != nil {
		t.Fatal(err)
	}
	var value any
	_ = json.Unmarshal(got.Result.Output, &value)
	if err := registry.ValidateValue(schema, value); err != nil {
		t.Fatalf("output %s does not conform to the output schema: %v", got.Result.Output, err)
	}
	var out result
	_ = json.Unmarshal(got.Result.Output, &out)
	if len(out.Reasons) != 3 || out.Reasons[0] != strings.Repeat("é", 200) || out.Reasons[1] != "b" || out.Reasons[2] != "c" {
		t.Errorf("reasons = %q, want three non-blank reasons, the first truncated to 200 runes", out.Reasons)
	}
}

func TestExecute_ReviewModelFailure_FailsTheCall(t *testing.T) {
	cases := map[string]*fakeReviewModel{
		"provider error":              {capabilities: []string{domain.ModelCapabilityVisionInput}, err: errors.New("chat completions returned HTTP 500")},
		"provider timeout":            {capabilities: []string{domain.ModelCapabilityVisionInput}, err: errors.New("send request timed out")},
		"tool call answer":            {capabilities: []string{domain.ModelCapabilityVisionInput}, answer: registry.ModelDecision{Kind: registry.DecisionToolCall}},
		"output off schema":           visionModel(`{"passed":"yes","reasons":[]}`),
		"output with an extra member": visionModel(`{"passed":true,"reasons":[],"score":3}`),
		"text output":                 visionModel(`"looks fine"`),
	}
	for name, model := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := modelExecutor(model, nil).Execute(context.Background(), modelAction(modelArguments))
			assertCleanError(t, err)
			if got.Result != nil {
				t.Errorf("result = %s, want none", got.Result.Output)
			}
		})
	}
}

func TestExecute_ImageOverLimit_DoesNotCallModel(t *testing.T) {
	for name, executor := range map[string]func(*fakeReviewModel) Executor{
		"photo": func(m *fakeReviewModel) Executor {
			e := modelExecutor(m, nil)
			e.ports.Photos = fakePhotos{mediaType: "image/jpeg", content: strings.Repeat("p", 1025)}
			return e
		},
		"candidate": func(m *fakeReviewModel) Executor {
			return modelExecutor(m, &fakeArtifacts{content: strings.Repeat("c", 1025)})
		},
		"unsupported media type": func(m *fakeReviewModel) Executor {
			e := modelExecutor(m, nil)
			e.ports.Photos = fakePhotos{mediaType: "image/gif", content: photoBytes}
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			model := visionModel(`{"passed":true,"reasons":[]}`)
			_, err := executor(model).Execute(context.Background(), modelAction(modelArguments))
			assertCleanError(t, err)
			if len(model.requests) != 0 {
				t.Errorf("model requests = %d, want none", len(model.requests))
			}
		})
	}
}

func TestExecute_ReviewModelUnusable_FailsWithoutFallingBackToMock(t *testing.T) {
	lacking := visionModel(`{"passed":true,"reasons":[]}`)
	lacking.capabilities = []string{domain.ModelCapabilityStructuredDecision}
	_, err := modelExecutor(lacking, nil).Execute(context.Background(), modelAction(modelArguments))
	assertCleanError(t, err)

	unregistered := modelAction(modelArguments)
	unregistered.ReviewModelID = "other-model"
	_, err = modelExecutor(visionModel(`{"passed":true,"reasons":[]}`), nil).Execute(context.Background(), unregistered)
	assertCleanError(t, err)

	_, err = Executor{}.Execute(context.Background(), modelAction(modelArguments))
	assertCleanError(t, err)
}

func TestExecute_MockControlWithReviewModel_Refused(t *testing.T) {
	model := visionModel(`{"passed":false,"reasons":["no"]}`)
	arguments := `{"assetRef":"artifact_1","photoAssetId":"p","styleRequirement":"clay","mock":"pass"}`
	_, err := modelExecutor(model, nil).Execute(context.Background(), modelAction(arguments))
	assertCleanError(t, err)
	if len(model.requests) != 0 {
		t.Errorf("model requests = %d, want none", len(model.requests))
	}
}
