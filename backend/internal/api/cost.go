package api

import (
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// costDTO mirrors Studio's Cost: the charge a Provider reported for one call. amount is
// decimal text, never a JSON number, so no precision is lost on the way to Studio. A call
// whose charge is not known is projected as the JSON literal null, never as a zero.
type costDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Source   string `json:"source"`
}

func toCostDTO(cost *domain.Cost) *costDTO {
	if cost == nil {
		return nil
	}
	return &costDTO{Amount: cost.Amount, Currency: cost.Currency, Source: cost.Source}
}

// runCostDTO mirrors Studio's RunCost: the known charges summed per currency and the
// number of finished external calls whose charge is not known. totals is always an
// array, empty when no call reported a charge.
type runCostDTO struct {
	Totals       []costTotalDTO `json:"totals"`
	UnknownCalls int            `json:"unknownCalls"`
}

type costTotalDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func toRunCostDTO(cost service.RunCost) runCostDTO {
	totals := make([]costTotalDTO, len(cost.Totals))
	for i, total := range cost.Totals {
		totals[i] = costTotalDTO{Amount: total.Amount, Currency: total.Currency}
	}
	return runCostDTO{Totals: totals, UnknownCalls: cost.UnknownCalls}
}
