package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestOrphanToolMessage_BoundedAndCarriesNoArguments(t *testing.T) {
	limit := 3
	cases := []struct {
		name        string
		tool        string
		disposition orphanDisposition
		count       orphanGenerationCount
		wantStatus  string
		wantText    []string
		wantCalls   string
	}{
		{
			name: "uncertain, counted with a limit", tool: "generate_image", disposition: orphanUncertain,
			count:      orphanGenerationCount{counted: true, used: 2, limit: &limit},
			wantStatus: "RESULT_UNCERTAIN",
			wantText: []string{
				"The generate_image call was interrupted because the process running it stopped.",
				"Its outcome is unknown",
				"The call was not retried automatically.",
				"It counts toward the generation limit: 2 of 3 calls used.",
				"Decide whether to call the tool again.",
			},
			wantCalls: `{"used":2,"limit":3}`,
		},
		{
			name: "uncertain, counted without a limit", tool: "generate_image", disposition: orphanUncertain,
			count:      orphanGenerationCount{counted: true, used: 4},
			wantStatus: "RESULT_UNCERTAIN",
			wantText:   []string{"It counts as a generation call: 4 calls used, no limit is set."},
			wantCalls:  `{"used":4,"limit":null}`,
		},
		{
			name: "uncertain, not counted", tool: "deploy", disposition: orphanUncertain,
			wantStatus: "RESULT_UNCERTAIN",
			wantText:   []string{"The deploy call was interrupted", "It does not count toward the generation limit."},
		},
		{
			name: "interrupted, not counted", tool: "review_asset", disposition: orphanInterrupted,
			wantStatus: "EXECUTION_INTERRUPTED",
			wantText: []string{
				"The review_asset call was interrupted because the process running it stopped before it returned a result.",
				"Calling the tool again is safe.",
				"It does not count toward the generation limit.",
			},
		},
		{
			name: "interrupted, counted with a limit", tool: "lookup", disposition: orphanInterrupted,
			count:      orphanGenerationCount{counted: true, used: 1, limit: &limit},
			wantStatus: "EXECUTION_INTERRUPTED",
			wantText:   []string{"It counts toward the generation limit: 1 of 3 calls used."},
			wantCalls:  `{"used":1,"limit":3}`,
		},
		{
			name: "an oversized tool name stays bounded", tool: strings.Repeat("é", 4096), disposition: orphanUncertain,
			count:      orphanGenerationCount{counted: true, used: 1_000_000, limit: &limit},
			wantStatus: "RESULT_UNCERTAIN",
			wantCalls:  `{"used":1000000,"limit":3}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content, err := buildOrphanToolMessage(tc.tool, tc.disposition, tc.count)
			if err != nil {
				t.Fatalf("buildOrphanToolMessage: %v", err)
			}
			if len(content) > maxOrphanToolMessageBytes {
				t.Errorf("message is %d bytes, want at most %d", len(content), maxOrphanToolMessageBytes)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(content, &fields); err != nil {
				t.Fatalf("decode %s: %v", content, err)
			}
			for key := range fields {
				if key != "status" && key != "message" && key != "generationCalls" {
					t.Errorf("message carries field %q, want only status, message and generationCalls", key)
				}
			}
			var msg orphanToolMessage
			if err := json.Unmarshal(content, &msg); err != nil {
				t.Fatalf("decode %s: %v", content, err)
			}
			if msg.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", msg.Status, tc.wantStatus)
			}
			for _, want := range tc.wantText {
				if !strings.Contains(msg.Message, want) {
					t.Errorf("message %q does not contain %q", msg.Message, want)
				}
			}
			calls, present := fields["generationCalls"]
			switch {
			case tc.wantCalls == "" && present:
				t.Errorf("generationCalls = %s, want it omitted", calls)
			case tc.wantCalls != "" && string(calls) != tc.wantCalls:
				t.Errorf("generationCalls = %s, want %s", calls, tc.wantCalls)
			}
		})
	}
}

func TestClassifyOrphan_FollowsRegisteredPolicy(t *testing.T) {
	policy := func(kind domain.SideEffectKind, idempotency domain.IdempotencyMode) domain.ToolMetadata {
		return domain.ToolMetadata{SideEffect: domain.SideEffectPolicy{Kind: kind, Idempotency: idempotency}}
	}
	cases := []struct {
		name       string
		metadata   domain.ToolMetadata
		registered bool
		want       orphanDisposition
	}{
		{"external unknown", policy(domain.SideEffectExternal, domain.IdempotencyUnknown), true, orphanUncertain},
		{"none", policy(domain.SideEffectNone, domain.IdempotencySafe), true, orphanInterrupted},
		{"external safe", policy(domain.SideEffectExternal, domain.IdempotencySafe), true, orphanInterrupted},
		{"external keyed", policy(domain.SideEffectExternal, domain.IdempotencyKeyed), true, orphanTerminal},
		{"no longer registered", domain.ToolMetadata{}, false, orphanTerminal},
	}
	for _, tc := range cases {
		if got := classifyOrphan(tc.metadata, tc.registered); got != tc.want {
			t.Errorf("%s: disposition = %d, want %d", tc.name, got, tc.want)
		}
	}
	if code, uncertain := orphanExecutionError(orphanInterrupted); code.Code != "EXECUTION_INTERRUPTED" || uncertain {
		t.Errorf("interrupted error = %+v uncertain %v, want EXECUTION_INTERRUPTED and false", code, uncertain)
	}
	for _, d := range []orphanDisposition{orphanUncertain, orphanTerminal} {
		if code, uncertain := orphanExecutionError(d); code.Code != "RESULT_UNCERTAIN" || !uncertain {
			t.Errorf("disposition %d error = %+v uncertain %v, want RESULT_UNCERTAIN and true", d, code, uncertain)
		}
	}
}
