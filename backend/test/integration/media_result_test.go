//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaoutput"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaresult"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// mediaResultDefinition stands a Run input text in for the Agent's FINAL, so the delivery
// checks run through the real claim path without driving a model:
//
//	input ─▶ brief.brief ─▶ result.brief ─┐
//	final ─────────────────▶ result.text ─┴▶ output.image / output.caption
func mediaResultDefinition(workflowID string) domain.Definition {
	return domain.Definition{
		WorkflowID: workflowID,
		Version:    1,
		Name:       "Media Result Delivery",
		Nodes: []domain.Node{
			{ID: "input", Type: "text_input", Name: "Caption",
				Config: json.RawMessage(`{"inputKey":"caption","required":true}`)},
			{ID: "final", Type: "text_input", Name: "Final",
				Config: json.RawMessage(`{"inputKey":"final","required":true}`)},
			{ID: "brief", Type: "media_brief", Name: "Brief", Config: json.RawMessage(`{"inputKey":"photos"}`)},
			{ID: "result", Type: "media_result", Name: "Result", Config: json.RawMessage(`{}`)},
			{ID: "output", Type: "media_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "input", SourceHandle: "text", Target: "brief", TargetHandle: "brief"},
			{ID: "e2", Source: "brief", SourceHandle: "text", Target: "result", TargetHandle: "brief"},
			{ID: "e3", Source: "final", SourceHandle: "text", Target: "result", TargetHandle: "text"},
			{ID: "e4", Source: "result", SourceHandle: "image", Target: "output", TargetHandle: "image"},
			{ID: "e5", Source: "result", SourceHandle: "caption", Target: "output", TargetHandle: "caption"},
		},
		CreatedAt: fixtureTime,
	}
}

// mediaResultImageURL is the credential-free address a generation fact binds for assetRef.
func mediaResultImageURL(assetRef string) string {
	return "http://mock-provider.test/v1/assets/" + assetRef + ".png"
}

// mediaResultFacts is one generation fact per photo, made with digest and bound to the
// generated image's URL, and one review per example under policy-v1; the review of the
// last photo fails.
func mediaResultFacts(photos []domain.AssetRef, digest string) []domain.ExecutionFact {
	var facts []domain.ExecutionFact
	for i, p := range photos {
		gen := newExecutionFact("fact_gen_"+p.AssetID, "", "", "", "image_generated", "gen_"+p.AssetID)
		gen.Binding = json.RawMessage(fmt.Sprintf(`{"imageUrl":%q,"photoAssetId":%q,"settingsDigest":%q}`,
			mediaResultImageURL("gen_"+p.AssetID), p.AssetID, digest))
		gen.CreatedAt = fixtureTime.Add(time.Duration(i) * time.Minute)
		passed := i < len(photos)-1
		review := newExecutionFact("fact_review_"+p.AssetID, "", "", "", "asset_reviewed", "gen_"+p.AssetID)
		review.Binding = json.RawMessage(fmt.Sprintf(`{"photoAssetId":%q,"policyVersion":"policy-v1"}`, p.AssetID))
		review.Verdict = &passed
		review.CreatedAt = gen.CreatedAt.Add(30 * time.Second)
		facts = append(facts, gen, review)
	}
	return facts
}

func mediaResultInput(t *testing.T, photos []domain.AssetRef, digest string) string {
	t.Helper()
	examples := make([]map[string]string, len(photos))
	for i, p := range photos {
		examples[i] = map[string]string{"photoAssetId": p.AssetID, "assetRef": "gen_" + p.AssetID}
	}
	final, err := json.Marshal(map[string]any{"examples": examples, "settingsDigest": digest, "note": "chosen examples"})
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"caption": "ember sprites", "final": string(final), "photos": photos})
	if err != nil {
		t.Fatal(err)
	}
	return string(input)
}

