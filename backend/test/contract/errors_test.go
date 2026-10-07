//go:build integration

package contract

import (
	"net/http"
	"testing"
)

// TestAPI_ErrorEnvelope_UnknownRoute_MatchesSpec covers the router's own fallback
// handlers (router.go's r.NotFound/r.MethodNotAllowed), not a service/domain error path:
// both must answer the same {"error": {"code", "message"}} envelope shape the error
// contract defines for every other error response.
//
// Note: the error contract's own code list (example codes) is illustrative, not
// exhaustive or closed; NOT_FOUND and METHOD_NOT_ALLOWED are this router's own additions
// for routes the spec's endpoint table never claims to cover (an unknown path, or a known
// path with the wrong method), and are kept exactly as router.go already defines them.
func TestAPI_ErrorEnvelope_UnknownRoute_MatchesSpec(t *testing.T) {
	env := newTestEnv(t)

	t.Run("unknown route", func(t *testing.T) {
		resp, body := env.doJSON(t, http.MethodGet, "/api/does-not-exist", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /api/does-not-exist status = %d, want %d, body=%s", resp.StatusCode, http.StatusNotFound, body)
		}
		assertErrorEnvelopeShape(t, body, "NOT_FOUND")
	})

	t.Run("known route, wrong method", func(t *testing.T) {
		// POST /api/runs/{runId} is not registered; only GET is (router.go's
		// r.Get("/{runId}", ...)), so this must hit MethodNotAllowed, not NotFound.
		resp, body := env.doJSON(t, http.MethodPost, "/api/runs/does-not-matter", nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST /api/runs/{runId} status = %d, want %d, body=%s", resp.StatusCode, http.StatusMethodNotAllowed, body)
		}
		assertErrorEnvelopeShape(t, body, "METHOD_NOT_ALLOWED")
	})
}

// assertErrorEnvelopeShape asserts the full {"error": {"code","message"}} envelope shape
// the error contract defines: code matches wantCode exactly, and message is
// present and non-empty (its exact wording is not part of the contract).
func assertErrorEnvelopeShape(t *testing.T, body []byte, wantCode string) {
	t.Helper()
	env := decodeBody[map[string]any](t, body)
	errBody, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("response is not an {\"error\": {...}} envelope: %s", body)
	}
	if code, _ := errBody["code"].(string); code != wantCode {
		t.Errorf("error.code = %q, want %q: %s", code, wantCode, body)
	}
	if msg, _ := errBody["message"].(string); msg == "" {
		t.Errorf("error.message is empty, want a non-empty message: %s", body)
	}
}
