package service

import (
	"encoding/json"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// maxTraceMediaItems bounds the media list one Agent Trace response carries. Like the fact
// ledger it is a display projection: an Agent Run that produced more media shows the first
// maxTraceMediaItems in persisted order and reports the cut; nothing stored is touched.
const maxTraceMediaItems = 256

// AgentTraceMedia lists the media the Agent Run's successful Tool calls produced, each by
// the reference a result or final output names it with, the URL that serves it and its
// media type, as the Tool's registered media links declare them. Values are copied from
// committed Tool results as they are; the API applies the field bound and the credential
// rules before anything is served.
type AgentTraceMedia struct {
	Items     []AgentTraceMediaItem
	Truncated bool
}

// AgentTraceMediaItem is one media reference with its URL and media type, and the Tool
// Attempt whose result carried it.
type AgentTraceMediaItem struct {
	Ref           string
	URL           string
	MediaType     string
	ToolAttemptID string
}

// agentTraceMedia collects the media links of every SUCCEEDED Tool Attempt in rounds whose
// Tool the Registry currently declares media links for. Only a SUCCEEDED Attempt holds a
// committed Tool result, so no other status contributes. A link whose pointers do not all
// select a string in the result yields no item rather than a guess. Without a Registry no
// Tool declares anything and the list is empty.
func agentTraceMedia(rounds []AgentTraceTurn, tools *registry.ToolRegistry) AgentTraceMedia {
	var media AgentTraceMedia
	if tools == nil {
		return media
	}
	for _, round := range rounds {
		for _, attempt := range round.ToolAttempts {
			if attempt.Status != domain.ToolAttemptSucceeded {
				continue
			}
			reg, ok := tools.Get(attempt.ToolName)
			if !ok || len(reg.Metadata.MediaLinks) == 0 {
				continue
			}
			var result any
			if err := json.Unmarshal(attempt.Result, &result); err != nil {
				continue
			}
			for _, link := range reg.Metadata.MediaLinks {
				value, ok := mediaLinkValueOf(result, link)
				if !ok {
					continue
				}
				if len(media.Items) >= maxTraceMediaItems {
					media.Truncated = true
					return media
				}
				media.Items = append(media.Items, AgentTraceMediaItem{
					Ref: value.Ref, URL: value.URL, MediaType: value.MediaType, ToolAttemptID: attempt.ID,
				})
			}
		}
	}
	return media
}

// mediaLinkValue is what one media link selects out of a committed document: the
// reference, the URL that serves the media and its media type.
type mediaLinkValue struct {
	Ref       string
	URL       string
	MediaType string
}

// mediaLinkValueOf reads one declared media link out of a decoded document (a Tool result
// or a Node output port value). Every pointer must select a non-empty string; the media
// type comes from the document when the link points at it and from the declaration
// otherwise.
func mediaLinkValueOf(document any, link domain.MediaLink) (mediaLinkValue, bool) {
	ref, ok := stringAtPointer(document, link.RefPointer)
	if !ok {
		return mediaLinkValue{}, false
	}
	url, ok := stringAtPointer(document, link.URLPointer)
	if !ok {
		return mediaLinkValue{}, false
	}
	mediaType := link.MediaType
	if link.MediaTypePointer != "" {
		if mediaType, ok = stringAtPointer(document, link.MediaTypePointer); !ok {
			return mediaLinkValue{}, false
		}
	}
	return mediaLinkValue{Ref: ref, URL: url, MediaType: mediaType}, true
}

func stringAtPointer(document any, pointer string) (string, bool) {
	value, found := domain.LookupJSONPointer(document, pointer)
	if !found {
		return "", false
	}
	text, isString := value.(string)
	return text, isString && text != ""
}

// agentTraceToolDisplay returns the display fields the Registry currently declares for
// every Tool that has an Attempt in rounds, keyed by Tool Name, so the API can project the
// named members of each Attempt without reading the Registry itself. A Tool without a
// registration or without display fields has no entry.
func agentTraceToolDisplay(rounds []AgentTraceTurn, tools *registry.ToolRegistry) map[string][]domain.ToolDisplayField {
	if tools == nil {
		return nil
	}
	display := make(map[string][]domain.ToolDisplayField)
	for _, round := range rounds {
		for _, attempt := range round.ToolAttempts {
			if _, seen := display[attempt.ToolName]; seen {
				continue
			}
			reg, ok := tools.Get(attempt.ToolName)
			if !ok || len(reg.Metadata.Display) == 0 {
				continue
			}
			display[attempt.ToolName] = reg.Metadata.Display
		}
	}
	return display
}
