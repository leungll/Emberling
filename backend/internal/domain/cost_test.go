package domain

import (
	"errors"
	"math"
	"testing"
)

func TestCost_Validate_AcceptsCanonicalAmounts(t *testing.T) {
	for _, amount := range []string{"0", "0.04", "12", "12.5", "99999999.999999", "0.000001"} {
		cost := Cost{Amount: amount, Currency: CurrencyUSD, Source: "image-model-v1"}
		if err := cost.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", amount, err)
		}
	}
}

func TestCost_Validate_RejectsMalformedAmountsAndBlankMembers(t *testing.T) {
	for _, amount := range []string{"", "-1", "1.", ".5", "01", "1.0000001", "100000000", "1e-3", "NaN", "0.04 "} {
		cost := Cost{Amount: amount, Currency: CurrencyUSD, Source: "image-model-v1"}
		if err := cost.Validate(); !errors.Is(err, ErrInvalidCost) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidCost", amount, err)
		}
	}
	if err := (Cost{Amount: "1", Currency: " ", Source: "m"}).Validate(); !errors.Is(err, ErrInvalidCost) {
		t.Errorf("blank currency: got %v, want ErrInvalidCost", err)
	}
	if err := (Cost{Amount: "1", Currency: CurrencyUSD}).Validate(); !errors.Is(err, ErrInvalidCost) {
		t.Errorf("blank source: got %v, want ErrInvalidCost", err)
	}
}

func TestCostAmountFromFloat_RoundsToSixDigitsAndKeepsZeroKnown(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{0.04, "0.04"},
		{0.004, "0.004"},
		{0.0000315, "0.000032"},
		{0.0000004, "0"},
		{1.5, "1.5"},
		{12, "12"},
	}
	for _, tc := range cases {
		got, ok := CostAmountFromFloat(tc.value)
		if !ok || got != tc.want {
			t.Errorf("CostAmountFromFloat(%v) = %q, %v; want %q, true", tc.value, got, ok, tc.want)
		}
	}
}

func TestCostAmountFromFloat_RejectsValuesNoProviderReports(t *testing.T) {
	for _, value := range []float64{-0.01, math.NaN(), math.Inf(1), math.Inf(-1), 1e9} {
		if got, ok := CostAmountFromFloat(value); ok {
			t.Errorf("CostAmountFromFloat(%v) = %q, true; want ok false", value, got)
		}
	}
}

func TestNormalizeCostAmount_CanonicalisesStoredRenderings(t *testing.T) {
	cases := map[string]string{
		"0.040000":  "0.04",
		"0.000000":  "0",
		"12.000000": "12",
		"007.5":     "7.5",
		"0":         "0",
		"3.140000":  "3.14",
	}
	for text, want := range cases {
		got, err := NormalizeCostAmount(text)
		if err != nil || got != want {
			t.Errorf("NormalizeCostAmount(%q) = %q, %v; want %q", text, got, err, want)
		}
	}
	for _, text := range []string{"", "-1", "abc", "1.2.3", "1.0000001"} {
		if _, err := NormalizeCostAmount(text); !errors.Is(err, ErrInvalidCost) {
			t.Errorf("NormalizeCostAmount(%q) = %v, want ErrInvalidCost", text, err)
		}
	}
}
