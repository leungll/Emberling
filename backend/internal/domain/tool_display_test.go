package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func displayTestToolMetadata() ToolMetadata {
	return ToolMetadata{
		Name:          "render",
		Description:   "Render a clip",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		OutputSchema:  json.RawMessage(`{"type":"object"}`),
		SideEffect:    SideEffectPolicy{Kind: SideEffectExternal, Idempotency: IdempotencySafe},
		ExecutionKind: ToolExecutionSync,
	}
}

func TestToolMetadata_Validate_AcceptsDisplayDeclarations(t *testing.T) {
	m := displayTestToolMetadata()
	m.MediaLinks = []MediaLink{
		{RefPointer: "/clipSha256", URLPointer: "/clipUrl", MediaTypePointer: "/mediaType"},
		{RefPointer: "/posterRef", URLPointer: "/posterUrl", MediaType: "image/png"},
	}
	m.Display = []ToolDisplayField{
		{Source: FactPointerArguments, Pointer: "/timelineSha256", Title: "Timeline"},
		{Source: FactPointerResult, Pointer: "/durationMs", Title: "Duration (ms)"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestToolMetadata_Validate_RejectsInvalidDisplayDeclarations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ToolMetadata)
		want   string
	}{
		{
			name:   "media link without ref pointer",
			mutate: func(m *ToolMetadata) { m.MediaLinks = []MediaLink{{URLPointer: "/u", MediaType: "video/mp4"}} },
			want:   "mediaLinks[0].refPointer",
		},
		{
			name:   "media link without url pointer",
			mutate: func(m *ToolMetadata) { m.MediaLinks = []MediaLink{{RefPointer: "/r", MediaType: "video/mp4"}} },
			want:   "mediaLinks[0].urlPointer",
		},
		{
			name: "media link pointer without leading slash",
			mutate: func(m *ToolMetadata) {
				m.MediaLinks = []MediaLink{{RefPointer: "r", URLPointer: "/u", MediaType: "video/mp4"}}
			},
			want: "mediaLinks[0].refPointer",
		},
		{
			name:   "media link with neither media type pointer nor literal",
			mutate: func(m *ToolMetadata) { m.MediaLinks = []MediaLink{{RefPointer: "/r", URLPointer: "/u"}} },
			want:   "exactly one of mediaTypePointer or mediaType",
		},
		{
			name: "media link with both media type pointer and literal",
			mutate: func(m *ToolMetadata) {
				m.MediaLinks = []MediaLink{{RefPointer: "/r", URLPointer: "/u", MediaTypePointer: "/t", MediaType: "video/mp4"}}
			},
			want: "exactly one of mediaTypePointer or mediaType",
		},
		{
			name: "media link literal that is not a media type",
			mutate: func(m *ToolMetadata) {
				m.MediaLinks = []MediaLink{{RefPointer: "/r", URLPointer: "/u", MediaType: "video"}}
			},
			want: "mediaLinks[0].mediaType",
		},
		{
			name:   "display field without title",
			mutate: func(m *ToolMetadata) { m.Display = []ToolDisplayField{{Source: FactPointerResult, Pointer: "/passed"}} },
			want:   "display[0].title is empty",
		},
		{
			name: "display field with a multi-line title",
			mutate: func(m *ToolMetadata) {
				m.Display = []ToolDisplayField{{Source: FactPointerResult, Pointer: "/passed", Title: "Pass\ned"}}
			},
			want: "display[0].title",
		},
		{
			name: "display field with an over-long title",
			mutate: func(m *ToolMetadata) {
				m.Display = []ToolDisplayField{{Source: FactPointerResult, Pointer: "/passed", Title: strings.Repeat("x", MaxDisplayTitleLength+1)}}
			},
			want: "display[0].title",
		},
		{
			name: "display field with an unknown source",
			mutate: func(m *ToolMetadata) {
				m.Display = []ToolDisplayField{{Source: "STATE", Pointer: "/passed", Title: "Passed"}}
			},
			want: "display[0]: source",
		},
		{
			name:   "display field with an empty pointer",
			mutate: func(m *ToolMetadata) { m.Display = []ToolDisplayField{{Source: FactPointerResult, Title: "Passed"}} },
			want:   "display[0]",
		},
		{
			name: "display field declared twice",
			mutate: func(m *ToolMetadata) {
				m.Display = []ToolDisplayField{
					{Source: FactPointerResult, Pointer: "/passed", Title: "Passed"},
					{Source: FactPointerResult, Pointer: "/passed", Title: "Verdict"},
				}
			},
			want: "display[1]: duplicate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := displayTestToolMetadata()
			tc.mutate(&m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLookupJSONPointer_WalksObjectsAndArrays(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{"artifact":{"mediaType":"image/png"},"items":[{"id":"a"},{"id":"b"}],"n":null}`), &doc); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		pointer string
		want    any
		found   bool
	}{
		{"/artifact/mediaType", "image/png", true},
		{"/items/1/id", "b", true},
		{"/items/01/id", nil, false},
		{"/items/2", nil, false},
		{"/missing", nil, false},
		{"/n", nil, true},
		{"/artifact/mediaType/x", nil, false},
	}
	for _, tc := range cases {
		got, found := LookupJSONPointer(doc, tc.pointer)
		if found != tc.found || got != tc.want {
			t.Errorf("LookupJSONPointer(%q) = (%v, %v), want (%v, %v)", tc.pointer, got, found, tc.want, tc.found)
		}
	}
}

func mediaLinkTestNodeMetadata() NodeMetadata {
	return NodeMetadata{
		Type:          "media_transcript",
		DisplayName:   "Media Transcript",
		Category:      NodeCategoryPromptAndModel,
		ExecutionKind: NodeExecutionSync,
		Outputs:       []PortMetadata{{Name: "transcript", DataType: PortTypeJSON, Required: true}},
		ConfigSchema:  json.RawMessage(`{"type":"object"}`),
		SideEffect:    SideEffectPolicy{Kind: SideEffectNone, Idempotency: IdempotencySafe},
	}
}

func TestNodeMetadata_Validate_AcceptsMediaLinkOnDeclaredOutputPort(t *testing.T) {
	m := mediaLinkTestNodeMetadata()
	m.MediaLinks = []MediaLink{
		{Port: "transcript", RefPointer: "/sourceId", URLPointer: "/sourceUrl", MediaTypePointer: "/sourceMediaType"},
		{Port: "transcript", RefPointer: "/posterRef", URLPointer: "/posterUrl", MediaType: "image/png"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestNodeMetadata_Validate_RejectsInvalidMediaLinks(t *testing.T) {
	cases := []struct {
		name string
		link MediaLink
		want string
	}{
		{
			name: "no port",
			link: MediaLink{RefPointer: "/r", URLPointer: "/u", MediaType: "video/mp4"},
			want: "mediaLinks[0].port is empty",
		},
		{
			name: "port that is not a declared output",
			link: MediaLink{Port: "task", RefPointer: "/r", URLPointer: "/u", MediaType: "video/mp4"},
			want: `mediaLinks[0].port "task" is not a declared output port`,
		},
		{
			name: "pointer without leading slash",
			link: MediaLink{Port: "transcript", RefPointer: "r", URLPointer: "/u", MediaType: "video/mp4"},
			want: "mediaLinks[0].refPointer",
		},
		{
			name: "both media type pointer and literal",
			link: MediaLink{Port: "transcript", RefPointer: "/r", URLPointer: "/u", MediaTypePointer: "/t", MediaType: "video/mp4"},
			want: "exactly one of mediaTypePointer or mediaType",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mediaLinkTestNodeMetadata()
			m.MediaLinks = []MediaLink{tc.link}
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestToolMetadata_Validate_RejectsMediaLinkNamingAPort(t *testing.T) {
	m := displayTestToolMetadata()
	m.MediaLinks = []MediaLink{{Port: "out", RefPointer: "/r", URLPointer: "/u", MediaType: "video/mp4"}}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "mediaLinks[0].port must be empty") {
		t.Fatalf("Validate() = %v, want the port rejected on a Tool link", err)
	}
}
