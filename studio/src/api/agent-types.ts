/**
 * Handwritten TypeScript mirror of the Agent Run, approval and Agent Trace part of the
 * Emberling Backend interface spec. It follows the same rules as `./types`: maintained by
 * hand, changed in the same pull request as the Backend DTOs and contract tests, and
 * holding server facts only. `./types` re-exports everything here, so importers keep one
 * entry point.
 */

import type {
  AgentGoal,
  CallbackBindingSummary,
  Cost,
  ExecutionError,
  JsonValue,
  TokenUsage,
} from './types';

/**
 * Why the Agent Loop stopped, as decided and persisted by the Backend. `null` while the
 * Agent Run is still going; Studio never infers a termination from the Turns it can see.
 */
export type AgentTermination =
  | 'FINAL_RESPONSE'
  | 'MAX_TURNS'
  | 'TIMEOUT'
  | 'MODEL_ERROR'
  | 'TOOL_ERROR'
  | 'INVALID_ACTION'
  | 'HUMAN_STOPPED';

export type AgentTurnStatus = 'READY' | 'RUNNING' | 'COMPLETED' | 'FAILED';

/**
 * Agent Action success is `SUCCEEDED`, matching NodeRun rather than Turn vocabulary.
 * `WAITING_APPROVAL` is a call a person must approve before it runs; `WAITING_HUMAN` is a
 * call whose outcome a person must confirm after it ran.
 */
export type AgentActionStatus =
  | 'READY'
  | 'RUNNING'
  | 'WAITING_CALLBACK'
  | 'WAITING_APPROVAL'
  | 'WAITING_HUMAN'
  | 'SUCCEEDED'
  | 'FAILED';

/** Why the Agent's clock is stopped: a person deciding, a release being reconciled, or a human wait. */
export type AgentPauseReason = 'APPROVAL' | 'RELEASE' | 'HUMAN';

/** What the Backend decided about a Tool call from the Tool's declared risk and the goal. */
export type AuthorizationDecision = 'ALLOW' | 'ASK' | 'DENY';

export type ApprovalStatus = 'PENDING' | 'APPROVED' | 'REJECTED' | 'EXPIRED';

/**
 * One person's decision on one Agent Action. `decidedBy` is the operator's identity name,
 * never a credential; `reason` is the bounded text given with the decision.
 */
export interface Approval {
  id: string;
  actionId: string;
  runId: string;
  agentRunId: string;
  status: ApprovalStatus;
  requestedAt: string;
  /** When an undecided approval expires; the Backend settles it, never this page. */
  deadline: string;
  decidedAt: string | null;
  decidedBy: string | null;
  reason: string | null;
}

/** An approval with what a person needs to decide it. */
export interface ApprovalView extends Approval {
  toolName: string;
  /** The committed call arguments the approval covers. */
  arguments: JsonValue | null;
  workflowId: string;
  nodeRunId: string;
}

export interface ApprovalListResponse {
  items: ApprovalView[];
}

export interface ApprovalDecisionRequest {
  decision: 'APPROVE' | 'REJECT';
  /** Required for a rejection, optional for an approval. */
  reason?: string;
}

/** The approval after the call; `repeated` means the same decision was already recorded. */
export interface ApprovalDecisionResponse {
  approval: Approval;
  repeated: boolean;
}

/**
 * One approval with the material a person decides on. `changeRequest` is the request the
 * call names through its `changeRequestId` argument, with its rendered Markdown and its
 * decision basis; `patch` is the full text of the workspace patch that request is about.
 * Both are `null` for a call that names no change request of this Agent Run. The list
 * keeps the `ApprovalView` shape; only the detail carries content.
 */
export interface ApprovalDetail extends ApprovalView {
  changeRequest: ApprovalChangeRequest | null;
  patch: ApprovalPatch | null;
}

/** A recorded change request with its Markdown content and resolved decision basis. */
export interface ApprovalChangeRequest extends ChangeRequestTrace {
  content: string;
  decisionBasis: DecisionBasisItem[];
}

/**
 * One cited item of a change request's decision basis: the statement, the Turn it names
 * and the Context Manifest item the reference resolves to. `turnId` is empty and `item`
 * `null` when the Backend can no longer resolve the reference.
 */
export interface DecisionBasisItem {
  statement: string;
  turnNo: number;
  turnId: string;
  manifestRef: string;
  item: ManifestItem | null;
}

/** A workspace patch summary with the cumulative patch text the trace never carries. */
export interface ApprovalPatch extends WorkspacePatchTrace {
  patch: string;
}

