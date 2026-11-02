package runtime

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// CostTotal is the sum of the known charges reported in one currency.
type CostTotal struct {
	Amount   string
	Currency string
}

// RunCostSummary is the read-time cost of one Run: the known charges summed per
// currency, and the number of finished external calls whose charge is not known. The
// two are never merged: an unknown charge is reported as a count, not estimated into a
// total, and a reported zero is a known charge that adds nothing but is not unknown.
type RunCostSummary struct {
	Totals       []CostTotal
	UnknownCalls int
}

// SumCosts is the one aggregation rule behind the Run-level cost. costs holds one entry
// per finished external call of the Run: the recorded Cost, or nil for a call that
// finished without one, whether its Provider reported no charge or its outcome was
// uncertain. Totals are summed exactly, in decimal, per currency, in currency order; a
// Cost that does not validate counts as unknown rather than corrupting a total.
func SumCosts(costs []*domain.Cost) RunCostSummary {
	var summary RunCostSummary
	units := map[string]*big.Int{}
	for _, cost := range costs {
		if cost == nil {
			summary.UnknownCalls++
			continue
		}
		value, err := costUnits(cost.Amount)
		if err != nil {
			summary.UnknownCalls++
			continue
		}
		if total, ok := units[cost.Currency]; ok {
			total.Add(total, value)
		} else {
			units[cost.Currency] = value
		}
	}
	currencies := make([]string, 0, len(units))
	for currency := range units {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	for _, currency := range currencies {
		summary.Totals = append(summary.Totals, CostTotal{Amount: formatCostUnits(units[currency]), Currency: currency})
	}
	return summary
}

// costUnits parses a canonical Cost amount into 10^-6 units.
func costUnits(amount string) (*big.Int, error) {
	if err := (domain.Cost{Amount: amount, Currency: "x", Source: "x"}).Validate(); err != nil {
		return nil, err
	}
	integer, fraction, _ := strings.Cut(amount, ".")
	digits := integer + fraction + strings.Repeat("0", domain.CostAmountFractionDigits-len(fraction))
	value, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("%w: amount %q", domain.ErrInvalidCost, amount)
	}
	return value, nil
}

// formatCostUnits renders 10^-6 units as a canonical amount: no trailing fractional
// zeros, and "0" for zero.
func formatCostUnits(units *big.Int) string {
	text := units.String()
	if len(text) <= domain.CostAmountFractionDigits {
		text = strings.Repeat("0", domain.CostAmountFractionDigits-len(text)+1) + text
	}
	split := len(text) - domain.CostAmountFractionDigits
	integer, fraction := text[:split], strings.TrimRight(text[split:], "0")
	if fraction == "" {
		return integer
	}
	return integer + "." + fraction
}
