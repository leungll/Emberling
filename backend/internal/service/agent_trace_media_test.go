package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

type mediaTestExecutor struct{}

func (mediaTestExecutor) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{}, nil
}

// mediaDeclaringRegistry registers one Tool whose result carries a clip reference, its URL
// and its media type under the pointers its media link declares.
func mediaDeclaringRegistry(t *testing.T) *registry.ToolRegistry {
	t.Helper()
	tools := registry.NewToolRegistry()
	err := tools.Register(registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:        "render_clip",
			Description: "Render a clip",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{
				"clipRef":{"type":"string"},"clipUrl":{"type":"string"},"mediaType":{"type":"string"}},"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			MediaLinks:    []domain.MediaLink{{RefPointer: "/clipRef", URLPointer: "/clipUrl", MediaTypePointer: "/mediaType"}},
		},
		Executor: mediaTestExecutor{},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return tools
}

func succeededClipAttempt(i int) domain.ToolAttempt {
	return domain.ToolAttempt{
		ID: fmt.Sprintf("ta_%d", i), ToolName: "render_clip", Status: domain.ToolAttemptSucceeded,
		Result: json.RawMessage(fmt.Sprintf(`{"clipRef":"sha256:%04d","clipUrl":"https://media.example/%d.mp4","mediaType":"video/mp4"}`, i, i)),
	}
}

// TestAgentTraceMedia_OverCap_TruncatedInPersistedOrder proves the media list stops at
// the cap and reports the cut, keeping the earliest items in persisted order.
func TestAgentTraceMedia_OverCap_TruncatedInPersistedOrder(t *testing.T) {
	tools := mediaDeclaringRegistry(t)
	attempts := make([]domain.ToolAttempt, 0, maxTraceMediaItems+1)
	for i := range maxTraceMediaItems + 1 {
		attempts = append(attempts, succeededClipAttempt(i))
	}
	media := agentTraceMedia([]AgentTraceTurn{{ToolAttempts: attempts}}, tools)
	if !media.Truncated {
		t.Fatal("Truncated = false, want true beyond the cap")
	}
	if len(media.Items) != maxTraceMediaItems {
		t.Fatalf("len(Items) = %d, want %d", len(media.Items), maxTraceMediaItems)
	}
	if media.Items[0].ToolAttemptID != "ta_0" || media.Items[maxTraceMediaItems-1].ToolAttemptID != fmt.Sprintf("ta_%d", maxTraceMediaItems-1) {
		t.Errorf("items are not the first %d in persisted order: first=%s last=%s", maxTraceMediaItems, media.Items[0].ToolAttemptID, media.Items[maxTraceMediaItems-1].ToolAttemptID)
	}

	exact := agentTraceMedia([]AgentTraceTurn{{ToolAttempts: attempts[:maxTraceMediaItems]}}, tools)
	if exact.Truncated || len(exact.Items) != maxTraceMediaItems {
		t.Errorf("exactly the cap: Truncated=%v len=%d, want false and %d", exact.Truncated, len(exact.Items), maxTraceMediaItems)
	}
}

// TestAgentTraceMedia_OnlySucceededStringMembers_Listed proves a non-SUCCEEDED Attempt, a
// result whose pointer selects nothing or a non-string, an undeclared Tool and a missing
// Registry all contribute no item.
func TestAgentTraceMedia_OnlySucceededStringMembers_Listed(t *testing.T) {
	tools := mediaDeclaringRegistry(t)
	failed := succeededClipAttempt(1)
	failed.Status = domain.ToolAttemptFailed
	numericRef := succeededClipAttempt(2)
	numericRef.Result = json.RawMessage(`{"clipRef":42,"clipUrl":"https://media.example/2.mp4","mediaType":"video/mp4"}`)
	missingURL := succeededClipAttempt(3)
	missingURL.Result = json.RawMessage(`{"clipRef":"sha256:0003","mediaType":"video/mp4"}`)
	undeclared := succeededClipAttempt(4)
	undeclared.ToolName = "lookup"
	good := succeededClipAttempt(5)

	rounds := []AgentTraceTurn{{ToolAttempts: []domain.ToolAttempt{failed, numericRef, missingURL, undeclared, good}}}
	media := agentTraceMedia(rounds, tools)
	if len(media.Items) != 1 || media.Items[0].ToolAttemptID != "ta_5" || media.Items[0].Ref != "sha256:0005" ||
		media.Items[0].URL != "https://media.example/5.mp4" || media.Items[0].MediaType != "video/mp4" {
		t.Fatalf("Items = %+v, want only the SUCCEEDED declaring Attempt's link", media.Items)
	}
	if media.Truncated {
		t.Error("Truncated = true, want false")
	}
	if got := agentTraceMedia(rounds, nil); len(got.Items) != 0 {
		t.Errorf("without a Registry Items = %+v, want none", got.Items)
	}
}

func TestAgentTraceToolDisplay_DeclaredToolsWithAttempts_Only(t *testing.T) {
	tools := mediaDeclaringRegistry(t)
	err := tools.Register(registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name: "shown_tool", Description: "Shows a key",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}}}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Display:       []domain.ToolDisplayField{{Source: domain.FactPointerArguments, Pointer: "/key", Title: "Key"}},
		},
		Executor: mediaTestExecutor{},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	rounds := []AgentTraceTurn{{ToolAttempts: []domain.ToolAttempt{
		{ID: "ta_1", ToolName: "shown_tool"}, {ID: "ta_2", ToolName: "render_clip"}, {ID: "ta_3", ToolName: "unregistered"},
	}}}
	display := agentTraceToolDisplay(rounds, tools)
	if len(display) != 1 || len(display["shown_tool"]) != 1 || display["shown_tool"][0].Title != "Key" {
		t.Fatalf("display = %+v, want only shown_tool's declaration", display)
	}
	if got := agentTraceToolDisplay(rounds, nil); got != nil {
		t.Errorf("without a Registry display = %+v, want nil", got)
	}
}
