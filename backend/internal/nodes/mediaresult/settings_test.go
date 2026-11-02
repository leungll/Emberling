package mediaresult

import (
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// roundFacts generates every photo once per round, each round with its own settings
// digest, and reviews every generation under one policy. passedInRound decides the
// verdicts of a round.
func roundFacts(digests []string, passedInRound func(round int) bool) (generated, reviewed []domain.ExecutionFact) {
	for round, digest := range digests {
		for _, id := range testPhotos {
			assetRef := roundAsset(round, id)
			generated = append(generated, genFact(id, assetRef, digest))
			reviewed = append(reviewed, reviewFact(id, assetRef, testPolicy, passedInRound(round)))
		}
	}
	return generated, reviewed
}

func roundAsset(round int, photoID string) string {
	return "gen_r" + string(rune('1'+round)) + "_" + photoID
}

// roundFinal declares, for each photo, the example generated in the given round, and the
// given settings and digest.
func roundFinal(t *testing.T, rounds []int, settings json.RawMessage, digest string) json.RawMessage {
	t.Helper()
	examples := make([]example, len(testPhotos))
	for i, id := range testPhotos {
		examples[i] = example{PhotoAssetID: id, AssetRef: roundAsset(rounds[i], id)}
	}
	members := map[string]any{"examples": examples, "settingsDigest": digest}
	if settings != nil {
		members["settings"] = settings
	}
	doc, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	return textValue(t, string(doc))
}

var (
	firstRoundSettings = json.RawMessage(`{"prompt":"ember sprites, strong grain","resolution":"1K"}`)
	lastRoundSettings  = json.RawMessage(`{"aspectRatio":"3:4","prompt":"ember sprites, soft grain","resolution":"1K"}`)
)

func TestMediaResult_LastRoundAllPassed_Accepted(t *testing.T) {
	first, last := mustSettingsDigest(firstRoundSettings), mustSettingsDigest(lastRoundSettings)
	generated, reviewed := roundFacts([]string{first, last}, func(round int) bool { return round == 1 })

	got, _ := runNode(t, roundFinal(t, []int{1, 1, 1}, lastRoundSettings, last), generated, reviewed)

	if !got.Accepted || len(got.Reasons) != 0 {
		t.Fatalf("accepted = %v reasons = %+v, want the last round accepted", got.Accepted, got.Reasons)
	}
	if got.PassedCount != 3 || got.SettingsDigest != last {
		t.Fatalf("passedCount/settingsDigest = %d/%q, want 3/%q", got.PassedCount, got.SettingsDigest, last)
	}
}

func TestMediaResult_ImagesFromDifferentRounds_RejectedWithSettingsMismatch(t *testing.T) {
	first, last := mustSettingsDigest(firstRoundSettings), mustSettingsDigest(lastRoundSettings)
	generated, reviewed := roundFacts([]string{first, last}, func(int) bool { return true })

	// photo_a keeps its first-round image; the others come from the last round.
	got, _ := runNode(t, roundFinal(t, []int{0, 1, 1}, lastRoundSettings, last), generated, reviewed)

	wantRejected(t, got, reasonSettingsMismatch, "photo_a")
	if got.SettingsDigest != "" {
		t.Fatalf("settingsDigest = %q, want empty when the examples come from different rounds", got.SettingsDigest)
	}
}

func TestMediaResult_FinalSettingsDisagreeWithDigest_RejectedWithSettingsMismatch(t *testing.T) {
	first, last := mustSettingsDigest(firstRoundSettings), mustSettingsDigest(lastRoundSettings)
	generated, reviewed := roundFacts([]string{first, last}, func(round int) bool { return round == 1 })

	// The digest and the images are the last round's, but the settings are the first
	// round's: the delivered settings did not produce the delivered images.
	got, _ := runNode(t, roundFinal(t, []int{1, 1, 1}, firstRoundSettings, last), generated, reviewed)

	wantRejected(t, got, reasonSettingsMismatch, "")
}

func TestMediaResult_FinalWithoutUsableSettings_RejectedWithSettingsMismatch(t *testing.T) {
	last := mustSettingsDigest(lastRoundSettings)
	generated, reviewed := roundFacts([]string{last}, func(int) bool { return true })
	cases := map[string]json.RawMessage{
		"settings absent":       nil,
		"settings null":         json.RawMessage(`null`),
		"settings not object":   json.RawMessage(`"ember sprites, soft grain"`),
		"settings empty object": json.RawMessage(`{}`),
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			got, _ := runNode(t, roundFinal(t, []int{0, 0, 0}, settings, last), generated, reviewed)
			wantRejected(t, got, reasonSettingsMismatch, "")
		})
	}
}

// TestSettingsDigest_MatchesRuntimeDigest proves the delivery check's digest is the one
// the Runtime computes, for settings that differ in key order, whitespace, number
// spelling, nesting and characters encoding/json would otherwise escape.
func TestSettingsDigest_MatchesRuntimeDigest(t *testing.T) {
	samples := []string{
		`{"prompt":"ember sprites"}`,
		`{ "resolution" : "1K", "prompt" : "ember sprites" }`,
		`{"prompt":"a <b> & c > d é  ","seed":42}`,
		`{"prompt":"x","seed":1.50,"nested":{"z":[1,2.0,{"b":null,"a":true}],"a":"\"q\""}}`,
		`{"prompt":"中文 提示词","aspectRatio":"16:9"}`,
	}
	for _, sample := range samples {
		want, err := runtime.SettingsDigest(json.RawMessage(sample))
		if err != nil {
			t.Fatalf("runtime.SettingsDigest(%s): %v", sample, err)
		}
		got, ok := settingsDigest(json.RawMessage(sample))
		if !ok || got != want {
			t.Errorf("settingsDigest(%s) = %q, %v; want %q", sample, got, ok, want)
		}
	}
}
