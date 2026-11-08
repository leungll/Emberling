package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/service"
)

// projectRunMedia renders a Run media list through the Snapshot response and returns the
// served `media` member, so a case sees exactly what the route serves.
func projectRunMedia(t *testing.T, media service.RunMedia, bound int) map[string]any {
	t.Helper()
	body, err := json.Marshal(runSnapshotResponse{Media: toRunMediaDTO(media, bound)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Media map[string]any `json:"media"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if decoded.Media == nil {
		t.Fatalf("response %s has no media member", body)
	}
	return decoded.Media
}

func runMediaItem(ref, url, mediaType string) service.RunMediaItem {
	return service.RunMediaItem{Ref: ref, URL: url, MediaType: mediaType, NodeRunID: "nr_1"}
}

func TestRunMedia_EmptyList_ServesItemsAndTruncatedKeys(t *testing.T) {
	got := projectRunMedia(t, service.RunMedia{}, toolViewFieldBound)
	want := map[string]any{"items": []any{}, "truncated": false}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("media (-want +got):\n%s", diff)
	}
}

func TestRunMedia_Members_ServedOrWithheldLikeTheTraceList(t *testing.T) {
	const source = "https://media.example/v1/sources/src_1/content"
	media := service.RunMedia{Items: []service.RunMediaItem{
		runMediaItem("src_1", source, "video/webm"),
		runMediaItem("src_2", "https://user:secret@media.example/v1/sources/src_2/content", "video/webm"),
		runMediaItem("src_3", "https://media.example/v1/sources/src_3/content?token=SECRET", "video/webm"),
		runMediaItem("src_4", "https://media.example/"+strings.Repeat("x", toolViewFieldBound), "video/webm"),
		runMediaItem(strings.Repeat("r", toolViewFieldBound), source, "video/webm"),
	}, Truncated: true}
	got := projectRunMedia(t, media, toolViewFieldBound)
	want := map[string]any{
		"items": []any{
			map[string]any{"ref": "src_1", "url": source, "mediaType": "video/webm", "nodeRunId": "nr_1"},
			map[string]any{"ref": "src_2", "url": nil, "mediaType": "video/webm", "nodeRunId": "nr_1", "withheld": "UNSAFE_URL"},
			map[string]any{"ref": "src_3", "url": nil, "mediaType": "video/webm", "nodeRunId": "nr_1", "withheld": "UNSAFE_URL"},
			map[string]any{"ref": "src_4", "url": nil, "mediaType": "video/webm", "nodeRunId": "nr_1", "withheld": "TOO_LARGE"},
			map[string]any{"ref": nil, "url": nil, "mediaType": nil, "nodeRunId": "nr_1", "withheld": "TOO_LARGE"},
		},
		"truncated": true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("media (-want +got):\n%s", diff)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "SECRET") {
		t.Errorf("served media carries a credential: %s", raw)
	}
}
