package mediaresult

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testDigest = "sha256:template"
	testPolicy = "policy-v1"
)

var testPhotos = []string{"photo_a", "photo_b", "photo_c"}

// briefPortValue is the Media Brief `text` document for the given photos, as a JSON string.
func briefPortValue(t *testing.T, photoIDs ...string) json.RawMessage {
	t.Helper()
	photos := make([]photo, len(photoIDs))
	for i, id := range photoIDs {
		photos[i] = photo{MediaType: "image/png", PhotoAssetID: id, SHA256: strings.Repeat("a", 64), SizeBytes: 128}
	}
	doc, err := json.Marshal(map[string]any{"brief": "ember sprites", "photos": photos})
	if err != nil {
		t.Fatal(err)
	}
	return textValue(t, string(doc))
}

func textValue(t *testing.T, s string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// finalValue declares one example per entry, mapping photo to example assetRef "gen_<photo>".
func finalValue(t *testing.T, digest string, photoIDs ...string) json.RawMessage {
	t.Helper()
	examples := make([]example, len(photoIDs))
	for i, id := range photoIDs {
		examples[i] = example{PhotoAssetID: id, AssetRef: "gen_" + id}
	}
	doc, err := json.Marshal(map[string]any{"examples": examples, "settingsDigest": digest, "summary": "three sprites"})
	if err != nil {
		t.Fatal(err)
	}
	return textValue(t, string(doc))
}

// generatedURL is the credential-free address the generation fact binds for assetRef.
func generatedURL(assetRef string) string {
	return "http://mock-provider.test/v1/assets/" + assetRef + ".png"
}

func genFact(photoID, assetRef, digest string) domain.ExecutionFact {
	return domain.ExecutionFact{
		ID: "fact_gen_" + assetRef, FactType: factImageGenerated, SubjectRef: assetRef,
		Binding: json.RawMessage(fmt.Sprintf(`{"imageUrl":%q,"photoAssetId":%q,"settingsDigest":%q}`, generatedURL(assetRef), photoID, digest)),
	}
}

// genFactWithoutURL is a generation fact whose binding carries no imageUrl.
func genFactWithoutURL(photoID, assetRef, digest string) domain.ExecutionFact {
	fact := genFact(photoID, assetRef, digest)
	fact.Binding = json.RawMessage(fmt.Sprintf(`{"photoAssetId":%q,"settingsDigest":%q}`, photoID, digest))
	return fact
}

func reviewFact(photoID, assetRef, policy string, passed bool) domain.ExecutionFact {
	return domain.ExecutionFact{
		ID: "fact_review_" + assetRef, FactType: factAssetReviewed, SubjectRef: assetRef,
		Binding: json.RawMessage(fmt.Sprintf(`{"photoAssetId":%q,"policyVersion":%q}`, photoID, policy)),
		Verdict: &passed,
	}
}

// happyFacts generates every photo with the template settings and reviews each under one
// policy; the last photo's review fails.
func happyFacts() (generated, reviewed []domain.ExecutionFact) {
	for i, id := range testPhotos {
		generated = append(generated, genFact(id, "gen_"+id, testDigest))
		reviewed = append(reviewed, reviewFact(id, "gen_"+id, testPolicy, i < len(testPhotos)-1))
	}
	return generated, reviewed
}

func runNode(t *testing.T, final json.RawMessage, generated, reviewed []domain.ExecutionFact) (summary, domain.ImageRef) {
	t.Helper()
	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{
		Ports: map[string]json.RawMessage{textPort: final, briefPort: briefPortValue(t, testPhotos...)},
		Facts: map[string]registry.FactSet{
			factImageGenerated: {Facts: generated},
			factAssetReviewed:  {Facts: reviewed},
		},
	}, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Kind != registry.NodeResultCompleted {
		t.Fatalf("result kind = %v, want completed", result.Kind)
	}
	var captionText string
	if err := json.Unmarshal(result.Output.Ports[captionPort], &captionText); err != nil {
		t.Fatalf("caption port is not a JSON string: %v", err)
	}
	var got summary
	if err := json.Unmarshal([]byte(captionText), &got); err != nil {
		t.Fatalf("caption is not a summary document: %v", err)
	}
	image, err := domain.ParseImageRef(result.Output.Ports[imagePort])
	if err != nil {
		t.Fatalf("image port is not a valid ImageRef: %v", err)
	}
	return got, image
}

func wantRejected(t *testing.T, got summary, code, photoID string) {
	t.Helper()
	if got.Accepted {
		t.Fatalf("accepted = true, want false with reason %s", code)
	}
	for _, r := range got.Reasons {
		if r.Code == code && r.PhotoAssetID == photoID {
			return
		}
	}
	t.Fatalf("reasons = %+v, want %s for %q", got.Reasons, code, photoID)
}

func TestMediaResult_EveryRuleHolds_AcceptedWithPassCountFromFacts(t *testing.T) {
	generated, reviewed := happyFacts()
	got, image := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)

	if !got.Accepted || len(got.Reasons) != 0 {
		t.Fatalf("accepted = %v reasons = %+v, want accepted with no reasons", got.Accepted, got.Reasons)
	}
	if got.PassedCount != 2 || got.PhotoCount != 3 {
		t.Fatalf("passedCount/photoCount = %d/%d, want 2/3 from the review verdicts", got.PassedCount, got.PhotoCount)
	}
	if len(got.NotPassed) != 1 || got.NotPassed[0] != "photo_c" {
		t.Fatalf("notPassed = %v, want [photo_c]", got.NotPassed)
	}
	if got.PolicyVersion != testPolicy || got.SettingsDigest != testDigest {
		t.Fatalf("policyVersion/settingsDigest = %q/%q, want %q/%q", got.PolicyVersion, got.SettingsDigest, testPolicy, testDigest)
	}
	wantGeneratedCover(t, image)
}

