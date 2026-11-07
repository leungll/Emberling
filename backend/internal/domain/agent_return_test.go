package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentReturnCode_ClosedSet_MapsEveryCodeToItsFailureSource(t *testing.T) {
	cases := map[AgentReturnCode]FailureSource{
		ReturnScopeDenied:          FailureAuthorization,
		ReturnStalePatch:           FailureAuthorization,
		ReturnVerificationMissing:  FailureAuthorization,
		ReturnGoalDenied:           FailureAuthorization,
		ReturnTargetOccupied:       FailureAuthorization,
		ReturnRevisionLimitReached: FailureAuthorization,
		ReturnApprovalRejected:     FailureHuman,
		ReturnApprovalExpired:      FailureHuman,
		ReturnReleaseRefused:       FailureProviderRefusal,
		ReturnReleaseNotDeployed:   FailureProviderRefusal,
	}
	for code, want := range cases {
		if !code.IsValid() {
			t.Errorf("%s.IsValid() = false, want true", code)
		}
		got, ok := code.FailureSource()
		if !ok || got != want {
			t.Errorf("%s.FailureSource() = %s, %v, want %s, true", code, got, ok, want)
		}
		if !got.IsValid() {
			t.Errorf("failure source %s of %s is not a valid FailureSource", got, code)
		}
	}
}

func TestAgentReturnCode_OutsideSet_Rejected(t *testing.T) {
	for _, code := range []AgentReturnCode{"", "TOOL_ERROR", "PRECONDITION_UNMET", "RESULT_UNCERTAIN", "scope_denied"} {
		if code.IsValid() {
			t.Errorf("%q.IsValid() = true, want false", code)
		}
		if source, ok := code.FailureSource(); ok || source != "" {
			t.Errorf("%q.FailureSource() = %q, %v, want \"\", false", code, source, ok)
		}
	}
}

func TestToolMetadata_Validate_ReconcilableOnNonAsyncOrWithoutPoll_Rejected(t *testing.T) {
	base := func() ToolMetadata {
		return ToolMetadata{
			Name:          "deploy_change",
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    SideEffectPolicy{Kind: SideEffectExternal, Idempotency: IdempotencyKeyed},
			ExecutionKind: ToolExecutionAsync,
			Poll:          &PollPolicy{IntervalMs: 1000, MaxPolls: 10},
			Reconcilable:  true,
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("ASYNC tool with poll and reconcilable: Validate() = %v, want nil", err)
	}

	sync := base()
	sync.ExecutionKind = ToolExecutionSync
	sync.Poll = nil
	if err := sync.Validate(); err == nil || !strings.Contains(err.Error(), "reconcilable is only allowed on an ASYNC tool that declares poll") {
		t.Errorf("SYNC reconcilable tool: Validate() = %v, want the reconcilable rule", err)
	}

	noPoll := base()
	noPoll.Poll = nil
	if err := noPoll.Validate(); err == nil || !strings.Contains(err.Error(), "reconcilable is only allowed on an ASYNC tool that declares poll") {
		t.Errorf("ASYNC reconcilable tool without poll: Validate() = %v, want the reconcilable rule", err)
	}
}

func TestToolMetadata_Validate_InvalidRiskOrRecordKind_Rejected(t *testing.T) {
	valid := func() ToolMetadata {
		return ToolMetadata{
			Name:          "write_file",
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    SideEffectPolicy{Kind: SideEffectNone, Idempotency: IdempotencySafe},
			ExecutionKind: ToolExecutionSync,
			Authorization: &ToolAuthorization{Risk: ToolRiskLow, Reversible: true},
			Records:       []ToolRecordKind{ToolRecordWorkspacePatch},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	cases := []struct {
		name    string
		mutate  func(*ToolMetadata)
		wantMsg string
	}{
		{"unknown risk", func(m *ToolMetadata) { m.Authorization.Risk = "HIGH" }, `authorization.risk "HIGH"`},
		{"empty risk", func(m *ToolMetadata) { m.Authorization.Risk = "" }, `authorization.risk ""`},
		{"unknown record kind", func(m *ToolMetadata) { m.Records = []ToolRecordKind{"APPROVAL"} }, `records: "APPROVAL" is not a supported record kind`},
		{"repeated record kind", func(m *ToolMetadata) {
			m.Records = []ToolRecordKind{ToolRecordChangeRequest, ToolRecordChangeRequest}
		}, `records: "CHANGE_REQUEST" is declared more than once`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := valid()
			tc.mutate(&metadata)
			err := metadata.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

func TestToolMetadata_UndeclaredAuthorization_IsLowRisk(t *testing.T) {
	undeclared := ToolMetadata{Name: "lookup"}
	if got := undeclared.EffectiveRisk(); got != ToolRiskLow {
		t.Errorf("EffectiveRisk() of an undeclared authorization = %s, want LOW", got)
	}
	declared := ToolMetadata{Authorization: &ToolAuthorization{Risk: ToolRiskProduction}}
	if got := declared.EffectiveRisk(); got != ToolRiskProduction {
		t.Errorf("EffectiveRisk() = %s, want PRODUCTION", got)
	}
	encoded, err := json.Marshal(undeclared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"authorization"`) {
		t.Errorf("undeclared authorization is on the wire: %s", encoded)
	}
	if !undeclared.DeclaresRecord(ToolRecordWorkspacePatch) == false {
		t.Errorf("DeclaresRecord on no records = true, want false")
	}
}
