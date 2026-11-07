package api

import (
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// releaseActionDTO is the release view of one Action: every operation the Runtime asked
// production for, in order, with what became of it; the deploy target occupation the
// approval acquired; and the human wait its confirm deadline opened. It carries operation
// keys, ids, statuses and times only: no request bodies, credentials or addresses.
type releaseActionDTO struct {
	Operations []releaseOperationDTO `json:"operations"`
	Occupation *releaseOccupationDTO `json:"occupation"`
	HumanWait  *humanWaitDTO         `json:"humanWait"`
}

// releaseOperationOutcome is what the Runtime knows about one operation: PENDING while
// production has not answered and nothing is in doubt, UNKNOWN once the dispatch got no
// usable answer, DEPLOYED, VOIDED or REFUSED once production answered for good.
type releaseOperationOutcome string

const (
	releaseOutcomePending  releaseOperationOutcome = "PENDING"
	releaseOutcomeUnknown  releaseOperationOutcome = "UNKNOWN"
	releaseOutcomeDeployed releaseOperationOutcome = "DEPLOYED"
	releaseOutcomeVoided   releaseOperationOutcome = "VOIDED"
	releaseOutcomeRefused  releaseOperationOutcome = "REFUSED"
)

type releaseOperationDTO struct {
	OperationKey  string                   `json:"operationKey"`
	ToolAttemptID string                   `json:"toolAttemptId"`
	AttemptNo     int                      `json:"attemptNo"`
	Status        domain.ToolAttemptStatus `json:"status"`
	Outcome       releaseOperationOutcome  `json:"outcome"`
	// ConfirmDeadline is when an unanswered operation hands its Action to a person;
	// OutcomeUnknownSince and VoidRequestedAt are set once each, when they happened.
	ConfirmDeadline     *time.Time `json:"confirmDeadline"`
	OutcomeUnknownSince *time.Time `json:"outcomeUnknownSince"`
	VoidRequestedAt     *time.Time `json:"voidRequestedAt"`
}

type releaseOccupationDTO struct {
	ID            string                         `json:"id"`
	TargetKey     string                         `json:"targetKey"`
	OperationKey  string                         `json:"operationKey"`
	Status        domain.ReleaseOccupationStatus `json:"status"`
	AcquiredAt    time.Time                      `json:"acquiredAt"`
	ReleasedAt    *time.Time                     `json:"releasedAt"`
	ReleaseReason *domain.ReleaseReason          `json:"releaseReason"`
	// NextQueryAt and QueryCount are the post-task reconciliation schedule an occupation
	// carries once its Run ended with the release outcome unknown.
	// DeployedUncheckedVersion is the version production runs when that reconciliation
	// learned the operation had deployed: live, but never health-checked by the task.
	NextQueryAt              *time.Time `json:"nextQueryAt"`
	QueryCount               int        `json:"queryCount"`
	DeployedUncheckedVersion *string    `json:"deployedUncheckedVersion"`
}

// humanWaitDTO is one human wait. ClosedBy is the operator's identity name, never a
// credential; Outcome is the production answer or the stop that closed it.
type humanWaitDTO struct {
	ID       string                 `json:"id"`
	Status   domain.HumanWaitStatus `json:"status"`
	OpenedAt time.Time              `json:"openedAt"`
	Deadline time.Time              `json:"deadline"`
	ClosedAt *time.Time             `json:"closedAt"`
	ClosedBy *string                `json:"closedBy"`
	Outcome  *string                `json:"outcome"`
}

// toReleaseActionDTO builds the release view, or nil when the round holds no release
// fact: no Attempt with an operation key, no occupation and no human wait.
func toReleaseActionDTO(round service.AgentTraceTurn) *releaseActionDTO {
	operations := make([]releaseOperationDTO, 0, len(round.ToolAttempts))
	for _, attempt := range round.ToolAttempts {
		if attempt.OperationKey == nil {
			continue
		}
		operations = append(operations, releaseOperationDTO{
			OperationKey:        *attempt.OperationKey,
			ToolAttemptID:       attempt.ID,
			AttemptNo:           attempt.AttemptNo,
			Status:              attempt.Status,
			Outcome:             releaseOutcomeOf(attempt),
			ConfirmDeadline:     utcTimePtr(attempt.ConfirmDeadline),
			OutcomeUnknownSince: utcTimePtr(attempt.OutcomeUnknownSince),
			VoidRequestedAt:     utcTimePtr(attempt.VoidRequestedAt),
		})
	}
	if len(operations) == 0 && round.Occupation == nil && round.HumanWait == nil {
		return nil
	}
	dto := &releaseActionDTO{Operations: operations}
	if occupation := round.Occupation; occupation != nil {
		dto.Occupation = &releaseOccupationDTO{
			ID:            occupation.ID,
			TargetKey:     occupation.TargetKey,
			OperationKey:  occupation.OperationKey,
			Status:        occupation.Status,
			AcquiredAt:    utcTime(occupation.AcquiredAt),
			ReleasedAt:    utcTimePtr(occupation.ReleasedAt),
			ReleaseReason: occupation.ReleaseReason,

			NextQueryAt:              utcTimePtr(occupation.NextQueryAt),
			QueryCount:               occupation.QueryCount,
			DeployedUncheckedVersion: occupation.DeployedUncheckedVersion,
		}
	}
	if wait := round.HumanWait; wait != nil {
		waitDTO := toHumanWaitDTO(*wait)
		dto.HumanWait = &waitDTO
	}
	return dto
}

// releaseOutcomeOf reads an operation's outcome from its Attempt: a SUCCEEDED Attempt
// deployed; a FAILED one was voided when production said so, is unknown when it failed with
// an uncertain outcome (a person stopped waiting, or the wait expired), and was refused
// otherwise; an open Attempt is unknown once the Runtime stopped knowing whether production
// took it.
func releaseOutcomeOf(attempt domain.ToolAttempt) releaseOperationOutcome {
	switch attempt.Status {
	case domain.ToolAttemptSucceeded:
		return releaseOutcomeDeployed
	case domain.ToolAttemptFailed:
		if attempt.Error != nil && attempt.Error.Code == domain.ReleaseCodeVoided {
			return releaseOutcomeVoided
		}
		if attempt.Uncertain != nil && *attempt.Uncertain {
			return releaseOutcomeUnknown
		}
		return releaseOutcomeRefused
	case domain.ToolAttemptStarted, domain.ToolAttemptDispatched:
		if attempt.OutcomeUnknownSince != nil {
			return releaseOutcomeUnknown
		}
	}
	return releaseOutcomePending
}