// wantGeneratedCover asserts the image port carries the cover example through the URL its
// generation fact binds.
func wantGeneratedCover(t *testing.T, image domain.ImageRef) {
	t.Helper()
	if image.Source != domain.ImageSourceExternal || image.URI != generatedURL("gen_photo_a") || image.MediaType != "image/png" {
		t.Fatalf("image = %+v, want the EXTERNAL image at the cover example's generation fact URL", image)
	}
}

// wantCoverPhoto asserts the image port fell back to the cover photo's own AssetRef.
func wantCoverPhoto(t *testing.T, image domain.ImageRef) {
	t.Helper()
	if image.Source != domain.ImageSourceAsset || image.Asset == nil || image.Asset.AssetID != "photo_a" {
		t.Fatalf("image = %+v, want the cover photo's AssetRef", image)
	}
}

func TestMediaResult_CoverExampleWithoutGenerationFact_FallsBackToCoverPhoto(t *testing.T) {
	generated, reviewed := happyFacts()
	got, image := runNode(t, finalValue(t, testDigest, testPhotos...), generated[1:], reviewed)

	wantRejected(t, got, reasonNotGenerated, "photo_a")
	wantCoverPhoto(t, image)
}

func TestMediaResult_GenerationFactWithoutImageURL_FallsBackToCoverPhoto(t *testing.T) {
	generated, reviewed := happyFacts()
	generated[0] = genFactWithoutURL("photo_a", "gen_photo_a", testDigest)
	got, image := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)

	if !got.Accepted {
		t.Fatalf("reasons = %+v, want accepted: imageUrl is not a delivery check", got.Reasons)
	}
	wantCoverPhoto(t, image)
}

func TestMediaResult_FinalReportsItsOwnURL_IgnoredInFavourOfFact(t *testing.T) {
	generated, reviewed := happyFacts()
	reported := "http://elsewhere.test/model-chosen.png"
	examples := make([]map[string]string, len(testPhotos))
	for i, id := range testPhotos {
		examples[i] = map[string]string{"photoAssetId": id, "assetRef": "gen_" + id, "imageUrl": reported}
	}
	doc, err := json.Marshal(map[string]any{"examples": examples, "settingsDigest": testDigest, "imageUrl": reported, "coverUrl": reported})
	if err != nil {
		t.Fatal(err)
	}
	got, image := runNode(t, textValue(t, string(doc)), generated, reviewed)
	if !got.Accepted {
		t.Fatalf("reasons = %+v, want accepted", got.Reasons)
	}
	wantGeneratedCover(t, image)

	// Without a fact URL the self-reported URL still does not reach the port.
	generated[0] = genFactWithoutURL("photo_a", "gen_photo_a", testDigest)
	_, image = runNode(t, textValue(t, string(doc)), generated, reviewed)
	wantCoverPhoto(t, image)
}

