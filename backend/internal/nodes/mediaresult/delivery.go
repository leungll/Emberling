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
	// declared template settings.
	reasonSettingsMismatch = "SETTINGS_MISMATCH"
	// reasonPolicyMismatch: the examples were reviewed under more than one policy version.
	reasonPolicyMismatch = "POLICY_MISMATCH"
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
// ImageURL is the Provider's credential-free address of the generated image; facts
// recorded before the generation Tool bound it leave it empty.
type generation struct {
	ImageURL       string `json:"imageUrl"`
	PhotoAssetID   string `json:"photoAssetId"`
	SettingsDigest string `json:"settingsDigest"`
}

type review struct {
	PhotoAssetID  string `json:"photoAssetId"`
	PolicyVersion string `json:"policyVersion"`
	passed        bool
}

// checkDelivery is the pure delivery decision. Every value it reports comes from the
// photo set and the facts; the FINAL only names which generated asset is each photo's
// example and which settings the template uses.
//
// It also returns the cover example's image URL: the imageUrl bound by the newest
// generation fact of the example declared for the first photo, provided that fact records
// the generation for that photo. It is empty when no such fact or binding exists. A URL
// the FINAL reports for itself is never read.
func checkDelivery(photos []photo, final finalOutput, generated, reviewed registry.FactSet) (summary, string) {
	var reasons reasonList
	inSet := make(map[string]bool, len(photos))
	for _, p := range photos {
		inSet[p.PhotoAssetID] = true
	}
	if generated.Truncated || reviewed.Truncated {
		reasons.add(reasonFactsIncomplete, "", "")
	}

	// Every fact subject must belong to the photo set. Facts arrive oldest first, so the
	// last fact about an asset is its newest and supersedes the earlier ones.
	generations := make(map[string]generation, len(generated.Facts))
	for _, fact := range generated.Facts {
		var g generation
		if !decodeBinding(fact, &g) {
			reasons.add(reasonMalformedFact, "", fact.SubjectRef)
			continue
		}
		if !inSet[g.PhotoAssetID] {
			reasons.add(reasonForeignSubject, g.PhotoAssetID, fact.SubjectRef)
		}
		generations[fact.SubjectRef] = g
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

	// The examples' reviews share one policy version, and the pass count comes from their
	// newest review verdicts. An example never reviewed has not passed.
	policies := make(map[string]bool)
	out := summary{PhotoCount: len(photos), NotPassed: []string{}}
	for _, p := range photos {
		assetRef, ok := exampleOf[p.PhotoAssetID]
		r, reviewedOK := reviews[assetRef]
		if ok && reviewedOK && r.PhotoAssetID == p.PhotoAssetID {
			policies[r.PolicyVersion] = true
			if r.passed {
				out.PassedCount++
				continue
			}
		}
		out.NotPassed = append(out.NotPassed, p.PhotoAssetID)
	}
	if len(policies) > 1 {
		reasons.add(reasonPolicyMismatch, "", "")
	}

	out.PolicyVersion = soleValue(policies)
	out.SettingsDigest = soleValue(digests)
	out.Reasons = reasons.items
	if out.Reasons == nil {
		out.Reasons = []reason{}
	}
	out.Accepted = len(out.Reasons) == 0
	return out, generations[exampleOf[photos[0].PhotoAssetID]].ImageURL
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