export type ToolAttemptStatus = 'STARTED' | 'DISPATCHED' | 'SUCCEEDED' | 'FAILED';

export type AgentDecisionKind = 'TOOL_CALL' | 'FINAL';

export interface AgentRunTrace {
  id: string;
  nodeRunId: string;
  /** `null` until the Agent Run reached a terminal outcome. */
  termination: AgentTermination | null;
  currentTurnNo: number;
  currentContextVersion: number;
  currentStateVersion: number;
  /**
   * Absolute Agent deadline while the clock runs. Entering WAITING_CALLBACK does not restart
   * it; a pause (`pausedAt` set) stops it, and resuming moves it later by the paused time,
   * so while paused it is not a countdown.
   */
  deadline: string | null;
  /** Set while the Agent's clock is stopped, with why. */
  pausedAt: string | null;
  pauseReason: AgentPauseReason | null;
  goal: AgentGoal | null;
  terminatedAt: string | null;
  error: ExecutionError | null;
}

/**
 * The committed Decision of one Turn. `tool` is `null` for a `FINAL` Decision.
 * `hasStatePatch` is the persisted fact that the Decision carried a State patch; the patch
 * itself belongs to Execution State, not to this projection.
 */
export interface AgentDecisionTrace {
  kind: AgentDecisionKind;
  tool: string | null;
  hasStatePatch: boolean;
}

/** Who won the conditional claim of a Turn or Action. */
export type ClaimSource = 'IMMEDIATE' | 'RECONCILER';

/** The path that delivered an Action's successful result. */
export type CompletionSource = 'SYNC_EXECUTION' | 'CALLBACK' | 'PROVIDER_POLL';

/**
 * The path that failed an Action. `RUNNER_LOST` means the process running a synchronous
 * Tool call stopped before the call returned.
 */
export type FailureSource =
  | 'SYNC_EXECUTION'
  | 'CALLBACK'
  | 'PROVIDER_POLL'
  | 'TIMEOUT'
  | 'RUNNER_LOST'
  | 'AUTHORIZATION'
  | 'HUMAN'
  | 'PROVIDER_REFUSAL';

export interface AgentActionTrace {
  id: string;
  type: string;
  status: AgentActionStatus;
  startedAt: string | null;
  completedAt: string | null;
  error: ExecutionError | null;
  /** Read from the Action's committed Events; `null` until the Action was claimed. */
  claimSource: ClaimSource | null;
  /** `null` unless the Action succeeded. */
  completionSource: CompletionSource | null;
  /** `null` unless the Action failed. */
  failureSource: FailureSource | null;
  /** The Backend's authorization decision for a Tool call; `null` before it was made and for FINAL. */
  authorization: AuthorizationDecision | null;
  /** Why a call was denied; `null` otherwise. */
  authorizationReason: string | null;
  /** The person's decision on a call whose authorization was ASK; `null` for every other Action. */
  approval: Approval | null;
  /** The release view of a `deploy_change` call; `null` for every other Action. */
  release: ReleaseActionTrace | null;
}

/**
 * What the Runtime knows about one release operation: `PENDING` while production has not
 * answered and nothing is in doubt, `UNKNOWN` once the dispatch got no usable answer, and
 * `DEPLOYED`, `VOIDED` or `REFUSED` once production answered for good.
 */
export type ReleaseOperationOutcome = 'PENDING' | 'UNKNOWN' | 'DEPLOYED' | 'VOIDED' | 'REFUSED';

/** One operation the Runtime asked production for, under its own operation key. */
export interface ReleaseOperationTrace {
  operationKey: string;
  toolAttemptId: string;
  attemptNo: number;
  status: ToolAttemptStatus;
  outcome: ReleaseOperationOutcome;
  /** When an unanswered operation hands its Action to a person. */
  confirmDeadline: string | null;
  outcomeUnknownSince: string | null;
  voidRequestedAt: string | null;
}

export type ReleaseOccupationStatus = 'ACTIVE' | 'RELEASED';

/** Why a deploy target occupation ended. */
export type ReleaseReason = 'DEPLOYED' | 'VOIDED' | 'REFUSED' | 'NOT_DEPLOYED';

/** The deploy target one approved release Action holds or held. */
export interface ReleaseOccupationTrace {
  id: string;
  targetKey: string;
  operationKey: string;
  status: ReleaseOccupationStatus;
  acquiredAt: string;
  releasedAt: string | null;
  releaseReason: ReleaseReason | null;
}

export type HumanWaitStatus = 'OPEN' | 'STOPPED' | 'EXPIRED' | 'RESOLVED';

