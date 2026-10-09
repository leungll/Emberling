package mediabrief

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

func photoJSON(index int) string {
	return fmt.Sprintf(`{"assetId":"asset_%02d","mediaType":"image/jpeg","sizeBytes":%d,"sha256":"%064x"}`, index, 1000+index, index)
}

func photoSetJSON(count int) string {
	items := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		items = append(items, photoJSON(i))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func briefInput(runInput string) registry.NodeInput {
	return registry.NodeInput{
		RunInput: json.RawMessage(runInput),
		Ports:    map[string]json.RawMessage{briefPort: json.RawMessage(`"Warm <evening> light & soft focus"`)},
	}
}

var testConfig = map[string]any{"inputKey": "photos"}

func TestRegistration_Validate_Passes(t *testing.T) {
	if err := registry.NewNodeRegistry().Register(Registration()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestMediaBriefExecutor_Execute_PhotoSetAndBriefProduceCanonicalDocument(t *testing.T) {
	runInput := `{"photos":[` + photoJSON(2) + `,` + photoJSON(1) + `]}`

	result, err := Executor{}.Execute(context.Background(), briefInput(runInput), testConfig)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if result.Kind != registry.NodeResultCompleted {
		t.Fatalf("Execute().Kind = %v, want COMPLETED", result.Kind)
	}
	var text string
	if err := json.Unmarshal(result.Output.Ports[textPort], &text); err != nil {
		t.Fatalf("the `text` port is not a JSON string: %v (port=%s)", err, result.Output.Ports[textPort])
	}
	// Photo order follows the Run input; keys are sorted and nothing is HTML-escaped.
	want := `{"brief":"Warm <evening> light & soft focus","photos":[` +
		`{"mediaType":"image/jpeg","photoAssetId":"asset_02","sha256":"` + fmt.Sprintf("%064x", 2) + `","sizeBytes":1002},` +
		`{"mediaType":"image/jpeg","photoAssetId":"asset_01","sha256":"` + fmt.Sprintf("%064x", 1) + `","sizeBytes":1001}]}`
	if text != want {
		t.Fatalf("text port =\n%s\nwant\n%s", text, want)
	}
}

func TestMediaBriefExecutor_Execute_RepeatedInputIsByteIdentical(t *testing.T) {
	runInput := `{"photos":` + photoSetJSON(3) + `}`
	first, err := Executor{}.Execute(context.Background(), briefInput(runInput), testConfig)
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	second, err := Executor{}.Execute(context.Background(), briefInput(runInput), testConfig)
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if string(first.Output.Ports[textPort]) != string(second.Output.Ports[textPort]) {
		t.Fatalf("repeated Execute() differs:\n%s\n%s", first.Output.Ports[textPort], second.Output.Ports[textPort])
	}
}

func TestMediaBriefExecutor_Execute_MaximumPhotoSetSucceeds(t *testing.T) {
	runInput := `{"photos":` + photoSetJSON(maxPhotos) + `}`
	if _, err := (Executor{}).Execute(context.Background(), briefInput(runInput), testConfig); err != nil {
		t.Fatalf("Execute() error = %v, want nil for %d photos", err, maxPhotos)
	}
}

func TestMediaBriefExecutor_Execute_InvalidInputFails(t *testing.T) {
	cases := []struct {
		name     string
		runInput string
		ports    map[string]json.RawMessage
		wantErr  string
	}{
		{name: "missing input key", runInput: `{}`, wantErr: `required run input key "photos" is missing`},
		{name: "null input key", runInput: `{"photos":null}`, wantErr: `required run input key "photos" is missing`},
		{name: "empty photo set", runInput: `{"photos":[]}`, wantErr: "photo set is empty"},
		{name: "oversized photo set", runInput: `{"photos":` + photoSetJSON(maxPhotos+1) + `}`, wantErr: "at most 12 are allowed"},
		{name: "not an array", runInput: `{"photos":` + photoJSON(1) + `}`, wantErr: "not an array of AssetRefs"},
		{name: "unknown AssetRef member", runInput: `{"photos":[{"assetId":"a","mediaType":"image/png","sizeBytes":1,"sha256":"x","storageKey":"k"}]}`, wantErr: "photo 0: value is not an AssetRef object"},
		{name: "unsupported media type", runInput: `{"photos":[{"assetId":"a","mediaType":"application/pdf","sizeBytes":1,"sha256":"x"}]}`, wantErr: "photo 0: media type is not a supported image type"},
		{name: "empty asset id", runInput: `{"photos":[{"assetId":"","mediaType":"image/png","sizeBytes":1,"sha256":"x"}]}`, wantErr: "photo 0: assetId is empty"},
		{name: "duplicate asset", runInput: `{"photos":[` + photoJSON(1) + `,` + photoJSON(1) + `]}`, wantErr: "photo 1: the same Asset appears more than once"},
		{name: "missing brief", runInput: `{"photos":` + photoSetJSON(1) + `}`, ports: map[string]json.RawMessage{}, wantErr: `required input port "brief" has no value`},
		{name: "brief not text", runInput: `{"photos":` + photoSetJSON(1) + `}`, ports: map[string]json.RawMessage{briefPort: json.RawMessage(`{"a":1}`)}, wantErr: `input port "brief" is not a JSON string`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := briefInput(tc.runInput)
			if tc.ports != nil {
				input.Ports = tc.ports
			}
			_, err := Executor{}.Execute(context.Background(), input, testConfig)
			if err == nil {
				t.Fatalf("Execute() error = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Execute() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestMediaBriefExecutor_Execute_ErrorDoesNotQuoteRunInputValues(t *testing.T) {
	runInput := `{"photos":[{"assetId":"secret-looking-id","mediaType":"text/secret-type","sizeBytes":1,"sha256":"x"}]}`
	_, err := Executor{}.Execute(context.Background(), briefInput(runInput), testConfig)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for an unsupported media type")
	}
	for _, leaked := range []string{"secret-looking-id", "text/secret-type"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Execute() error %q quotes the run input value %q", err, leaked)
		}
	}
}