func TestMediaResult_SameInput_ByteIdenticalCaption(t *testing.T) {
	generated, reviewed := happyFacts()
	input := registry.NodeInput{
		Ports: map[string]json.RawMessage{textPort: finalValue(t, testDigest, testPhotos...), briefPort: briefPortValue(t, testPhotos...)},
		Facts: map[string]registry.FactSet{factImageGenerated: {Facts: generated}, factAssetReviewed: {Facts: reviewed}},
	}
	first, err := Executor{}.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Executor{}.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `"{\"accepted\":true,\"notPassed\":[\"photo_c\"],\"passedCount\":2,\"photoCount\":3,\"policyVersion\":\"policy-v1\",\"reasons\":[],\"settingsDigest\":\"sha256:template\"}"`
	if string(first.Output.Ports[captionPort]) != want || string(second.Output.Ports[captionPort]) != want {
		t.Fatalf("caption = %s / %s, want %s", first.Output.Ports[captionPort], second.Output.Ports[captionPort], want)
	}
}

func TestMediaResult_PhotoWithoutExample_RejectedNodeStillSucceeds(t *testing.T) {
	generated, reviewed := happyFacts()
	got, _ := runNode(t, finalValue(t, testDigest, "photo_a", "photo_b"), generated, reviewed)
	wantRejected(t, got, reasonMissingExample, "photo_c")
}

func TestMediaResult_PhotoWithTwoExamples_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	got, _ := runNode(t, finalValue(t, testDigest, "photo_a", "photo_b", "photo_c", "photo_c"), generated, reviewed)
	wantRejected(t, got, reasonDuplicateExample, "photo_c")
}

func TestMediaResult_ExampleNotGeneratedInRun_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated[:2], reviewed)
	wantRejected(t, got, reasonNotGenerated, "photo_c")
}

func TestMediaResult_ExampleGeneratedForAnotherPhoto_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	generated[2] = genFact("photo_a", "gen_photo_c", testDigest)
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	wantRejected(t, got, reasonPhotoMismatch, "photo_c")
}

func TestMediaResult_ExampleWithOtherSettings_RejectedAndDigestNotReported(t *testing.T) {
	generated, reviewed := happyFacts()
	generated[1] = genFact("photo_b", "gen_photo_b", "sha256:other")
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	wantRejected(t, got, reasonSettingsMismatch, "photo_b")
	if got.SettingsDigest != "" {
		t.Fatalf("settingsDigest = %q, want empty when the examples disagree", got.SettingsDigest)
	}
}

func TestMediaResult_DeclaredDigestDiffersFromFacts_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	got, _ := runNode(t, finalValue(t, "sha256:claimed", testPhotos...), generated, reviewed)
	wantRejected(t, got, reasonSettingsMismatch, "photo_a")
}

func TestMediaResult_ReviewsUnderTwoPolicies_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	reviewed[0] = reviewFact("photo_a", "gen_photo_a", "policy-v2", true)
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	wantRejected(t, got, reasonPolicyMismatch, "")
	if got.PolicyVersion != "" {
		t.Fatalf("policyVersion = %q, want empty when reviews disagree", got.PolicyVersion)
	}
}

func TestMediaResult_FactAboutForeignPhoto_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	generated = append(generated, genFact("photo_outside", "gen_photo_outside", testDigest))
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	wantRejected(t, got, reasonForeignSubject, "photo_outside")
}

func TestMediaResult_ExampleForForeignPhoto_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	got, _ := runNode(t, finalValue(t, testDigest, "photo_a", "photo_b", "photo_c", "photo_outside"), generated, reviewed)
	wantRejected(t, got, reasonForeignSubject, "photo_outside")
}

func TestMediaResult_NewestReviewDecides_PassCount(t *testing.T) {
	generated, reviewed := happyFacts()
	// photo_c failed review first, then a newer review of the same asset passed.
	reviewed = append(reviewed, reviewFact("photo_c", "gen_photo_c", testPolicy, true))
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	if !got.Accepted || got.PassedCount != 3 || len(got.NotPassed) != 0 {
		t.Fatalf("got %+v, want accepted with all three passed by the newest reviews", got)
	}
}

