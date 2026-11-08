//go:build integration

package contract

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// TestAPI_NodeTypesCatalog_MediaLinks_SerialisedAndOmittedWhenEmpty: a Node Type that
// declares media links serves them in the catalogue in the same shape a Tool's links
// have plus the port they read, so Studio understands the Run's media list by this
// declaration and never by node type; a Node Type that declares none omits the key.
func TestAPI_NodeTypesCatalog_MediaLinks_SerialisedAndOmittedWhenEmpty(t *testing.T) {
	env, _ := newTranscriptEnv(t)

	resp, body := env.doJSON(t, http.MethodGet, "/api/node-types", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/node-types status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "type")

	transcript, ok := items["media_transcript"]
	if !ok {
		t.Fatalf("GET /api/node-types does not list media_transcript: %s", body)
	}
	const wantLinks = `[{"port":"transcript","refPointer":"/sourceId","urlPointer":"/sourceUrl","mediaTypePointer":"/sourceMediaType"}]`
	var compact bytes.Buffer
	if err := json.Compact(&compact, transcript["mediaLinks"]); err != nil {
		t.Fatalf("media_transcript.mediaLinks = %s, want a JSON array: %v", transcript["mediaLinks"], err)
	}
	if compact.String() != wantLinks {
		t.Errorf("media_transcript.mediaLinks = %s, want %s", transcript["mediaLinks"], wantLinks)
	}

	textInput, ok := items["text_input"]
	if !ok {
		t.Fatalf("GET /api/node-types does not list text_input: %s", body)
	}
	if raw, present := textInput["mediaLinks"]; present {
		t.Errorf("text_input.mediaLinks = %s, want the key absent for a Node Type that declares none", raw)
	}
}
