package runtime

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

const signedURL = "https://cdn.example.test/photo-a.jpg?X-Signature=s3cr3t-sig"

func argPointer(p string) domain.FactPointer {
	return domain.FactPointer{Source: domain.FactPointerArguments, Pointer: p}
}

func resultPointer(p string) domain.FactPointer {
	return domain.FactPointer{Source: domain.FactPointerResult, Pointer: p}
}

func boolPtr(v bool) *bool { return &v }

func imageGeneratedProduction() domain.FactProduction {
	return domain.FactProduction{
		FactType:       "image_generated",
		SubjectPointer: resultPointer("/asset/assetId"),
		BindArguments: map[string]domain.FactPointer{
			"photoAssetId":   argPointer("/photoAssetId"),
			"settingsDigest": resultPointer("/settingsDigest"),
		},
	}
}

func assetReviewedProduction() domain.FactProduction {
	return domain.FactProduction{
		FactType:       "asset_reviewed",
		SubjectPointer: argPointer("/assetRef"),
		BindArguments: map[string]domain.FactPointer{
			"photoAssetId":  argPointer("/photoAssetId"),
			"policyVersion": resultPointer("/policyVersion"),
		},
		VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/approved"},
		BasisFactType:  "image_generated",
	}
}

func TestExtractProducedFact(t *testing.T) {
	imageArgs := json.RawMessage(`{"photoAssetId":"photo-a","sourceUrl":"` + signedURL + `","settings":{"style":"warm"}}`)
	imageResult := json.RawMessage(`{"asset":{"assetId":"gen-1","url":"` + signedURL + `"},"settingsDigest":"sha256:abc"}`)

	tests := []struct {
		name       string
		production domain.FactProduction
		arguments  json.RawMessage
		result     json.RawMessage
		want       ProducedFact
		wantErr    *FactExtractionError
	}{
		{
			name:       "image generation result binds an argument and a result value",
			production: imageGeneratedProduction(),
			arguments:  imageArgs,
			result:     imageResult,
			want: ProducedFact{
				FactType:   "image_generated",
				SubjectRef: "gen-1",
				Binding:    map[string]string{"photoAssetId": "photo-a", "settingsDigest": "sha256:abc"},
			},
		},
		{
			name:       "review result carries a verdict and a basis fact type",
			production: assetReviewedProduction(),
			arguments:  json.RawMessage(`{"assetRef":"gen-1","photoAssetId":"photo-a"}`),
			result:     json.RawMessage(`{"approved":false,"policyVersion":"v2","notes":"blurry"}`),
			want: ProducedFact{
				FactType:      "asset_reviewed",
				SubjectRef:    "gen-1",
				Binding:       map[string]string{"photoAssetId": "photo-a", "policyVersion": "v2"},
				Verdict:       boolPtr(false),
				BasisFactType: "image_generated",
			},
		},
		{
			name: "non-string bindings use canonical JSON text",
			production: domain.FactProduction{
				FactType:       "measured",
				SubjectPointer: argPointer("/items/0/id"),
				BindArguments: map[string]domain.FactPointer{
					"count":   argPointer("/count"),
					"ratio":   argPointer("/ratio"),
					"enabled": argPointer("/enabled"),
					"options": resultPointer("/options"),
					"tags":    resultPointer("/tags"),
					"a/b":     resultPointer("/a~1b"),
				},
			},
			arguments: json.RawMessage(`{"items":[{"id":"x-1"}],"count":12,"ratio":1.50,"enabled":true}`),
			result:    json.RawMessage(`{"options": {"z": 1, "a": {"y": [2, 1], "b": "<&>"}}, "tags": ["b", "a"], "a/b": "slash"}`),
			want: ProducedFact{
				FactType:   "measured",
				SubjectRef: "x-1",
				Binding: map[string]string{
					"count":   "12",
					"ratio":   "1.50",
					"enabled": "true",
					"options": `{"a":{"b":"<&>","y":[2,1]},"z":1}`,
					"tags":    `["b","a"]`,
					"a/b":     "slash",
				},
			},
		},
		{
			name:       "missing subject",
			production: imageGeneratedProduction(),
			arguments:  imageArgs,
			result:     json.RawMessage(`{"asset":{"url":"` + signedURL + `"},"settingsDigest":"sha256:abc"}`),
			wantErr:    &FactExtractionError{FactType: "image_generated", Field: "subjectPointer", Source: domain.FactPointerResult, Pointer: "/asset/assetId", Reason: FactValueMissing},
		},
		{
			name:       "empty subject",
			production: imageGeneratedProduction(),
			arguments:  imageArgs,
			result:     json.RawMessage(`{"asset":{"assetId":""},"settingsDigest":"sha256:abc"}`),
			wantErr:    &FactExtractionError{FactType: "image_generated", Field: "subjectPointer", Source: domain.FactPointerResult, Pointer: "/asset/assetId", Reason: FactValueWrongType},
		},
		{
			name:       "non-string subject",
			production: imageGeneratedProduction(),
			arguments:  imageArgs,
			result:     json.RawMessage(`{"asset":{"assetId":7},"settingsDigest":"sha256:abc"}`),
			wantErr:    &FactExtractionError{FactType: "image_generated", Field: "subjectPointer", Source: domain.FactPointerResult, Pointer: "/asset/assetId", Reason: FactValueWrongType},
		},
		{
			name:       "wrong-typed verdict",
			production: assetReviewedProduction(),
			arguments:  json.RawMessage(`{"assetRef":"gen-1","photoAssetId":"photo-a"}`),
			result:     json.RawMessage(`{"approved":"yes","policyVersion":"v2"}`),
			wantErr:    &FactExtractionError{FactType: "asset_reviewed", Field: "verdictPointer", Source: domain.FactPointerResult, Pointer: "/approved", Reason: FactValueWrongType},
		},
		{
			name:       "missing argument binding",
			production: imageGeneratedProduction(),
			arguments:  json.RawMessage(`{"sourceUrl":"` + signedURL + `"}`),
			result:     imageResult,
			wantErr:    &FactExtractionError{FactType: "image_generated", Field: `bindArguments["photoAssetId"]`, Source: domain.FactPointerArguments, Pointer: "/photoAssetId", Reason: FactValueMissing},
		},
		{
			name:       "null binding is missing",
			production: imageGeneratedProduction(),
			arguments:  imageArgs,
			result:     json.RawMessage(`{"asset":{"assetId":"gen-1"},"settingsDigest":null}`),
			wantErr:    &FactExtractionError{FactType: "image_generated", Field: `bindArguments["settingsDigest"]`, Source: domain.FactPointerResult, Pointer: "/settingsDigest", Reason: FactValueMissing},
		},
		{
			name:       "invalid result document",
			production: imageGeneratedProduction(),
			arguments:  imageArgs,
			result:     json.RawMessage(`{"asset":` + signedURL),
			wantErr:    &FactExtractionError{FactType: "image_generated", Field: "subjectPointer", Source: domain.FactPointerResult, Pointer: "/asset/assetId", Reason: FactDocumentInvalid},
		},
		{
			name: "array index out of range",
			production: domain.FactProduction{
				FactType:       "listed",
				SubjectPointer: resultPointer("/items/2"),
			},
			result:  json.RawMessage(`{"items":["a","b"]}`),
			wantErr: &FactExtractionError{FactType: "listed", Field: "subjectPointer", Source: domain.FactPointerResult, Pointer: "/items/2", Reason: FactValueMissing},
		},
		{
			name: "unknown pointer source",
			production: domain.FactProduction{
				FactType:       "listed",
				SubjectPointer: domain.FactPointer{Source: "EVENT", Pointer: "/id"},
			},
			result:  json.RawMessage(`{"id":"a"}`),
			wantErr: &FactExtractionError{FactType: "listed", Field: "subjectPointer", Source: "EVENT", Pointer: "/id", Reason: FactPointerInvalid},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractProducedFact(tt.production, tt.arguments, tt.result)
			if tt.wantErr != nil {
				var extractErr *FactExtractionError
				if !errors.As(err, &extractErr) {
					t.Fatalf("ExtractProducedFact() error = %v, want *FactExtractionError", err)
				}
				if *extractErr != *tt.wantErr {
					t.Fatalf("ExtractProducedFact() error = %+v, want %+v", *extractErr, *tt.wantErr)
				}
				assertNoArgumentValues(t, err.Error())
				return
			}
			if err != nil {
				t.Fatalf("ExtractProducedFact() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ExtractProducedFact() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestProducedFact_BindingJSON(t *testing.T) {
	tests := []struct {
		name    string
		binding map[string]string
		want    string
	}{
		{name: "empty binding", binding: nil, want: `{}`},
		{name: "sorted keys", binding: map[string]string{"settingsDigest": "sha256:abc", "photoAssetId": "photo-a"}, want: `{"photoAssetId":"photo-a","settingsDigest":"sha256:abc"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ProducedFact{FactType: "image_generated", Binding: tt.binding}.BindingJSON()
			if err != nil {
				t.Fatalf("BindingJSON() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("BindingJSON() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSettingsDigest(t *testing.T) {
	base, err := SettingsDigest(json.RawMessage(`{"style":"warm","size":{"w":1024,"h":768},"seed":42,"tags":["a","b"]}`))
	if err != nil {
		t.Fatalf("SettingsDigest() error = %v", err)
	}
	if !strings.HasPrefix(base, "sha256:") || len(base) != len("sha256:")+64 {
		t.Fatalf("SettingsDigest() = %q, want sha256: followed by 64 hex characters", base)
	}

	tests := []struct {
		name     string
		settings string
		same     bool
	}{
		{name: "keys reordered at every depth", settings: `{"tags":["a","b"],"seed":42,"size":{"h":768,"w":1024},"style":"warm"}`, same: true},
		{name: "whitespace and newlines", settings: "{\n  \"style\" : \"warm\",\n\t\"size\": { \"w\": 1024, \"h\": 768 },\n  \"seed\": 42, \"tags\": [ \"a\", \"b\" ]\n}\n", same: true},
		{name: "changed value", settings: `{"style":"cool","size":{"w":1024,"h":768},"seed":42,"tags":["a","b"]}`, same: false},
		{name: "array order is significant", settings: `{"style":"warm","size":{"w":1024,"h":768},"seed":42,"tags":["b","a"]}`, same: false},
		{name: "number literal text is kept", settings: `{"style":"warm","size":{"w":1024,"h":768},"seed":42.0,"tags":["a","b"]}`, same: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SettingsDigest(json.RawMessage(tt.settings))
			if err != nil {
				t.Fatalf("SettingsDigest() error = %v", err)
			}
			if (got == base) != tt.same {
				t.Fatalf("SettingsDigest() = %q, base %q, want same=%v", got, base, tt.same)
			}
		})
	}

	for _, invalid := range []string{``, `[1,2]`, `"warm"`, `{"a":1} {"b":2}`, `{"a":`} {
		if _, err := SettingsDigest(json.RawMessage(invalid)); err == nil {
			t.Errorf("SettingsDigest(%q) error = nil, want error", invalid)
		}
	}
}

func reviewFact(id, subject, photo, policy string, verdict bool) domain.ExecutionFact {
	binding, _ := json.Marshal(map[string]string{"photoAssetId": photo, "policyVersion": policy})
	return domain.ExecutionFact{ID: id, FactType: "asset_reviewed", SubjectRef: subject, Binding: binding, Verdict: &verdict}
}

func videoRequirement() domain.FactRequirement {
	return domain.FactRequirement{
		FactType:        "asset_reviewed",
		SubjectArgument: "/assetRef",
		MatchBindings:   []string{"photoAssetId"},
		RequireVerdict:  boolPtr(true),
	}
}

func TestMatchRequirement(t *testing.T) {
	videoArgs := func(asset, photo string) json.RawMessage {
		return json.RawMessage(`{"assetRef":"` + asset + `","photoAssetId":"` + photo + `","callbackUrl":"` + signedURL + `"}`)
	}
	generated := domain.ExecutionFact{ID: "fact-gen", FactType: "image_generated", SubjectRef: "gen-a1", Binding: json.RawMessage(`{"photoAssetId":"photo-a"}`)}

	tests := []struct {
		name        string
		req         domain.FactRequirement
		arguments   json.RawMessage
		candidates  []domain.ExecutionFact
		wantFactID  string
		wantFailure *RequirementFailure
	}{
		{
			name:       "newest satisfying candidate wins",
			req:        videoRequirement(),
			arguments:  videoArgs("gen-a1", "photo-a"),
			candidates: []domain.ExecutionFact{generated, reviewFact("fact-new", "gen-a1", "photo-a", "v2", true), reviewFact("fact-old", "gen-a1", "photo-a", "v1", true)},
			wantFactID: "fact-new",
		},
		{
			name:       "older satisfying candidate behind a newer mismatch",
			req:        videoRequirement(),
			arguments:  videoArgs("gen-a1", "photo-a"),
			candidates: []domain.ExecutionFact{reviewFact("fact-new", "gen-a1", "photo-a", "v2", false), reviewFact("fact-old", "gen-a1", "photo-a", "v1", true)},
			wantFactID: "fact-old",
		},
		{
			name:        "photo A's fact does not satisfy a request about photo B's asset",
			req:         videoRequirement(),
			arguments:   videoArgs("gen-b1", "photo-b"),
			candidates:  []domain.ExecutionFact{reviewFact("fact-a", "gen-a1", "photo-a", "v1", true)},
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Subject: "gen-b1", Reason: RequirementNoFact},
		},
		{
			name:        "binding mismatch names the binding",
			req:         videoRequirement(),
			arguments:   videoArgs("gen-a1", "photo-b"),
			candidates:  []domain.ExecutionFact{reviewFact("fact-a", "gen-a1", "photo-a", "v1", true)},
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Subject: "gen-a1", Reason: RequirementBindingMismatch, Binding: "photoAssetId"},
		},
		{
			name:        "verdict false is rejected",
			req:         videoRequirement(),
			arguments:   videoArgs("gen-a1", "photo-a"),
			candidates:  []domain.ExecutionFact{reviewFact("fact-a", "gen-a1", "photo-a", "v1", false)},
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Subject: "gen-a1", Reason: RequirementVerdictMismatch},
		},
		{
			name:        "fact without a verdict does not satisfy a required verdict",
			req:         domain.FactRequirement{FactType: "image_generated", SubjectArgument: "/assetRef", RequireVerdict: boolPtr(true)},
			arguments:   videoArgs("gen-a1", "photo-a"),
			candidates:  []domain.ExecutionFact{generated},
			wantFailure: &RequirementFailure{FactType: "image_generated", Subject: "gen-a1", Reason: RequirementVerdictMismatch},
		},
		{
			name:       "requirement without bindings or verdict",
			req:        domain.FactRequirement{FactType: "image_generated", SubjectArgument: "/assetRef"},
			arguments:  json.RawMessage(`{"assetRef":"gen-a1"}`),
			candidates: []domain.ExecutionFact{reviewFact("fact-r", "gen-a1", "photo-a", "v1", true), generated},
			wantFactID: "fact-gen",
		},
		{
			name:        "failure describes the newest same-subject candidate",
			req:         videoRequirement(),
			arguments:   videoArgs("gen-a1", "photo-a"),
			candidates:  []domain.ExecutionFact{reviewFact("fact-new", "gen-a1", "photo-a", "v2", false), reviewFact("fact-old", "gen-a1", "photo-z", "v1", true)},
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Subject: "gen-a1", Reason: RequirementVerdictMismatch},
		},
		{
			name:        "empty candidates",
			req:         videoRequirement(),
			arguments:   videoArgs("gen-a1", "photo-a"),
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Subject: "gen-a1", Reason: RequirementNoFact},
		},
		{
			name:        "missing subject argument",
			req:         videoRequirement(),
			arguments:   json.RawMessage(`{"photoAssetId":"photo-a","callbackUrl":"` + signedURL + `"}`),
			candidates:  []domain.ExecutionFact{reviewFact("fact-a", "gen-a1", "photo-a", "v1", true)},
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Reason: RequirementArgumentMissing},
		},
		{
			name:        "non-string subject argument",
			req:         videoRequirement(),
			arguments:   json.RawMessage(`{"assetRef":{"url":"` + signedURL + `"},"photoAssetId":"photo-a"}`),
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Reason: RequirementArgumentMissing},
		},
		{
			name:        "missing binding argument",
			req:         videoRequirement(),
			arguments:   json.RawMessage(`{"assetRef":"gen-a1","callbackUrl":"` + signedURL + `"}`),
			candidates:  []domain.ExecutionFact{reviewFact("fact-a", "gen-a1", "photo-a", "v1", true)},
			wantFailure: &RequirementFailure{FactType: "asset_reviewed", Subject: "gen-a1", Reason: RequirementArgumentMissing, Binding: "photoAssetId"},
		},
		{
			name:      "non-string binding argument compares by canonical text",
			req:       domain.FactRequirement{FactType: "measured", SubjectArgument: "/id", MatchBindings: []string{"count"}},
			arguments: json.RawMessage(`{"id":"x-1","count":12}`),
			candidates: []domain.ExecutionFact{
				{ID: "fact-m", FactType: "measured", SubjectRef: "x-1", Binding: json.RawMessage(`{"count":"12"}`)},
			},
			wantFactID: "fact-m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, failure := MatchRequirement(tt.req, tt.arguments, tt.candidates)
			if tt.wantFailure != nil {
				if failure == nil {
					t.Fatalf("MatchRequirement() matched %q, want failure %+v", match.Fact.ID, *tt.wantFailure)
				}
				if *failure != *tt.wantFailure {
					t.Fatalf("MatchRequirement() failure = %+v, want %+v", *failure, *tt.wantFailure)
				}
				return
			}
			if failure != nil {
				t.Fatalf("MatchRequirement() failure = %+v, want match %q", *failure, tt.wantFactID)
			}
			if match.Fact.ID != tt.wantFactID {
				t.Fatalf("MatchRequirement() matched %q, want %q", match.Fact.ID, tt.wantFactID)
			}
		})
	}
}

func TestRequirementFailure_ExecutionErrorCarriesNoArgumentValues(t *testing.T) {
	arguments := json.RawMessage(`{"assetRef":"gen-a1","photoAssetId":"photo-secret-b","callbackUrl":"` + signedURL + `"}`)
	candidates := []domain.ExecutionFact{reviewFact("fact-a", "gen-a1", "photo-a", "v1", true)}

	_, failure := MatchRequirement(videoRequirement(), arguments, candidates)
	if failure == nil {
		t.Fatal("MatchRequirement() failure = nil, want a binding mismatch")
	}
	execErr := failure.ExecutionError()
	if execErr.Code != CodePreconditionUnmet {
		t.Fatalf("Code = %q, want %q", execErr.Code, CodePreconditionUnmet)
	}
	var details map[string]string
	if err := json.Unmarshal(execErr.Details, &details); err != nil {
		t.Fatalf("Details is not a string map: %v", err)
	}
	want := map[string]string{"factType": "asset_reviewed", "subject": "gen-a1", "reason": "BINDING_MISMATCH", "binding": "photoAssetId"}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("Details = %v, want %v", details, want)
	}
	rendered := execErr.Message + string(execErr.Details)
	assertNoArgumentValues(t, rendered)
	if strings.Contains(rendered, "photo-secret-b") || strings.Contains(rendered, "photo-a") {
		t.Fatalf("rendered error %q echoes a binding value", rendered)
	}
}

func TestGenerationLimitReached(t *testing.T) {
	limit := func(n int) *int { return &n }
	tests := []struct {
		name     string
		limit    *int
		attempts int
		want     bool
	}{
		{name: "nil limit is unlimited", limit: nil, attempts: 1000, want: false},
		{name: "no attempts yet", limit: limit(1), attempts: 0, want: false},
		{name: "one below the limit", limit: limit(12), attempts: 11, want: false},
		{name: "at the limit", limit: limit(12), attempts: 12, want: true},
		{name: "above the limit", limit: limit(2), attempts: 3, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GenerationLimitReached(tt.limit, tt.attempts); got != tt.want {
				t.Fatalf("GenerationLimitReached() = %v, want %v", got, tt.want)
			}
		})
	}
}

func assertNoArgumentValues(t *testing.T, rendered string) {
	t.Helper()
	for _, secret := range []string{signedURL, "s3cr3t-sig", "cdn.example.test"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("rendered error %q echoes an argument or result value", rendered)
		}
	}
}
