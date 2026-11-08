package registry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// displayDeclaringRegistration is a Tool whose schemas declare a nested result object, a
// scalar result member and one argument, so pointer resolution can be exercised at every
// depth the built-in Tools use.
func displayDeclaringRegistration() ToolRegistration {
	reg := validSyncToolRegistration()
	reg.Metadata.Name = "render_clip"
	reg.Metadata.InputSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"timelineSha256": {"type": "string"},
			"settings": {"type": "object", "properties": {"prompt": {"type": "string"}}, "additionalProperties": false}
		},
		"additionalProperties": false
	}`)
	reg.Metadata.OutputSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"clipSha256": {"type": "string"},
			"clipUrl": {"type": "string"},
			"durationMs": {"type": "integer"},
			"artifact": {"type": "object", "properties": {"mediaType": {"type": "string"}}, "additionalProperties": false}
		},
		"additionalProperties": false
	}`)
	return reg
}

func TestToolRegistry_Register_AcceptsDisplayPointersInsideSchemas(t *testing.T) {
	reg := displayDeclaringRegistration()
	reg.Metadata.MediaLinks = []domain.ToolMediaLink{
		{RefPointer: "/clipSha256", URLPointer: "/clipUrl", MediaTypePointer: "/artifact/mediaType"},
	}
	reg.Metadata.Display = []domain.ToolDisplayField{
		{Source: domain.FactPointerArguments, Pointer: "/settings/prompt", Title: "Prompt"},
		{Source: domain.FactPointerResult, Pointer: "/durationMs", Title: "Duration (ms)"},
	}
	if err := NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("Register() = %v, want nil", err)
	}
}

func TestToolRegistry_Register_RejectsDisplayPointersOutsideSchemas(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.ToolMetadata)
		want   string
	}{
		{
			name: "media ref pointer names an undeclared result member",
			mutate: func(m *domain.ToolMetadata) {
				m.MediaLinks = []domain.ToolMediaLink{{RefPointer: "/clipDigest", URLPointer: "/clipUrl", MediaType: "video/mp4"}}
			},
			want: `mediaLinks[0].refPointer: pointer "/clipDigest" is outside the outputSchema`,
		},
		{
			name: "media url pointer names an undeclared result member",
			mutate: func(m *domain.ToolMetadata) {
				m.MediaLinks = []domain.ToolMediaLink{{RefPointer: "/clipSha256", URLPointer: "/url", MediaType: "video/mp4"}}
			},
			want: `mediaLinks[0].urlPointer: pointer "/url" is outside the outputSchema`,
		},
		{
			name: "media type pointer names an undeclared nested member",
			mutate: func(m *domain.ToolMetadata) {
				m.MediaLinks = []domain.ToolMediaLink{{RefPointer: "/clipSha256", URLPointer: "/clipUrl", MediaTypePointer: "/artifact/kind"}}
			},
			want: `mediaLinks[0].mediaTypePointer: pointer "/artifact/kind" is outside the outputSchema`,
		},
		{
			name: "display argument pointer names an undeclared input member",
			mutate: func(m *domain.ToolMetadata) {
				m.Display = []domain.ToolDisplayField{{Source: domain.FactPointerArguments, Pointer: "/settings/seed", Title: "Seed"}}
			},
			want: `display[0]: pointer "/settings/seed" is outside the inputSchema`,
		},
		{
			name: "display result pointer names an input member",
			mutate: func(m *domain.ToolMetadata) {
				m.Display = []domain.ToolDisplayField{{Source: domain.FactPointerResult, Pointer: "/timelineSha256", Title: "Timeline"}}
			},
			want: `display[0]: pointer "/timelineSha256" is outside the outputSchema`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := displayDeclaringRegistration()
			tc.mutate(&reg.Metadata)
			err := NewToolRegistry().Register(reg)
			if err == nil {
				t.Fatalf("Register() = nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Register() = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestToolRegistry_Register_DisplayShapeErrorsSurfaceAtStartup proves the shape rules
// the declaration types carry (a missing title, a media link naming both or neither media
// type source) fail registration, not only the pointer-resolution rules.
func TestToolRegistry_Register_DisplayShapeErrorsSurfaceAtStartup(t *testing.T) {
	reg := displayDeclaringRegistration()
	reg.Metadata.MediaLinks = []domain.ToolMediaLink{{RefPointer: "/clipSha256", URLPointer: "/clipUrl"}}
	reg.Metadata.Display = []domain.ToolDisplayField{{Source: domain.FactPointerResult, Pointer: "/durationMs"}}
	err := NewToolRegistry().Register(reg)
	if err == nil {
		t.Fatal("Register() = nil, want shape errors")
	}
	for _, want := range []string{"mediaLinks[0]: exactly one of mediaTypePointer or mediaType", "display[0].title is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Register() = %v, want it to mention %q", err, want)
		}
	}
}
