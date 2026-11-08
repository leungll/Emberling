package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/service"
)

// projectMedia renders a media list through the whole Agent Trace response and returns
// the served `media` member, so a case sees exactly what the route serves.
func projectMedia(t *testing.T, media service.AgentTraceMedia, bound int) map[string]any {
	t.Helper()
	body, err := json.Marshal(toAgentTraceResponse(service.AgentTrace{Media: media}, bound))
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

func mediaItem(ref, url, mediaType string) service.AgentTraceMediaItem {
	return service.AgentTraceMediaItem{Ref: ref, URL: url, MediaType: mediaType, ToolAttemptID: "ta_1"}
}

func TestAgentTraceMedia_EmptyList_ServesItemsAndTruncatedKeys(t *testing.T) {
	got := projectMedia(t, service.AgentTraceMedia{}, toolViewFieldBound)
	want := map[string]any{"items": []any{}, "truncated": false}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("media (-want +got):\n%s", diff)
	}
}

func TestAgentTraceMedia_ServableURLs_ProjectedAsGiven(t *testing.T) {
	media := service.AgentTraceMedia{Items: []service.AgentTraceMediaItem{
		mediaItem("artifact_"+strings.Repeat("a", 32), "/api/runs/run_1/artifacts/artifact_x/content", "image/png"),
		mediaItem("https://media.example/v1/videos/vid_1.mp4", "https://media.example/v1/videos/vid_1.mp4", "video/mp4"),
	}, Truncated: true}
	got := projectMedia(t, media, toolViewFieldBound)
	want := map[string]any{
		"items": []any{
			map[string]any{"ref": "artifact_" + strings.Repeat("a", 32), "url": "/api/runs/run_1/artifacts/artifact_x/content", "mediaType": "image/png", "toolAttemptId": "ta_1"},
			map[string]any{"ref": "https://media.example/v1/videos/vid_1.mp4", "url": "https://media.example/v1/videos/vid_1.mp4", "mediaType": "video/mp4", "toolAttemptId": "ta_1"},
		},
		"truncated": true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("media (-want +got):\n%s", diff)
	}
}

// TestAgentTraceMedia_UnsafeURL_WithheldButRefKept proves every URL shape that may carry a
// credential, or point outside the Backend's own routes, is withheld while the reference
// stays visible as text, so Studio can still name the media it cannot load.
func TestAgentTraceMedia_UnsafeURL_WithheldButRefKept(t *testing.T) {
	for _, unsafe := range []string{
		"https://user:pw@media.example/v.mp4",
		"https://media.example/v.mp4?sig=abc",
		"https://media.example/v.mp4#frag",
		"ftp://media.example/v.mp4",
		"//media.example/v.mp4",
		"/assets/asset_1/content",
		"/api/assets/asset_1/content?token=x",
		"/api/assets/asset_1/content#x",
		"api/assets/asset_1/content",
		"javascript:alert(1)",
	} {
		got := projectMedia(t, service.AgentTraceMedia{Items: []service.AgentTraceMediaItem{mediaItem("ref-1", unsafe, "video/mp4")}}, toolViewFieldBound)
		items, _ := got["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("%q: items = %v, want one", unsafe, got["items"])
		}
		want := map[string]any{"ref": "ref-1", "url": nil, "mediaType": "video/mp4", "toolAttemptId": "ta_1", "withheld": "UNSAFE_URL"}
		if diff := cmp.Diff(want, items[0]); diff != "" {
			t.Errorf("%q (-want +got):\n%s", unsafe, diff)
		}
		if strings.Contains(mustJSON(t, got), unsafe) {
			t.Errorf("%q: the withheld URL still appears in %s", unsafe, mustJSON(t, got))
		}
	}
}

// TestAgentTraceMedia_OverBound_WithheldNeverCut proves an oversized URL withholds the URL
// alone, while an oversized reference or media type withholds the whole item; nothing is
// truncated to fit.
func TestAgentTraceMedia_OverBound_WithheldNeverCut(t *testing.T) {
	const bound = 64
	long := strings.Repeat("x", bound)
	cases := map[string]struct {
		item service.AgentTraceMediaItem
		want map[string]any
	}{
		"url over the bound": {
			item: mediaItem("ref-1", "https://media.example/"+long, "video/mp4"),
			want: map[string]any{"ref": "ref-1", "url": nil, "mediaType": "video/mp4", "toolAttemptId": "ta_1", "withheld": "TOO_LARGE"},
		},
		"ref over the bound": {
			item: mediaItem(long, "https://media.example/v.mp4", "video/mp4"),
			want: map[string]any{"ref": nil, "url": nil, "mediaType": nil, "toolAttemptId": "ta_1", "withheld": "TOO_LARGE"},
		},
		"media type over the bound": {
			item: mediaItem("ref-1", "https://media.example/v.mp4", "video/"+long),
			want: map[string]any{"ref": nil, "url": nil, "mediaType": nil, "toolAttemptId": "ta_1", "withheld": "TOO_LARGE"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := projectMedia(t, service.AgentTraceMedia{Items: []service.AgentTraceMediaItem{tc.item}}, bound)
			items, _ := got["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("items = %v, want one", got["items"])
			}
			if diff := cmp.Diff(tc.want, items[0]); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
			if strings.Contains(mustJSON(t, got), long) {
				t.Errorf("the oversized value still appears in %s", mustJSON(t, got))
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}
