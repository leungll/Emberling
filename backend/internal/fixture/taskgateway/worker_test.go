package taskgateway

import (
	"net/http"
	"strings"
	"testing"
)

// failedPayloadFor dispatches one task with reference and returns its FAILED payload.
func failedPayloadFor(t *testing.T, f *gatewayFixture, reference any) *errorWire {
	t.Helper()
	status, raw := f.post(t, f.taskBody("", reference))
	if status != http.StatusAccepted {
		t.Fatalf("POST = %d %s", status, raw)
	}
	_, payload, rawPayload := decodedCallback(t, f.callbacks.next(t).body)
	if payload.Status != statusFailed || payload.Error == nil || payload.Image != nil {
		t.Fatalf("payload = %s, want FAILED with an error and no image", rawPayload)
	}
	if calls := f.generator.calls.Load(); calls != 0 {
		t.Fatalf("upstream calls = %d, want none when the reference cannot be used", calls)
	}
	return payload.Error
}

func TestWorker_AssetReferenceNotFound_FailsTaskWithReason(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	ref := f.references.assetRef(f.references.content)
	ref["asset"].(map[string]any)["assetId"] = "ast_missing"
	got := failedPayloadFor(t, f, ref)
	if got.Code != codeReferenceNotFound || got.Message != "reference asset ast_missing was not found" {
		t.Fatalf("error = %+v, want %s naming the asset", got, codeReferenceNotFound)
	}
}

func TestWorker_ReferenceDigestMismatch_FailsTaskWithReason(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	ref := f.references.assetRef(f.references.content)
	ref["asset"].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	got := failedPayloadFor(t, f, ref)
	if got.Code != codeReferenceInvalid || !strings.Contains(got.Message, "digest") {
		t.Fatalf("error = %+v, want %s about the digest", got, codeReferenceInvalid)
	}
}

func TestWorker_ReferenceSizeMismatch_FailsTaskWithReason(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	ref := f.references.assetRef(f.references.content)
	ref["asset"].(map[string]any)["sizeBytes"] = 1
	got := failedPayloadFor(t, f, ref)
	if got.Code != codeReferenceInvalid || !strings.Contains(got.Message, "size") {
		t.Fatalf("error = %+v, want %s about the size", got, codeReferenceInvalid)
	}
}

func TestWorker_ExternalReferenceDownloadFails_FailsTaskWithReason(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	got := failedPayloadFor(t, f, map[string]any{"source": "EXTERNAL", "uri": f.references.server.URL + "/broken", "mediaType": "image/png"})
	if got.Code != codeReferenceFetchFailed || got.Message != "reference fetch returned HTTP 502" {
		t.Fatalf("error = %+v, want %s with the HTTP status", got, codeReferenceFetchFailed)
	}
}

func TestWorker_ExternalReference_SentWithoutHeaders(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	status, raw := f.post(t, f.taskBody("", map[string]any{"source": "EXTERNAL", "uri": f.references.server.URL + "/external/photo.png", "mediaType": "image/png"}))
	if status != http.StatusAccepted {
		t.Fatalf("POST = %d %s", status, raw)
	}
	f.callbacks.next(t)
	headers := <-f.references.headers
	for _, name := range []string{"Authorization", "Cookie", callbackHeader} {
		if headers.Get(name) != "" {
			t.Fatalf("reference request carried %s", name)
		}
	}
}

func TestWorker_UpstreamFailure_FailsTaskWithFixedLabel(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{generator: &fakeGenerator{err: errUpstream}})
	status, raw := f.post(t, f.taskBody("", f.references.assetRef(f.references.content)))
	if status != http.StatusAccepted {
		t.Fatalf("POST = %d %s", status, raw)
	}
	_, payload, rawPayload := decodedCallback(t, f.callbacks.next(t).body)
	if payload.Status != statusFailed || payload.Error == nil || payload.Error.Code != codeProviderTaskFailed || payload.Error.Message != errUpstream.Error() {
		t.Fatalf("payload = %s, want %s with the upstream label", rawPayload, codeProviderTaskFailed)
	}
	if calls := f.generator.calls.Load(); calls != 1 {
		t.Fatalf("upstream calls = %d, want exactly one: the gateway never retries upstream", calls)
	}
}
