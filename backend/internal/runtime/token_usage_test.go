package runtime

import (
	"math"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func reportedUsage(input, output int) *domain.TokenUsage {
	return &domain.TokenUsage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
}

// TestSumTokenUsage_NoEntries_IsNil proves that nothing to add is reported as absent, not
// as a zeroed total.
func TestSumTokenUsage_NoEntries_IsNil(t *testing.T) {
	if got := SumTokenUsage(); got != nil {
		t.Errorf("SumTokenUsage() = %+v, want nil", got)
	}
}

// TestSumTokenUsage_AllEntriesUnreported_IsNil proves that model calls whose Provider
// reported no usage do not invent a zero total.
func TestSumTokenUsage_AllEntriesUnreported_IsNil(t *testing.T) {
	if got := SumTokenUsage(nil, nil); got != nil {
		t.Errorf("SumTokenUsage(nil, nil) = %+v, want nil", got)
	}
}

// TestSumTokenUsage_PartialReports_SumsOnlyReported proves an unreported call contributes
// nothing while the reported ones are added field by field.
func TestSumTokenUsage_PartialReports_SumsOnlyReported(t *testing.T) {
	got := SumTokenUsage(reportedUsage(10, 2), nil, reportedUsage(7, 5))
	want := domain.TokenUsage{InputTokens: 17, OutputTokens: 7, TotalTokens: 24}
	if got == nil || *got != want {
		t.Errorf("SumTokenUsage = %+v, want %+v", got, want)
	}
}

// TestSumTokenUsage_ReportedZero_IsZeroNotNil keeps "reported 0 tokens" distinct from
// "reported nothing".
func TestSumTokenUsage_ReportedZero_IsZeroNotNil(t *testing.T) {
	got := SumTokenUsage(nil, reportedUsage(0, 0))
	if got == nil || *got != (domain.TokenUsage{}) {
		t.Errorf("SumTokenUsage = %+v, want a zero total", got)
	}
}

// TestSumTokenUsage_DoesNotAliasInput proves the total is a fresh value: writing the sum
// onto one NodeRun can never mutate the Turn usage it was computed from.
func TestSumTokenUsage_DoesNotAliasInput(t *testing.T) {
	only := reportedUsage(3, 4)
	got := SumTokenUsage(only)
	if got == only {
		t.Fatalf("SumTokenUsage returned its input pointer")
	}
	got.InputTokens = 99
	if only.InputTokens != 3 {
		t.Errorf("input mutated through the total: %+v", only)
	}
}

// TestSumTokenUsage_Overflow_Saturates proves a total never wraps around to a negative
// count.
func TestSumTokenUsage_Overflow_Saturates(t *testing.T) {
	huge := &domain.TokenUsage{InputTokens: math.MaxInt, OutputTokens: math.MaxInt - 1, TotalTokens: math.MaxInt}
	got := SumTokenUsage(huge, reportedUsage(5, 5))
	want := domain.TokenUsage{InputTokens: math.MaxInt, OutputTokens: math.MaxInt, TotalTokens: math.MaxInt}
	if got == nil || *got != want {
		t.Errorf("SumTokenUsage = %+v, want every field saturated at MaxInt", got)
	}
}

// TestSaturatingAdd_NegativeOverflow_Saturates covers the lower bound of the same rule.
func TestSaturatingAdd_NegativeOverflow_Saturates(t *testing.T) {
	if got := saturatingAdd(math.MinInt, -1); got != math.MinInt {
		t.Errorf("saturatingAdd(MinInt, -1) = %d, want MinInt", got)
	}
	if got := saturatingAdd(-3, 5); got != 2 {
		t.Errorf("saturatingAdd(-3, 5) = %d, want 2", got)
	}
}
