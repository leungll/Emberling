package reviewasset

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func action(arguments string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1",
		ToolName: ToolName, AttemptNo: 1, Arguments: json.RawMessage(arguments),
	}
}

func review(t *testing.T, arguments string) result {
	t.Helper()
	got, err := Executor{}.Execute(context.Background(), action(arguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil {
		t.Fatalf("result = %+v, want COMPLETED", got)
	}
	schema, err := registry.CompileSchema(json.RawMessage(outputSchema))
	if err != nil {
		t.Fatalf("compile output schema: %v", err)
	}
	var value any
	if err := json.Unmarshal(got.Result.Output, &value); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		t.Fatalf("output %s does not conform to the output schema: %v", got.Result.Output, err)
	}
	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	return out
}

func TestRegistration_SafeVerdictProducerRequiringGeneratedImage_RegistersCleanly(t *testing.T) {
	reg := Registration()
	if want := (domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}); reg.Metadata.SideEffect != want {
		t.Errorf("sideEffect = %+v, want %+v", reg.Metadata.SideEffect, want)
	}
	if reg.Metadata.CountsTowardGenerationLimit {
		t.Error("countsTowardGenerationLimit = true, want false for a review")
	}
	produces := reg.Metadata.Produces
	if produces == nil || produces.FactType != FactType || produces.VerdictPointer == nil || produces.BasisFactType != generatedFactType {
		t.Fatalf("produces = %+v, want a verdict-carrying %q based on %q", produces, FactType, generatedFactType)
	}
	if len(reg.Metadata.Requires) != 1 || reg.Metadata.Requires[0].FactType != generatedFactType {
		t.Fatalf("requires = %+v, want one %q requirement", reg.Metadata.Requires, generatedFactType)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestExecute_UncontrolledReview_VerdictIsDeterministicPerAssetRef(t *testing.T) {
	passing, failing := 0, 0
	for i := 0; i < 30; i++ {
		assetRef := fmt.Sprintf("img_%016x", i)
		arguments := `{"assetRef":"` + assetRef + `","photoAssetId":"asset_photo_1"}`
		first, second := review(t, arguments), review(t, arguments)
		if first.Passed != second.Passed || first.Passed != Verdict(assetRef) {
			t.Fatalf("verdict for %s = %v then %v, want the same deterministic verdict", assetRef, first.Passed, second.Passed)
		}
		if first.PolicyVersion != PolicyVersion {
			t.Errorf("policyVersion = %q, want %q", first.PolicyVersion, PolicyVersion)
		}
		if first.Passed {
			passing++
			if len(first.Reasons) != 0 {
				t.Errorf("passing review reasons = %v, want none", first.Reasons)
			}
		} else {
			failing++
			if len(first.Reasons) != 1 {
				t.Errorf("failing review reasons = %v, want one fixed reason", first.Reasons)
			}
		}
	}
	if passing == 0 || failing == 0 {
		t.Fatalf("verdicts over 30 references: %d passed, %d failed, want both outcomes", passing, failing)
	}
}

func TestExecute_MockControl_ForcesVerdict(t *testing.T) {
	// img_0000000000000000 fails without control and img_0000000000000001 passes.
	if Verdict("img_0000000000000000") || !Verdict("img_0000000000000001") {
		t.Fatal("fixture references no longer have the expected uncontrolled verdicts")
	}
	if !review(t, `{"assetRef":"img_0000000000000000","photoAssetId":"p","mock":"pass"}`).Passed {
		t.Error("mock pass did not force a passing verdict")
	}
	if review(t, `{"assetRef":"img_0000000000000001","photoAssetId":"p","mock":"fail"}`).Passed {
		t.Error("mock fail did not force a failing verdict")
	}
}

func TestExecute_InvalidArguments_Fails(t *testing.T) {
	for _, arguments := range []string{`{"assetRef":"img_1"}`, `{"photoAssetId":"p"}`, `[]`} {
		if _, err := (Executor{}).Execute(context.Background(), action(arguments)); err == nil {
			t.Errorf("execute(%s) error = nil, want an explicit error", arguments)
		}
	}
}
