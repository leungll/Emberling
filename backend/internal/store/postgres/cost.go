package postgres

import (
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// costColumns is the SELECT fragment of the three cost columns every call table shares.
// The amount is read as text so the decimal reaches Go exactly as PostgreSQL holds it,
// never through a binary float.
const costColumns = `cost_amount::text, cost_currency, cost_source`

// costParams renders a Cost as the three column values an INSERT or UPDATE binds. A nil
// Cost binds three NULLs, which the all-or-none CHECK accepts as "charge not known"; a
// Cost that fails validation is refused here, before any SQL runs, so an unusable figure
// never reaches a row.
func costParams(cost *domain.Cost) (amount, currency, source *string, err error) {
	if cost == nil {
		return nil, nil, nil, nil
	}
	if err := cost.Validate(); err != nil {
		return nil, nil, nil, err
	}
	return &cost.Amount, &cost.Currency, &cost.Source, nil
}

// scanCost rebuilds a Cost from the three scanned columns. All NULL is no Cost; the
// stored NUMERIC rendering is brought back to the canonical amount form.
func scanCost(amount, currency, source *string) (*domain.Cost, error) {
	if amount == nil && currency == nil && source == nil {
		return nil, nil
	}
	if amount == nil || currency == nil || source == nil {
		return nil, fmt.Errorf("cost columns are partially set")
	}
	canonical, err := domain.NormalizeCostAmount(*amount)
	if err != nil {
		return nil, err
	}
	return &domain.Cost{Amount: canonical, Currency: *currency, Source: *source}, nil
}