// mediaResultCaption reads the caption and image the Output Node delivered into Run.output.
func mediaResultCaption(t *testing.T, h *execHarness, runID string) (map[string]any, domain.ImageRef, domain.Run) {
	t.Helper()
	run, err := h.getRun(runID)
	if err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	var output struct {
		Caption string          `json:"caption"`
		Image   json.RawMessage `json:"image"`
	}
	if err := json.Unmarshal(run.Output, &output); err != nil {
		t.Fatalf("Run.output %s is not the Media Output result: %v", run.Output, err)
	}
	image, err := domain.ParseImageRef(output.Image)
	if err != nil {
		t.Fatalf("Run.output image is not a valid ImageRef: %v", err)
	}
	var caption map[string]any
	if err := json.Unmarshal([]byte(output.Caption), &caption); err != nil {
		t.Fatalf("caption %q is not JSON: %v", output.Caption, err)
	}
	return caption, image, run
}

// The delivery is checked against each Run's own committed facts. Two Runs declare the
// same examples; the first Run's facts cover them, while the second Run's facts lack the
// last photo's generation. The second delivery is refused even though the first Run holds
// a fact with that very asset reference, and a refused delivery still completes the Run.
func TestMediaResult_FactsFromStore_CheckedPerRunThroughServicePath(t *testing.T) {
	h := execPhotoSetHarness(t)
	for _, reg := range []registry.NodeRegistration{mediaresult.Registration(), mediaoutput.Registration()} {
		if err := h.nodes.Register(reg); err != nil {
			t.Fatalf("register %s: %v", reg.Metadata.Type, err)
		}
	}
	def := h.saveDefinition(mediaResultDefinition("wf_media_result"))

	var photos []domain.AssetRef
	for i, name := range []string{"one", "two", "three"} {
		photos = append(photos, execCommitAsset(t, h, domain.Asset{
			AssetID: "asset_photo_" + name, MediaType: "image/png", SizeBytes: int64(500 + i),
			SHA256: strings.Repeat(fmt.Sprint(i+1), 64), CreatedAt: fixtureTime,
		}))
	}
	const digest = "sha256:template-settings"
	input := mediaResultInput(t, photos, digest)

	accepted := h.createRun(def.WorkflowID, def.Version, input)
	seedFactsOnRun(t, h, accepted.ID, "ar_media_accepted", mediaResultFacts(photos, digest))
	refused := h.createRun(def.WorkflowID, def.Version, input)
	refusedFacts := mediaResultFacts(photos, digest)
	refusedFacts = append(refusedFacts[:4:4], refusedFacts[5]) // drop the last photo's generation
	for i := range refusedFacts {
		refusedFacts[i].ID += "_refused"
	}
	seedFactsOnRun(t, h, refused.ID, "ar_media_refused", refusedFacts)

	h.drain(accepted.ID)
	h.drain(refused.ID)

	caption, image, run := mediaResultCaption(t, h, accepted.ID)
	if run.Status != domain.RunCompleted {
		t.Fatalf("accepted Run status = %s, want COMPLETED", run.Status)
	}
	if caption["accepted"] != true || caption["passedCount"] != float64(2) || caption["photoCount"] != float64(3) ||
		caption["policyVersion"] != "policy-v1" || caption["settingsDigest"] != digest {
		t.Fatalf("accepted Run caption = %v, want accepted, 2 of 3 passed, policy-v1 and the template digest", caption)
	}
	if want := mediaResultImageURL("gen_asset_photo_one"); image.Source != domain.ImageSourceExternal || image.URI != want {
		t.Fatalf("accepted Run image = %+v, want the EXTERNAL cover example at %s from its generation fact", image, want)
	}

	caption, _, run = mediaResultCaption(t, h, refused.ID)
	if run.Status != domain.RunCompleted {
		t.Fatalf("refused Run status = %s, want COMPLETED: a refused delivery is not an execution failure", run.Status)
	}
	reasons, _ := json.Marshal(caption["reasons"])
	if caption["accepted"] != false || !strings.Contains(string(reasons), `"code":"NOT_GENERATED","photoAssetId":"asset_photo_three"`) {
		t.Fatalf("refused Run caption = %v, want accepted=false naming the ungenerated example", caption)
	}
	nodeRuns, _, _ := h.snapshot(refused.ID)
	if nr := execNodeRunByNodeID(t, nodeRuns, "result"); nr.Status != domain.NodeRunSucceeded {
		t.Fatalf("media_result NodeRun status = %s, want SUCCEEDED", nr.Status)
	}
}