/**
 * The wait opened when a release passed its confirm deadline without an answer. `closedBy`
 * is an operator's identity name, never a credential; `outcome` is the production answer or
 * the stop that closed it.
 */
export interface HumanWaitTrace {
  id: string;
  status: HumanWaitStatus;
  openedAt: string;
  deadline: string;
  closedAt: string | null;
  closedBy: string | null;
  outcome: string | null;
}

/**
 * The release view of a `deploy_change` Action: its operations in order, the target
 * occupation and the human wait. Operation keys, ids, statuses and times only.
 */
export interface ReleaseActionTrace {
  operations: ReleaseOperationTrace[];
  occupation: ReleaseOccupationTrace | null;
  humanWait: HumanWaitTrace | null;
}

/** One Tool Attempt. A retry preserves the earlier Attempt and adds a new `attemptNo`. */
export interface ToolAttemptTrace {
  id: string;
  toolName: string;
  attemptNo: number;
  status: ToolAttemptStatus;
  /** `null` for a synchronous Tool call. It never carries a token hash or credential. */
  callbackBinding: CallbackBindingSummary | null;
  startedAt: string | null;
  dispatchedAt: string | null;
  completedAt: string | null;
  error: ExecutionError | null;
  /** The charge the Provider reported for this Tool call; `null` when none is known. */
  cost: Cost | null;
  /** `generate_image` only: the photo the call works on, from its arguments. */
  photo?: ToolPhotoTrace;
  /** `generate_image` only: the named generation settings, from its arguments. */
  settings?: ToolSettingsTrace;
  /** `review_asset` only, once the Attempt `SUCCEEDED`: the verdict and its reasons. */
  review?: ToolReviewTrace;
  /** `sandbox_test` only, once the Attempt `SUCCEEDED`: the runner's verdict and counts. */
  test?: ToolTestTrace;
  /** `verify_patch` only, once the Attempt `SUCCEEDED`: the three checks and the log artifact. */
  verification?: ToolVerificationTrace;
  /** `deploy` only: the target from its arguments, the version once it `SUCCEEDED`. */
  deployment?: ToolDeploymentTrace;
  /** `generate_video` only, once the Attempt `SUCCEEDED` and the URL is credential-free. */
  video?: ToolVideoTrace;
  /** Members left out of this Attempt's projection, and why. */
  withheld?: WithheldToolField[];
}

/**
 * A photo by Asset ID. `position` (1-based) and `count` place it in the Run input's photo
 * set; both are absent when the set does not determine a single position.
 */
export interface ToolPhotoTrace {
  photoAssetId: string;
  position?: number;
  count?: number;
}

/**
 * The generation settings a `generate_image` call asked for. Each member is absent when
 * the call did not set it. A prompt over 500 bytes is cut and `promptTruncated` is true.
 */
export interface ToolSettingsTrace {
  prompt?: string;
  promptTruncated?: boolean;
  aspectRatio?: string;
  resolution?: string;
}

/** A `review_asset` verdict. `reasons` is absent when the review gave none. */
export interface ToolReviewTrace {
  passed: boolean;
  reasons?: string[];
}

export interface ToolTestTrace {
  passed: boolean;
  summary: { total: number; failed: number };
}

/**
 * A `verify_patch` result. `passed` holds only when all three checks hold. `logArtifactId`
 * names the text Execution Artifact holding the runner's log, served by the artifact
 * content route; `logTruncated` marks a log the runner cut at its bound.
 */
export interface ToolVerificationTrace {
  passed: boolean;
  testsPassed: boolean;
  scanClean: boolean;
  dependenciesClean: boolean;
  tests: { total: number; failed: number };
  logArtifactId: string;
  logTruncated: boolean;
}

export interface ToolDeploymentTrace {
  target?: { service: string; environment: string };
  version?: string;
}

export interface ToolVideoTrace {
  videoUrl: string;
}

/**
 * A projected member the Backend did not return. `TOO_LARGE`: over the Trace field bound,
 * never cut. `UNSAFE_URL`: a URL with userinfo, query or fragment, which could carry a
 * credential. `field` is a dotted path such as `video.videoUrl`.
 */
export interface WithheldToolField {
  field: string;
  reason: 'TOO_LARGE' | 'UNSAFE_URL';
}

/** What kind of material one Context Manifest item records. */
export type ManifestItemKind =
  'INSTRUCTIONS' | 'TASK_INPUT' | 'STATE' | 'TOOL_RESULT' | 'WORKSPACE_FILE' | 'ARTIFACT';

/** Why an item's content was left out of the model request. */
export type ManifestOmission = 'REFERENCE_ONLY';

