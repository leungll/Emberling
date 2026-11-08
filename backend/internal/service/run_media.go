package service

import (
	"encoding/json"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// RunMedia lists the media the Run's successful steps produced, each by the reference an
// output names it with, the URL that serves it and its media type, as the Node Type's
// registered media links declare them. Values are copied from committed NodeRun outputs
// as they are; the API applies the field bound and the credential rules before anything
// is served. It is bounded like the Agent Trace media list: a Run that produced more
// shows the first items in NodeRun order and reports the cut; nothing stored is touched.
type RunMedia struct {
	Items     []RunMediaItem
	Truncated bool
}

// RunMediaItem is one media reference with its URL and media type, and the NodeRun whose
// output carried it.
type RunMediaItem struct {
	Ref       string
	URL       string
	MediaType string
	NodeRunID string
}

// runMedia collects the media links of every SUCCEEDED NodeRun whose Node Type the
// Registry currently declares media links for. Only a SUCCEEDED NodeRun holds a committed
// output, so no other status contributes. A link whose pointers do not all select a string
// in the named port's value yields no item rather than a guess. Without a Registry no Node
// Type declares anything and the list is empty.
func runMedia(nodeRuns []domain.NodeRun, nodes *registry.NodeRegistry) RunMedia {
	var media RunMedia
	if nodes == nil {
		return media
	}
	for _, nodeRun := range nodeRuns {
		if nodeRun.Status != domain.NodeRunSucceeded {
			continue
		}
		reg, ok := nodes.Get(nodeRun.NodeType)
		if !ok || len(reg.Metadata.MediaLinks) == 0 {
			continue
		}
		var ports map[string]any
		if err := json.Unmarshal(nodeRun.Output, &ports); err != nil {
			continue
		}
		for _, link := range reg.Metadata.MediaLinks {
			port, present := ports[link.Port]
			if !present {
				continue
			}
			value, ok := mediaLinkValueOf(port, link)
			if !ok {
				continue
			}
			if len(media.Items) >= maxTraceMediaItems {
				media.Truncated = true
				return media
			}
			media.Items = append(media.Items, RunMediaItem{
				Ref: value.Ref, URL: value.URL, MediaType: value.MediaType, NodeRunID: nodeRun.ID,
			})
		}
	}
	return media
}
