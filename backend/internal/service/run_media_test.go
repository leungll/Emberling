package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

type mediaTestNodeExecutor struct{}

func (mediaTestNodeExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }
func (mediaTestNodeExecutor) Execute(context.Context, registry.NodeInput, map[string]any) (registry.NodeResult, error) {
	return registry.CompletedResult(nil), nil
}

// mediaDeclaringNodeRegistry registers a Node Type whose transcript port names a source
// video, with a second link whose pointers no output satisfies, and a Node Type that
// declares nothing.
func mediaDeclaringNodeRegistry(t *testing.T) *registry.NodeRegistry {
	t.Helper()
	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		{
			Metadata: domain.NodeMetadata{
				Type: "test_transcript", DisplayName: "Transcript", Category: domain.NodeCategoryPromptAndModel,
				ExecutionKind: domain.NodeExecutionSync,
				Outputs: []domain.PortMetadata{
					{Name: "transcript", DataType: domain.PortTypeJSON, Required: true},
					{Name: "task", DataType: domain.PortTypeJSON, Required: true},
				},
				ConfigSchema: json.RawMessage(`{"type":"object"}`),
				SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
				MediaLinks: []domain.MediaLink{
					{Port: "transcript", RefPointer: "/sourceId", URLPointer: "/sourceUrl", MediaTypePointer: "/sourceMediaType"},
					{Port: "task", RefPointer: "/posterRef", URLPointer: "/posterUrl", MediaType: "image/png"},
				},
			},
			Binding: registry.ExecutorBinding{Executor: mediaTestNodeExecutor{}},
		},
		{
			Metadata: domain.NodeMetadata{
				Type: "test_plain", DisplayName: "Plain", Category: domain.NodeCategoryPromptAndModel,
				ExecutionKind: domain.NodeExecutionSync,
				Outputs:       []domain.PortMetadata{{Name: "transcript", DataType: domain.PortTypeJSON, Required: true}},
				ConfigSchema:  json.RawMessage(`{"type":"object"}`),
				SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			},
			Binding: registry.ExecutorBinding{Executor: mediaTestNodeExecutor{}},
		},
	} {
		if err := nodes.Register(reg); err != nil {
			t.Fatalf("register %s: %v", reg.Metadata.Type, err)
		}
	}
	return nodes
}

func transcriptNodeRun(id, nodeType string, status domain.NodeRunStatus, transcript string) domain.NodeRun {
	return domain.NodeRun{
		ID: id, NodeType: nodeType, Status: status,
		Output: json.RawMessage(`{"transcript":` + transcript + `,"task":{"sourceId":"src_1"}}`),
	}
}

const servedTranscript = `{"artifactId":"artifact_1","sourceId":"src_1","sourceUrl":"https://media.example/v1/sources/src_1/content","sourceMediaType":"video/webm"}`

func TestRunMedia_SucceededNodeRunWithDeclaredLinks_ListsTheSourceOnce(t *testing.T) {
	nodes := mediaDeclaringNodeRegistry(t)
	media := runMedia([]domain.NodeRun{
		transcriptNodeRun("nr_1", "test_transcript", domain.NodeRunSucceeded, servedTranscript),
	}, nodes)
	want := RunMedia{Items: []RunMediaItem{{
		Ref: "src_1", URL: "https://media.example/v1/sources/src_1/content", MediaType: "video/webm", NodeRunID: "nr_1",
	}}}
	if fmt.Sprint(media) != fmt.Sprint(want) {
		t.Fatalf("runMedia = %+v, want %+v (the poster link selects nothing and yields no item)", media, want)
	}
}

func TestRunMedia_NothingToList_EmptyList(t *testing.T) {
	nodes := mediaDeclaringNodeRegistry(t)
	cases := map[string][]domain.NodeRun{
		"node type without links": {transcriptNodeRun("nr_1", "test_plain", domain.NodeRunSucceeded, servedTranscript)},
		"unregistered node type":  {transcriptNodeRun("nr_1", "test_unknown", domain.NodeRunSucceeded, servedTranscript)},
		"failed node run":         {transcriptNodeRun("nr_1", "test_transcript", domain.NodeRunFailed, servedTranscript)},
		"running node run":        {transcriptNodeRun("nr_1", "test_transcript", domain.NodeRunRunning, servedTranscript)},
		"older output without the served source": {
			transcriptNodeRun("nr_1", "test_transcript", domain.NodeRunSucceeded, `{"artifactId":"artifact_1","sourceId":"src_1"}`),
		},
		"output that is not an object": {{ID: "nr_1", NodeType: "test_transcript", Status: domain.NodeRunSucceeded, Output: json.RawMessage(`"text"`)}},
		"no node runs":                 nil,
	}
	for name, nodeRuns := range cases {
		t.Run(name, func(t *testing.T) {
			media := runMedia(nodeRuns, nodes)
			if len(media.Items) != 0 || media.Truncated {
				t.Fatalf("runMedia = %+v, want an empty, untruncated list", media)
			}
		})
	}
	if media := runMedia([]domain.NodeRun{transcriptNodeRun("nr_1", "test_transcript", domain.NodeRunSucceeded, servedTranscript)}, nil); len(media.Items) != 0 {
		t.Fatalf("runMedia without a Registry = %+v, want an empty list", media)
	}
}

// TestRunMedia_OverCap_TruncatedInNodeRunOrder proves the list stops at the cap and reports
// the cut, keeping the earliest NodeRuns' items in order.
func TestRunMedia_OverCap_TruncatedInNodeRunOrder(t *testing.T) {
	nodes := mediaDeclaringNodeRegistry(t)
	nodeRuns := make([]domain.NodeRun, 0, maxTraceMediaItems+1)
	for i := range maxTraceMediaItems + 1 {
		transcript := strings.ReplaceAll(servedTranscript, "src_1", fmt.Sprintf("src_%04d", i))
		nodeRuns = append(nodeRuns, transcriptNodeRun(fmt.Sprintf("nr_%d", i), "test_transcript", domain.NodeRunSucceeded, transcript))
	}
	media := runMedia(nodeRuns, nodes)
	if !media.Truncated || len(media.Items) != maxTraceMediaItems {
		t.Fatalf("items = %d, truncated = %t; want %d items and the cut reported", len(media.Items), media.Truncated, maxTraceMediaItems)
	}
	if media.Items[0].Ref != "src_0000" || media.Items[0].NodeRunID != "nr_0" || media.Items[maxTraceMediaItems-1].Ref != fmt.Sprintf("src_%04d", maxTraceMediaItems-1) {
		t.Fatalf("items are not the first %d in NodeRun order: first=%+v last=%+v", maxTraceMediaItems, media.Items[0], media.Items[maxTraceMediaItems-1])
	}
}
