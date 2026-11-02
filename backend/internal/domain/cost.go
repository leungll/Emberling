package domain

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Cost is the charge one external call incurred, exactly as the Provider that served the
// call reported it. It is recorded once, in the transaction that commits the call's
// result, and never changes afterwards; a call whose Provider reported no charge, and a
// call whose outcome is uncertain, carry no Cost at all. That absence is a different fact
// from a reported zero, and the two are never merged: an absent Cost is "unknown", a Cost
// with Amount "0" is "known to be free".
//
// Amount is a non-negative decimal string with at most CostAmountFractionDigits fractional
// digits, such as "0.04" or "12"; it is kept as text so no binary rounding enters a
// persisted figure. Currency is the ISO 4217 code the Provider bills in. Source is the
// stable Model ID of the call, which is how a figure is traced back to the Provider and
// price list that produced it; prices are never estimated here.
type Cost struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Source   string `json:"source"`
}

// Cost amount bounds, matching the persisted NUMERIC(14,6) column: up to eight integer
// digits and six fractional digits.
const (
	CostAmountIntegerDigits  = 8
	CostAmountFractionDigits = 6
)

// CurrencyUSD is the currency every Provider this Runtime talks to bills in today.
const CurrencyUSD = "USD"

// ErrInvalidCost is wrapped by every Cost validation failure.
var ErrInvalidCost = errors.New("invalid cost")

// Validate reports whether the Cost can be persisted: a well-formed, in-range Amount and
// a non-blank Currency and Source.
func (c Cost) Validate() error {
	if err := validateCostAmount(c.Amount); err != nil {
		return err
	}
	if strings.TrimSpace(c.Currency) == "" {
		return fmt.Errorf("%w: currency is empty", ErrInvalidCost)
	}
	if strings.TrimSpace(c.Source) == "" {
		return fmt.Errorf("%w: source is empty", ErrInvalidCost)
	}
	return nil
}

// validateCostAmount accepts "0", or digits without a leading zero, optionally followed
// by "." and one to six fractional digits.
func validateCostAmount(amount string) error {
	integer, fraction, hasFraction := strings.Cut(amount, ".")
	if integer == "" || !allDigits(integer) || len(integer) > CostAmountIntegerDigits {
		return fmt.Errorf("%w: amount %q is not a decimal within %d integer digits", ErrInvalidCost, amount, CostAmountIntegerDigits)
	}
	if len(integer) > 1 && integer[0] == '0' {
		return fmt.Errorf("%w: amount %q has a leading zero", ErrInvalidCost, amount)
	}
	if hasFraction && (fraction == "" || !allDigits(fraction) || len(fraction) > CostAmountFractionDigits) {
		return fmt.Errorf("%w: amount %q is not a decimal within %d fractional digits", ErrInvalidCost, amount, CostAmountFractionDigits)
	}
	return nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// CostAmountFromFloat renders a Provider-reported number as a Cost amount: rounded half
// away from zero to CostAmountFractionDigits, with trailing fractional zeros removed. A
// negative, NaN, infinite or out-of-range value, which no Provider legitimately reports,
// yields ok false so the caller records no Cost rather than a wrong one. -0 and values
// that round to zero are "0", a known zero.
func CostAmountFromFloat(value float64) (string, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return "", false
	}
	scale := math.Pow10(CostAmountFractionDigits)
	units := math.Floor(value*scale + 0.5)
	if units >= math.Pow10(CostAmountIntegerDigits+CostAmountFractionDigits) {
		return "", false
	}
	return formatCostUnits(int64(units)), true
}

// formatCostUnits renders a non-negative count of 10^-6 units as a Cost amount.
func formatCostUnits(units int64) string {
	integer := units / 1_000_000
	fraction := units % 1_000_000
	if fraction == 0 {
		return strconv.FormatInt(integer, 10)
	}
	digits := strings.TrimRight(fmt.Sprintf("%06d", fraction), "0")
	return strconv.FormatInt(integer, 10) + "." + digits
}

// NormalizeCostAmount brings a stored decimal rendering, such as the "0.040000" a
// NUMERIC(14,6) column reads back as, to the canonical Amount form "0.04". It rejects any
// text that is not a non-negative decimal of at most six fractional digits.
func NormalizeCostAmount(text string) (string, error) {
	text = strings.TrimSpace(text)
	integer, fraction, _ := strings.Cut(text, ".")
	if integer == "" || !allDigits(integer) {
		return "", fmt.Errorf("%w: amount %q is not a decimal", ErrInvalidCost, text)
	}
	if fraction != "" && !allDigits(fraction) {
		return "", fmt.Errorf("%w: amount %q is not a decimal", ErrInvalidCost, text)
	}
	integer = strings.TrimLeft(integer, "0")
	if integer == "" {
		integer = "0"
	}
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		return integer, validateCostAmount(integer)
	}
	amount := integer + "." + fraction
	return amount, validateCostAmount(amount)
}
