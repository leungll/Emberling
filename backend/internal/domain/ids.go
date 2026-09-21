package domain

// Stable domain prefixes for externally visible IDs. Nothing routes on ID shape: the
// prefix exists so that an ID read from a log, Trace or API response is unambiguous.
const (
	IDPrefixWorkflow            = "wf"
	IDPrefixRun                 = "run"
	IDPrefixNodeRun             = "nr"
	IDPrefixNodeAttempt         = "attempt"
	IDPrefixEvent               = "evt"
	IDPrefixAsset               = "asset"
	IDPrefixArtifact            = "artifact"
	IDPrefixCallbackBinding     = "binding"
	IDPrefixAgentRun            = "ar"
	IDPrefixAgentTurn           = "turn"
	IDPrefixAgentDecision       = "decision"
	IDPrefixAgentAction         = "action"
	IDPrefixToolAttempt         = "tool_attempt"
	IDPrefixAgentContextVersion = "ctxv"
	IDPrefixAgentStateVersion   = "statev"
)

// IDGenerator produces new domain identifiers. It is an interface so that tests which
// assert on identity can inject a deterministic generator; the uuid-backed
// implementation lives outside domain.
type IDGenerator interface {
	NewID(prefix string) string
}
