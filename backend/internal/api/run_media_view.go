package api

import "github.com/leungll/Emberling/backend/internal/service"

// ---- Run Snapshot: media the Run's steps produced ----
//
// The Run page shows a step's media, such as the source video an editing task names by
// id, next to the reference its output carries, without ever building a URL from that
// value: a reference resolves to a URL only when the Node Type's registered media links
// say so and the SUCCEEDED output carried both. Each URL passes the same credential rules
// and field bound as the Agent Trace media list before it is served.

// runMediaDTO is the bounded media list of the Run Snapshot. Truncated reports that the
// Run produced more media than Items shows; the stored outputs are never cut.
type runMediaDTO struct {
	Items     []runMediaItemDTO `json:"items"`
	Truncated bool              `json:"truncated"`
}

// runMediaItemDTO is one media reference with the URL that serves it and its media type,
// and the NodeRun whose output carried it; the members are withheld by the rules of
// mediaMembersDTO.
type runMediaItemDTO struct {
	mediaMembersDTO
	NodeRunID string `json:"nodeRunId"`
	Withheld  string `json:"withheld,omitempty"`
}

func toRunMediaDTO(media service.RunMedia, maxFieldBytes int) runMediaDTO {
	items := make([]runMediaItemDTO, 0, len(media.Items))
	for _, item := range media.Items {
		members, withheld := projectMediaMembers(item.Ref, item.URL, item.MediaType, maxFieldBytes)
		items = append(items, runMediaItemDTO{mediaMembersDTO: members, NodeRunID: item.NodeRunID, Withheld: withheld})
	}
	return runMediaDTO{Items: items, Truncated: media.Truncated}
}