/**
 * One material of a Turn's model request: an identifier, a version, a digest and a
 * length, never the material itself. For an included item `sha256` and `length` are those
 * of the request bytes; for an omitted `ARTIFACT` they describe the referenced content and
 * `ref` is the Artifact ID the artifact content route takes.
 */
export interface ManifestItem {
  kind: ManifestItemKind;
  label: string;
  ref: string;
  /** The source's version where it has one (state version, attempt number, patch sequence); `''` otherwise. */
  version: string;
  sha256: string;
  length: number;
  included: boolean;
  /** `null` for an included item. */
  omittedReason: ManifestOmission | null;
}

/** One Turn with its Decision, Action and Tool Attempts, as persisted. */
export interface AgentTurnTrace {
  id: string;
  turnNo: number;
  status: AgentTurnStatus;
  startedAt: string | null;
  completedAt: string | null;
  error: ExecutionError | null;
  /** Usage the model reported for this Turn; `null` when unreported or not yet known. */
  usage: TokenUsage | null;
  /** The charge the model reported for this Turn; `null` when unreported or not yet known. */
  cost: Cost | null;
  /** Read from the Turn's committed Event; `null` until the Turn was claimed. */
  claimSource: ClaimSource | null;
  /** `null` before the Turn committed a Decision. */
  decision: AgentDecisionTrace | null;
  /** `null` for a Turn whose Decision created no Action. */
  action: AgentActionTrace | null;
  toolAttempts: ToolAttemptTrace[];
  /** The Context Manifest written with the Turn's claim; `null` until the Turn was claimed. */
  manifest: ManifestItem[] | null;
}

/**
 * Read-only projection of one Agent NodeRun's internals. `turns` is in persisted order,
 * by `turnNo`. A MANAGED_AGENT NodeRun's Node Detail carries no Attempts; its inner facts
 * are read here instead.
 */
export interface AgentTrace {
  agentRun: AgentRunTrace;
  turns: AgentTurnTrace[];
  facts: AgentTraceFacts;
  generationBudget: GenerationBudgetTrace;
  /** The frozen workspace and its recorded patches; `null` when the Agent froze none. */
  workspace: AgentWorkspaceTrace | null;
  /** The change requests the Agent Run recorded, oldest first, without their content. */
  changeRequests: AgentTraceChangeRequests;
}

/**
 * The bounded change request list. `truncated` means the Agent Run recorded more requests
 * than `items` shows.
 */
export interface AgentTraceChangeRequests {
  items: ChangeRequestTrace[];
  truncated: boolean;
}

/**
 * One recorded change request: the patch it describes by digest, the verification fact it
 * rests on and the text Artifact holding its rendered content, with that content's digest.
 * The Markdown itself is read through the Artifact content route.
 */
export interface ChangeRequestTrace {
  id: string;
  patchDigest: string;
  verificationFactId: string;
  contentArtifactId: string;
  contentDigest: string;
  toolAttemptId: string;
  createdAt: string;
}

/**
 * The repository binding frozen when the Agent Run started, with the summaries of every
 * write recorded since, oldest first. A patch is named by its sequence number, digest and
 * touched paths; the patch text is never part of the trace. `patchesTruncated` means the
 * Agent Run holds more patches than `patches` shows.
 */
export interface AgentWorkspaceTrace {
  repositoryId: string;
  baseCommit: string;
  allowedPaths: string[];
  deployTarget: { service: string; environment: string } | null;
  patches: WorkspacePatchTrace[];
  patchesTruncated: boolean;
}

export interface WorkspacePatchTrace {
  seq: number;
  patchDigest: string;
  touchedPaths: string[];
  toolAttemptId: string;
  createdAt: string;
}

/**
 * One execution fact the Agent Run recorded from a Tool result. `bindings` hold only
 * references and digests; `verdict` and `basisFactId` are `null` for a fact that carries
 * no judgement.
 */
export interface ExecutionFactTrace {
  id: string;
  factType: string;
  subject: string;
  bindings: Record<string, unknown>;
  verdict: boolean | null;
  basisFactId: string | null;
  toolAttemptId: string;
  createdAt: string;
}

/**
 * The bounded fact ledger, oldest first. `truncated` means the Agent Run recorded more
 * facts than `items` shows; the stored facts are never cut.
 */
export interface AgentTraceFacts {
  items: ExecutionFactTrace[];
  truncated: boolean;
}

/**
 * `maxGenerationCalls` is the frozen limit, `null` when the Agent has none.
 * `generationCallsUsed` counts every generation call already made, whatever its outcome.
 */
export interface GenerationBudgetTrace {
  maxGenerationCalls: number | null;
  generationCallsUsed: number;
}
