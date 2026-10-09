package runtime

import (
	"math"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// SumTokenUsage adds Token usage field by field. It is the one aggregation rule behind
// both totals that exist: an Agent NodeRun's usage is the sum of its Turns' usage, and a
// Run's read-time total is the sum of its NodeRuns' usage. An Agent's Turns therefore
// reach the Run total exactly once, through the Agent NodeRun.
//
// A nil entry is a model call whose Provider reported nothing, which is a different fact
// from "reported 0 tokens": it contributes nothing, and when no entry carries usage at
// all the result is nil rather than a zeroed TokenUsage. Each field saturates at the int
// bounds instead of wrapping, so an absurd Provider report can never turn a total
// negative.
func SumTokenUsage(usages ...*domain.TokenUsage) *domain.TokenUsage {
	var total *domain.TokenUsage
	for _, usage := range usages {
		if usage == nil {
			continue
		}
		if total == nil {
			total = &domain.TokenUsage{}
		}
		total.InputTokens = saturatingAdd(total.InputTokens, usage.InputTokens)
		total.OutputTokens = saturatingAdd(total.OutputTokens, usage.OutputTokens)
		total.TotalTokens = saturatingAdd(total.TotalTokens, usage.TotalTokens)
	}
	return total
}

func saturatingAdd(a, b int) int {
	switch {
	case b > 0 && a > math.MaxInt-b:
		return math.MaxInt
	case b < 0 && a < math.MinInt-b:
		return math.MinInt
	default:
		return a + b
	}
}
