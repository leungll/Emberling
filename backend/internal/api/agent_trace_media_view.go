package api

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/leungll/Emberling/backend/internal/service"
)

// ---- Agent Trace: media the Tool calls produced ----
//
// The media list lets Studio show a generated image, clip or video next to the reference
// a final output or a fact names it by, without ever building a URL from that value: a
// reference resolves to a URL only when a Tool's registered media links say so and the
// SUCCEEDED result carried both. URLs are therefore taken from Tool results alone, never
// from Agent output, and each passes the same credential rules as every URL the Trace
// shows before it is served.

// agentTraceMediaDTO is the bounded media list. Truncated reports that the Agent Run
// produced more media than Items shows; the stored results are never cut.
type agentTraceMediaDTO struct {
	Items     []agentTraceMediaItemDTO `json:"items"`
	Truncated bool                     `json:"truncated"`
}

// agentTraceMediaItemDTO is one media reference with the URL that serves it and its media
// type, and the Tool Attempt whose result carried it.
type agentTraceMediaItemDTO struct {
	mediaMembersDTO
	ToolAttemptID string `json:"toolAttemptId"`
	Withheld      string `json:"withheld,omitempty"`
}

// mediaMembersDTO is the served form of one media reference, shared by the Agent Trace
// list and the Run Snapshot list. A member that may not be served is null and the item's
// Withheld says why: a URL carrying userinfo, a query or a fragment, or one outside the
// Backend's own API routes, is UNSAFE_URL while the reference stays visible as text; a
// member over the field bound is TOO_LARGE and never cut, and a cut reference would name
// nothing, so an oversized reference or media type withholds the whole item.
type mediaMembersDTO struct {
	Ref       *string `json:"ref"`
	URL       *string `json:"url"`
	MediaType *string `json:"mediaType"`
}

func toAgentTraceMediaDTO(media service.AgentTraceMedia, maxFieldBytes int) agentTraceMediaDTO {
	items := make([]agentTraceMediaItemDTO, 0, len(media.Items))
	for _, item := range media.Items {
		items = append(items, toAgentTraceMediaItemDTO(item, maxFieldBytes))
	}
	return agentTraceMediaDTO{Items: items, Truncated: media.Truncated}
}

func toAgentTraceMediaItemDTO(item service.AgentTraceMediaItem, maxFieldBytes int) agentTraceMediaItemDTO {
	members, withheld := projectMediaMembers(item.Ref, item.URL, item.MediaType, maxFieldBytes)
	return agentTraceMediaItemDTO{mediaMembersDTO: members, ToolAttemptID: item.ToolAttemptID, Withheld: withheld}
}

// projectMediaMembers applies the field bound and the credential rule to one media
// reference and returns what may be served, with the reason anything is withheld.
func projectMediaMembers(ref, mediaURL, mediaType string, maxFieldBytes int) (mediaMembersDTO, string) {
	var dto mediaMembersDTO
	if !withinFieldBound(ref, maxFieldBytes) || !withinFieldBound(mediaType, maxFieldBytes) {
		return dto, withheldTooLarge
	}
	dto.Ref, dto.MediaType = &ref, &mediaType
	switch {
	case !withinFieldBound(mediaURL, maxFieldBytes):
		return dto, withheldTooLarge
	case !servableMediaURL(mediaURL):
		return dto, withheldUnsafeURL
	default:
		dto.URL = &mediaURL
		return dto, ""
	}
}

// withinFieldBound reports whether the JSON encoding of value fits the Trace field bound.
func withinFieldBound(value string, maxFieldBytes int) bool {
	encoded, err := json.Marshal(value)
	return err == nil && len(encoded) <= maxFieldBytes
}

// servableMediaURL accepts the two shapes a Tool result may address media by: an absolute
// http or https URL that carries no userinfo, query or fragment, or a root-relative path
// under the Backend's own /api/ routes with no query or fragment. Anything else, including
// a protocol-relative or opaque URL, is not served.
func servableMediaURL(raw string) bool {
	if !strings.HasPrefix(raw, "/") {
		return credentialFreeURL(raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "" && parsed.Host == "" && parsed.User == nil && parsed.Opaque == "" &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && parsed.RawFragment == "" &&
		strings.HasPrefix(parsed.Path, "/api/") && !strings.HasPrefix(raw, "//")
}
