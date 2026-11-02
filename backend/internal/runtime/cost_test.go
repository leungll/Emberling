package runtime

import (
	"reflect"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func usd(amount string) *domain.Cost {
	return &domain.Cost{Amount: amount, Currency: domain.CurrencyUSD, Source: "image-model-v1"}
}

// TestSumCosts_UnknownAndZero_StrictlyDistinct: a nil entry is counted as unknown and
// adds nothing; a reported "0" is a known charge that adds nothing and is not unknown.
func TestSumCosts_UnknownAndZero_StrictlyDistinct(t *testing.T) {
	got := SumCosts([]*domain.Cost{usd("0"), nil, usd("0"), nil, nil})
	want := RunCostSummary{Totals: []CostTotal{{Amount: "0", Currency: domain.CurrencyUSD}}, UnknownCalls: 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SumCosts = %+v, want %+v", got, want)
	}

	if got := SumCosts([]*domain.Cost{nil, nil}); got.UnknownCalls != 2 || len(got.Totals) != 0 {
		t.Errorf("all unknown: SumCosts = %+v, want no totals and 2 unknown", got)
	}
	if got := SumCosts(nil); got.UnknownCalls != 0 || len(got.Totals) != 0 {
		t.Errorf("no calls: SumCosts = %+v, want the empty summary", got)
	}
}

// TestSumCosts_ManyCalls_SumsExactlyInDecimal covers the multi-Attempt, multi-Turn case:
// fractions that would not sum exactly in binary floating point do here.
func TestSumCosts_ManyCalls_SumsExactlyInDecimal(t *testing.T) {
	costs := []*domain.Cost{usd("0.1"), usd("0.2"), usd("0.04"), usd("0.000001"), usd("12"), nil}
	got := SumCosts(costs)
	want := RunCostSummary{Totals: []CostTotal{{Amount: "12.340001", Currency: domain.CurrencyUSD}}, UnknownCalls: 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SumCosts = %+v, want %+v", got, want)
	}
}

// TestSumCosts_MixedCurrencies_TotalsPerCurrency: amounts in different currencies are
// never added together; each currency is reported on its own, in currency order.
func TestSumCosts_MixedCurrencies_TotalsPerCurrency(t *testing.T) {
	eur := &domain.Cost{Amount: "1.5", Currency: "EUR", Source: "model-eu"}
	got := SumCosts([]*domain.Cost{usd("0.25"), eur, usd("0.75")})
	want := RunCostSummary{Totals: []CostTotal{
		{Amount: "1.5", Currency: "EUR"},
		{Amount: "1", Currency: domain.CurrencyUSD},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SumCosts = %+v, want %+v", got, want)
	}
}

// TestSumCosts_InvalidAmount_CountsAsUnknown: a stored amount that fails validation
// never corrupts a total; it is reported as unknown instead.
func TestSumCosts_InvalidAmount_CountsAsUnknown(t *testing.T) {
	bad := &domain.Cost{Amount: "1e-3", Currency: domain.CurrencyUSD, Source: "m"}
	got := SumCosts([]*domain.Cost{bad, usd("0.5")})
	want := RunCostSummary{Totals: []CostTotal{{Amount: "0.5", Currency: domain.CurrencyUSD}}, UnknownCalls: 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SumCosts = %+v, want %+v", got, want)
	}
}
