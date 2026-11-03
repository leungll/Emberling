package domain

import "time"

// ReleaseOccupationStatus is the state of one release target occupation. ACTIVE is the
// only non-terminal state; a RELEASED occupation records why the target was given back.
type ReleaseOccupationStatus string

const (
	ReleaseOccupationActive   ReleaseOccupationStatus = "ACTIVE"
	ReleaseOccupationReleased ReleaseOccupationStatus = "RELEASED"
)

func (s ReleaseOccupationStatus) IsValid() bool {
	return s == ReleaseOccupationActive || s == ReleaseOccupationReleased
}

// ReleaseReason says why an occupation was released: the operation deployed, production
// confirmed it void, production refused it definitely, or the retries ran out without a
// deployment.
type ReleaseReason string

const (
	ReleaseReasonDeployed    ReleaseReason = "DEPLOYED"
	ReleaseReasonVoided      ReleaseReason = "VOIDED"
	ReleaseReasonRefused     ReleaseReason = "REFUSED"
	ReleaseReasonNotDeployed ReleaseReason = "NOT_DEPLOYED"
)

func (r ReleaseReason) IsValid() bool {
	switch r {
	case ReleaseReasonDeployed, ReleaseReasonVoided, ReleaseReasonRefused, ReleaseReasonNotDeployed:
		return true
	default:
		return false
	}
}

// ReleaseTargetKey is the persisted identity of a deploy target: "<service>/<environment>".
// Occupation uniqueness is decided on this key alone.
func ReleaseTargetKey(target WorkspaceDeployTarget) string {
	return target.Service + "/" + target.Environment
}

// ReleaseOccupation records that one approved release Action holds a deploy target. It is
// acquired in the approval transaction and released exactly once, in the transaction that
// settles the release. At most one occupation of a target is ACTIVE at any time; the
// database's partial unique index is the only arbiter of that, so a concurrent approval of
// the same target loses in the database and never here. OperationKey names the release
// operation currently holding the target; a re-release after a void updates it.
type ReleaseOccupation struct {
	ID           string                  `json:"id"`
	TargetKey    string                  `json:"targetKey"`
	RunID        string                  `json:"runId"`
	ActionID     string                  `json:"actionId"`
	OperationKey string                  `json:"operationKey"`
	Status       ReleaseOccupationStatus `json:"status"`
	AcquiredAt   time.Time               `json:"acquiredAt"`
	// ReleasedAt and ReleaseReason are written once with the RELEASED status.
	ReleasedAt    *time.Time     `json:"releasedAt"`
	ReleaseReason *ReleaseReason `json:"releaseReason"`
	// NextQueryAt, QueryCount and DeployedUncheckedVersion belong to the post-task
	// reconciliation of a target that was deployed without a health check. They are
	// persisted facts the release path creates but never writes.
	NextQueryAt              *time.Time `json:"-"`
	QueryCount               int        `json:"-"`
	DeployedUncheckedVersion *string    `json:"-"`
}

// HumanWaitStatus is the state of one human wait. OPEN is the only non-terminal state.
type HumanWaitStatus string

const (
	HumanWaitOpen     HumanWaitStatus = "OPEN"
	HumanWaitStopped  HumanWaitStatus = "STOPPED"
	HumanWaitExpired  HumanWaitStatus = "EXPIRED"
	HumanWaitResolved HumanWaitStatus = "RESOLVED"
)

func (s HumanWaitStatus) IsValid() bool {
	switch s {
	case HumanWaitOpen, HumanWaitStopped, HumanWaitExpired, HumanWaitResolved:
		return true
	default:
		return false
	}
}

// HumanWait is the wait opened for a release Action whose production confirmation did not
// arrive before its confirm deadline. It is opened OPEN and closed once: RESOLVED when
// production later gives a definite answer, STOPPED or EXPIRED by the human-stop path.
// UNIQUE (action_id) ties it to one Action, so a repeated deadline scan cannot open two.
type HumanWait struct {
	ID       string          `json:"id"`
	ActionID string          `json:"actionId"`
	RunID    string          `json:"runId"`
	Status   HumanWaitStatus `json:"status"`
	OpenedAt time.Time       `json:"openedAt"`
	Deadline time.Time       `json:"deadline"`
	// ClosedAt, ClosedBy and Outcome are written once with a terminal status. ClosedBy is
	// the authenticated operator's fixed identity name for a human stop and nil otherwise;
	// Outcome is the definite production answer or the stop that closed the wait.
	ClosedAt *time.Time `json:"closedAt"`
	ClosedBy *string    `json:"closedBy"`
	Outcome  *string    `json:"outcome"`
}