func TestMediaResult_UnreviewedExample_CountsAsNotPassed(t *testing.T) {
	generated, reviewed := happyFacts()
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed[1:])
	if !got.Accepted || got.PassedCount != 1 || len(got.NotPassed) != 2 || got.NotPassed[0] != "photo_a" {
		t.Fatalf("got %+v, want accepted, passedCount 1, notPassed [photo_a photo_c]", got)
	}
}

func TestMediaResult_TruncatedFacts_Rejected(t *testing.T) {
	generated, reviewed := happyFacts()
	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{
		Ports: map[string]json.RawMessage{textPort: finalValue(t, testDigest, testPhotos...), briefPort: briefPortValue(t, testPhotos...)},
		Facts: map[string]registry.FactSet{
			factImageGenerated: {Facts: generated, Truncated: true},
			factAssetReviewed:  {Facts: reviewed},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output.Ports[captionPort]), reasonFactsIncomplete) {
		t.Fatalf("caption = %s, want %s", result.Output.Ports[captionPort], reasonFactsIncomplete)
	}
}

func TestMediaResult_ManyFailures_ReasonsBounded(t *testing.T) {
	var examples []example
	for i := 0; i < 3*maxReasons; i++ {
		examples = append(examples, example{PhotoAssetID: fmt.Sprintf("outside_%d", i), AssetRef: "x"})
	}
	doc, _ := json.Marshal(map[string]any{"examples": examples, "settingsDigest": testDigest})
	got, _ := runNode(t, textValue(t, string(doc)), nil, nil)
	if len(got.Reasons) != maxReasons || got.Reasons[maxReasons-1].Code != reasonsTruncated {
		t.Fatalf("len(reasons) = %d, want %d ending in %s", len(got.Reasons), maxReasons, reasonsTruncated)
	}
}

func TestMediaResult_MalformedInput_FailsWithoutQuotingContent(t *testing.T) {
	brief := briefPortValue(t, testPhotos...)
	cases := map[string]map[string]json.RawMessage{
		"final not a string":     {textPort: json.RawMessage(`{"examples":[]}`), briefPort: brief},
		"final not JSON":         {textPort: textValue(t, "secret-ish {not json"), briefPort: brief},
		"final not an object":    {textPort: textValue(t, `["secret-ish"]`), briefPort: brief},
		"final wrong member":     {textPort: textValue(t, `{"examples":"secret-ish"}`), briefPort: brief},
		"final missing":          {briefPort: brief},
		"brief not a document":   {textPort: finalValue(t, testDigest, testPhotos...), briefPort: textValue(t, "secret-ish")},
		"brief without photos":   {textPort: finalValue(t, testDigest, testPhotos...), briefPort: textValue(t, `{"brief":"secret-ish","photos":[]}`)},
		"brief repeats a photo":  {textPort: finalValue(t, testDigest, testPhotos...), briefPort: briefPortValue(t, "photo_a", "photo_a")},
		"brief missing entirely": {textPort: finalValue(t, testDigest, testPhotos...)},
	}
	for name, ports := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Executor{}.Execute(context.Background(), registry.NodeInput{Ports: ports}, nil)
			if err == nil {
				t.Fatal("Execute succeeded, want an error for unreadable input")
			}
			if strings.Contains(err.Error(), "secret-ish") {
				t.Fatalf("error %q quotes port content", err)
			}
		})
	}
}

func TestMediaResult_Registration_DeclaresPortsAndFactInputs(t *testing.T) {
	reg := Registration()
	if err := reg.Metadata.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	meta := reg.Metadata
	if meta.ExecutionKind != domain.NodeExecutionSync || meta.SideEffect.Kind != domain.SideEffectNone {
		t.Fatalf("kind/side effect = %s/%s, want SYNC/NONE", meta.ExecutionKind, meta.SideEffect.Kind)
	}
	if got := fmt.Sprint(meta.FactInputs); got != "[image_generated asset_reviewed]" {
		t.Fatalf("FactInputs = %s", got)
	}
	ports := func(ps []domain.PortMetadata) string {
		var out []string
		for _, p := range ps {
			out = append(out, fmt.Sprintf("%s:%s:%v", p.Name, p.DataType, p.Required))
		}
		return strings.Join(out, ",")
	}
	if got := ports(meta.Inputs); got != "text:text:true,brief:text:true" {
		t.Fatalf("inputs = %s", got)
	}
	if got := ports(meta.Outputs); got != "image:image:true,caption:text:true" {
		t.Fatalf("outputs = %s", got)
	}
}
