package mediaresult

import (
	"encoding/json"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// Reason codes. Each names one failed delivery check; any reason makes accepted false.
const (
	// reasonFactsIncomplete: the Run holds more facts of a declared type than reached the
	// node, so no check over the whole set can be trusted.
	reasonFactsIncomplete = "FACTS_INCOMPLETE"
	// reasonMalformedFact: a fact's binding is not the object its producing Tool declares.
	reasonMalformedFact = "MALFORMED_FACT"
	// reasonForeignSubject: a fact or a declared example belongs to a photo outside the set.
	reasonForeignSubject = "FOREIGN_SUBJECT"
	// reasonMissingExample / reasonDuplicateExample: a photo has no example, or several.
	reasonMissingExample   = "MISSING_EXAMPLE"
	reasonDuplicateExample = "DUPLICATE_EXAMPLE"
	// reasonNotGenerated: a declared example is not an image this Run generated.
	reasonNotGenerated = "NOT_GENERATED"
	// reasonPhotoMismatch: a declared example was generated for a different photo.
	reasonPhotoMismatch = "PHOTO_MISMATCH"
	// reasonSettingsMismatch: an example was generated with settings other than the
	// declared template settings, or the declared settings object is missing or does not
	// hash to the declared settings digest.
	reasonSettingsMismatch = "SETTINGS_MISMATCH"
	// reasonPolicyMismatch: the examples were reviewed under more than one policy version
	// or by more than one review model, such as the mock policy for some and a vision
	// model for others.
	reasonPolicyMismatch = "POLICY_MISMATCH"
	// reasonStaleExample: an example whose newest review did not pass, or that was never
	// reviewed, while a later image generated for the same photo passed its newest
	// review. A delivery may include an example that did not pass only when the Run
	// holds no better image for that photo.
	reasonStaleExample = "STALE_EXAMPLE"
	// reasonsTruncated closes a reason list that reached maxReasons.
	reasonsTruncated = "REASONS_TRUNCATED"
)

// maxReasons bounds the caption: past it the list ends with one reasonsTruncated entry.
const maxReasons = 32

// summary is the caption document. Field order is alphabetical so the encoding is
// canonical. PassedCount and NotPassed report review outcomes; they do not decide
// acceptance, because a delivery may legitimately include examples that did not pass.
type summary struct {
	Accepted       bool     `json:"accepted"`
	NotPassed      []string `json:"notPassed"`
	PassedCount    int      `json:"passedCount"`
	PhotoCount     int      `json:"photoCount"`
	PolicyVersion  string   `json:"policyVersion"`
	Reasons        []reason `json:"reasons"`
	SettingsDigest string   `json:"settingsDigest"`
}

// reason is one failed check, naming at most the photo and the asset it concerns.
type reason struct {
	AssetRef     string `json:"assetRef,omitempty"`
	Code         string `json:"code"`
	PhotoAssetID string `json:"photoAssetId,omitempty"`
}

type reasonList struct{ items []reason }

func (l *reasonList) add(code, photoAssetID, assetRef string) {
	switch {
	case len(l.items) < maxReasons-1:
		l.items = append(l.items, reason{Code: code, PhotoAssetID: photoAssetID, AssetRef: assetRef})
	case len(l.items) == maxReasons-1:
		l.items = append(l.items, reason{Code: reasonsTruncated})
	}
}

// generation and review are the bindings of the newest fact about one generated asset.
// Artifact is the canonical JSON of the saved image's ArtifactRef; facts recorded before
// the generation Tool bound it leave it empty and carry only ImageURL, which such facts
// recorded as the Provider's absolute, credential-free address of the image. Facts that
// predate both leave both empty.
type generation struct {
	Artifact       string `json:"artifact"`
	ImageURL       string `json:"imageUrl"`
	PhotoAssetID   string `json:"photoAssetId"`
	SettingsDigest string `json:"settingsDigest"`
}

// ReviewModelID is empty on a fact recorded before reviews bound their review model.
type review struct {
	PhotoAssetID  string `json:"photoAssetId"`
	PolicyVersion string `json:"policyVersion"`
	ReviewModelID string `json:"reviewModelId"`
	passed        bool
}

// checkDelivery is the pure delivery decision. Every value it reports comes from the
// photo set and the facts; the FINAL only names which generated asset is each photo's
// example and which settings the template uses. The declared settings must hash to the
// declared digest, and every example's generation fact must bind that same digest, so
// the delivered settings are the ones that produced every delivered image: examples
// taken from different rounds of settings cannot be delivered together.
//
// It also returns the cover example: the example declared for the first photo with the
// bindings of its newest generation fact, provided that fact records the generation for
// that photo. It is empty when no such fact exists. An image the FINAL reports for itself
// is never read.
func checkDelivery(photos []photo, final finalOutput, generated, reviewed registry.FactSet) (summary, coverExample) {
	var reasons reasonList
	inSet := make(map[string]bool, len(photos))
	for _, p := range photos {
		inSet[p.PhotoAssetID] = true
	}
	if generated.Truncated || reviewed.Truncated {
		reasons.add(reasonFactsIncomplete, "", "")
	}
	if digest, ok := settingsDigest(final.Settings); !ok || digest != final.SettingsDigest {
		reasons.add(reasonSettingsMismatch, "", "")
	}

	// Every fact subject must belong to the photo set. Facts arrive oldest first, so the
	// last fact about an asset is its newest and supersedes the earlier ones; its position
	// orders the asset against the other generations.
	generations := make(map[string]generation, len(generated.Facts))
	generatedAt := make(map[string]int, len(generated.Facts))
	for i, fact := range generated.Facts {
		var g generation
		if !decodeBinding(fact, &g) {
			reasons.add(reasonMalformedFact, "", fact.SubjectRef)
			continue
		}
		if !inSet[g.PhotoAssetID] {
			reasons.add(reasonForeignSubject, g.PhotoAssetID, fact.SubjectRef)
		}
		generations[fact.SubjectRef] = g
		generatedAt[fact.SubjectRef] = i
	}
	reviews := make(map[string]review, len(reviewed.Facts))
	for _, fact := range reviewed.Facts {
		var r review
		if !decodeBinding(fact, &r) {
			reasons.add(reasonMalformedFact, "", fact.SubjectRef)
			continue
		}
		if !inSet[r.PhotoAssetID] {
			reasons.add(reasonForeignSubject, r.PhotoAssetID, fact.SubjectRef)
		}
		r.passed = fact.Verdict != nil && *fact.Verdict
		reviews[fact.SubjectRef] = r
	}

	// Each photo has exactly one example, and each example is an image this Run generated
	// for that photo with the declared template settings.
	examplesPerPhoto := make(map[string]int, len(photos))
	exampleOf := make(map[string]string, len(photos))
	digests := make(map[string]bool)
	for _, ex := range final.Examples {
		if !inSet[ex.PhotoAssetID] {
			reasons.add(reasonForeignSubject, ex.PhotoAssetID, ex.AssetRef)
			continue
		}
		examplesPerPhoto[ex.PhotoAssetID]++
		g, ok := generations[ex.AssetRef]
		switch {
		case !ok:
			reasons.add(reasonNotGenerated, ex.PhotoAssetID, ex.AssetRef)
			continue
		case g.PhotoAssetID != ex.PhotoAssetID:
			reasons.add(reasonPhotoMismatch, ex.PhotoAssetID, ex.AssetRef)
			continue
		}
		digests[g.SettingsDigest] = true
		if final.SettingsDigest == "" || g.SettingsDigest != final.SettingsDigest {
			reasons.add(reasonSettingsMismatch, ex.PhotoAssetID, ex.AssetRef)
		}
		if _, chosen := exampleOf[ex.PhotoAssetID]; !chosen {
			exampleOf[ex.PhotoAssetID] = ex.AssetRef
		}
	}
	for _, p := range photos {
		switch n := examplesPerPhoto[p.PhotoAssetID]; {
		case n == 0:
			reasons.add(reasonMissingExample, p.PhotoAssetID, "")
		case n > 1:
			reasons.add(reasonDuplicateExample, p.PhotoAssetID, "")
		}
	}

	// The examples' reviews share one policy version and one review model, and the pass
	// count comes from their newest review verdicts. An example never reviewed has not
	// passed. An example that did not pass is accepted only while the Run holds no better
	// image for its photo: it is stale when a later generation for that photo passed.
	latestPass := latestPassingGeneration(generated.Facts, generations, reviews)
	policies := make(map[string]bool)
	reviewModels := make(map[string]bool)
	out := summary{PhotoCount: len(photos), NotPassed: []string{}}
	for _, p := range photos {
		assetRef, ok := exampleOf[p.PhotoAssetID]
		r, reviewedOK := reviews[assetRef]
		if ok && reviewedOK && r.PhotoAssetID == p.PhotoAssetID {
			policies[r.PolicyVersion] = true
			reviewModels[r.ReviewModelID] = true
			if r.passed {
				out.PassedCount++
				continue
			}
		}
		out.NotPassed = append(out.NotPassed, p.PhotoAssetID)
		if later, found := latestPass[p.PhotoAssetID]; ok && found && later > generatedAt[assetRef] {
			reasons.add(reasonStaleExample, p.PhotoAssetID, assetRef)
		}
	}
	if len(policies) > 1 || len(reviewModels) > 1 {
		reasons.add(reasonPolicyMismatch, "", "")
	}

	out.PolicyVersion = soleValue(policies)
	out.SettingsDigest = soleValue(digests)
	out.Reasons = reasons.items
	if out.Reasons == nil {
		out.Reasons = []reason{}
	}
	out.Accepted = len(out.Reasons) == 0
	cover := exampleOf[photos[0].PhotoAssetID]
	return out, coverExample{assetRef: cover, generation: generations[cover]}
}

// latestPassingGeneration maps each photo to the position, in the oldest-first fact
// order, of the newest generation for it whose newest review passed and reviewed it for
// that same photo. Photos with no passing generation are absent.
func latestPassingGeneration(facts []domain.ExecutionFact, generations map[string]generation, reviews map[string]review) map[string]int {
	latest := make(map[string]int)
	for i, fact := range facts {
		g, ok := generations[fact.SubjectRef]
		if !ok {
			continue
		}
		if r := reviews[fact.SubjectRef]; r.passed && r.PhotoAssetID == g.PhotoAssetID {
			latest[g.PhotoAssetID] = i
		}
	}
	return latest
}

// coverExample is the cover photo's declared example and the bindings of the generation
// fact that proves it. assetRef is empty when the cover photo has no proven example.
type coverExample struct {
	assetRef   string
	generation generation
}

// decodeBinding reads a fact binding into dst and reports whether it names a photo.
func decodeBinding(fact domain.ExecutionFact, dst interface{ photo() string }) bool {
	if err := json.Unmarshal(fact.Binding, dst); err != nil {
		return false
	}
	return dst.photo() != ""
}

func (g *generation) photo() string { return g.PhotoAssetID }
func (r *review) photo() string     { return r.PhotoAssetID }

// soleValue returns the one value of set, or "" when it holds none or several.
func soleValue(set map[string]bool) string {
	if len(set) != 1 {
		return ""
	}
	for value := range set {
		return value
	}
	return ""
}
