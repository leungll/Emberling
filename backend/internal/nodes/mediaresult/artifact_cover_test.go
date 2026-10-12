package mediaresult

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// generatedArtifact is the artifact a generation of photoID saved; its digest differs per
// photo, so each photo's example is a distinct artifact.
func generatedArtifact(photoID string) domain.ArtifactRef {
	sum := sha256.Sum256([]byte("generated from " + photoID))
	digest := hex.EncodeToString(sum[:])
	id, _ := domain.ArtifactIDForSHA256(digest)
	return domain.ArtifactRef{ArtifactID: id, MediaType: "image/png", SizeBytes: 321, SHA256: digest}
}

// artifactBinding renders an artifact the way a generation fact binds it: the canonical
// JSON of the reference, keys sorted, as one string.
func artifactBinding(t *testing.T, ref domain.ArtifactRef) string {
	t.Helper()
	return fmt.Sprintf(`{"artifactId":%q,"mediaType":%q,"sha256":%q,"sizeBytes":%d}`, ref.ArtifactID, ref.MediaType, ref.SHA256, ref.SizeBytes)
}

// artifactGenFact is a generation fact in the current form: its subject is the artifact
// id, and it binds the artifact and the Run-scoped site-relative imageUrl.
func artifactGenFact(t *testing.T, photoID string, ref domain.ArtifactRef, digest string) domain.ExecutionFact {
	t.Helper()
	binding, err := json.Marshal(map[string]string{
		"artifact":       artifactBinding(t, ref),
		"imageUrl":       "/api/runs/run_1/artifacts/" + ref.ArtifactID + "/content",
		"photoAssetId":   photoID,
		"settingsDigest": digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return domain.ExecutionFact{ID: "fact_gen_" + ref.ArtifactID, FactType: factImageGenerated, SubjectRef: ref.ArtifactID, Binding: binding}
}

// artifactFinal declares each photo's generated artifact as its example.
func artifactFinal(t *testing.T) json.RawMessage {
	t.Helper()
	examples := make([]example, len(testPhotos))
	for i, id := range testPhotos {
		examples[i] = example{PhotoAssetID: id, AssetRef: generatedArtifact(id).ArtifactID}
	}
	doc, err := json.Marshal(map[string]any{"examples": examples, "settingsDigest": testDigest})
	if err != nil {
		t.Fatal(err)
	}
	return textValue(t, string(doc))
}

func artifactFacts(t *testing.T) (generated, reviewed []domain.ExecutionFact) {
	t.Helper()
	for _, id := range testPhotos {
		ref := generatedArtifact(id)
		generated = append(generated, artifactGenFact(t, id, ref, testDigest))
		reviewed = append(reviewed, reviewFact(id, ref.ArtifactID, testPolicy, true))
	}
	return generated, reviewed
}

// TestMediaResult_ArtifactBinding_EmitsArtifactImageRef covers the current generation
// fact: the cover example is published as the ARTIFACT reference its fact binds, not as
// the site-relative imageUrl, which is no EXTERNAL address.
func TestMediaResult_ArtifactBinding_EmitsArtifactImageRef(t *testing.T) {
	generated, reviewed := artifactFacts(t)
	got, image := runNode(t, artifactFinal(t), generated, reviewed)

	if !got.Accepted || got.PassedCount != 3 {
		t.Fatalf("summary = %+v, want accepted with 3 passed", got)
	}
	want := generatedArtifact("photo_a")
	if image.Source != domain.ImageSourceArtifact || image.Artifact == nil || *image.Artifact != want {
		t.Fatalf("image = %+v, want the ARTIFACT reference %+v of the cover example", image, want)
	}
}

// TestMediaResult_LegacyFactWithoutArtifact_FallsBack covers generation facts recorded
// before facts bound the artifact, and artifact bindings that cannot be used: a legacy
// fact's absolute imageUrl is published as EXTERNAL, and anything else falls back to the
// cover photo.
func TestMediaResult_LegacyFactWithoutArtifact_FallsBack(t *testing.T) {
	t.Run("legacy fact with an absolute imageUrl", func(t *testing.T) {
		generated, reviewed := happyFacts()
		got, image := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
		if !got.Accepted {
			t.Fatalf("reasons = %+v, want accepted", got.Reasons)
		}
		wantGeneratedCover(t, image)
	})

	unusable := map[string]func(ref domain.ArtifactRef) string{
		"artifact binding is not JSON": func(domain.ArtifactRef) string { return "not json" },
		"artifact id not derived from its digest": func(ref domain.ArtifactRef) string {
			ref.ArtifactID = "artifact_" + "0123456789abcdef0123456789abcdef"
			return artifactBinding(t, ref)
		},
		"artifact of another subject": func(domain.ArtifactRef) string {
			return artifactBinding(t, generatedArtifact("photo_b"))
		},
		"unsupported media type": func(ref domain.ArtifactRef) string {
			ref.MediaType = "image/svg+xml"
			return artifactBinding(t, ref)
		},
	}
	for name, binding := range unusable {
		t.Run(name, func(t *testing.T) {
			generated, reviewed := artifactFacts(t)
			ref := generatedArtifact("photo_a")
			raw, err := json.Marshal(map[string]string{
				"artifact":       binding(ref),
				"imageUrl":       "/api/runs/run_1/artifacts/" + ref.ArtifactID + "/content",
				"photoAssetId":   "photo_a",
				"settingsDigest": testDigest,
			})
			if err != nil {
				t.Fatal(err)
			}
			generated[0].Binding = raw
			got, image := runNode(t, artifactFinal(t), generated, reviewed)
			if !got.Accepted {
				t.Fatalf("reasons = %+v, want accepted: the cover image is not a delivery check", got.Reasons)
			}
			wantCoverPhoto(t, image)
		})
	}
}
