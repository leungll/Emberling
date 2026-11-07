/**
 * Handwritten TypeScript mirror of the Emberling Backend interface spec.
 *
 * These types are maintained by hand. The MVP deliberately has no OpenAPI code
 * generation, so this file, the Backend transport DTOs and the Backend contract tests
 * must be changed in the same pull request. A type that drifts from the wire format is a
 * silent bug: nothing here is validated at runtime.
 *
 * Scope rules that this file enforces by omission:
 *   - No Phase 2 values. `CANCELLED` and `SKIPPED` are not part of the MVP status sets
 *     and must not be added here before the accepted scope changes.
 *   - No client-side Runtime vocabulary. Every status, Event and aggregate below is a
 *     server fact that Studio renders as-is.
 */

// ---------------------------------------------------------------------------
// JSON Schema
// ---------------------------------------------------------------------------

export type JsonValue =
  string | number | boolean | null | JsonValue[] | { [key: string]: JsonValue };

export type JsonObject = Record<string, JsonValue>;

/**
 * Minimal JSON Schema shape covering the subset the Backend emits for `configSchema`,
 * `runInputSchema` and Tool input/output schemas. It is intentionally permissive: the
 * Backend is the only validator that decides whether a value is acceptable.
 */
export interface JsonSchema {
  type?: 'object' | 'array' | 'string' | 'number' | 'integer' | 'boolean' | 'null';
  title?: string;
  description?: string;
  properties?: Record<string, JsonSchema>;
  required?: string[];
  additionalProperties?: boolean | JsonSchema;
  items?: JsonSchema;
  minItems?: number;
  maxItems?: number;
  enum?: JsonValue[];
  default?: JsonValue;
  format?: string;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
}

// ---------------------------------------------------------------------------
// Definition contract
// ---------------------------------------------------------------------------

export interface Position {
  x: number;
  y: number;
}

export type BackoffKind = 'FIXED' | 'EXPONENTIAL';

export interface ExecutionPolicy {
  timeoutMs: number;
  maxAttempts: number;
  backoff: BackoffKind;
}

export interface Node {
  id: string;
  type: string;
  name: string;
  /** Canvas geometry only. Execution order comes from edges, never from position. */
  position: Position;
  config: JsonObject;
  executionPolicy?: ExecutionPolicy;
}

export interface Edge {
  id: string;
  source: string;
  sourceHandle: string;
  target: string;
  targetHandle: string;
}

export type ValidationStatus = 'VALID';

export interface Validation {
  status: ValidationStatus;
  /** Identifies the whole Backend validation contract, including runInputSchema generation. */
  validatorVersion: string;
  validatedAt: string;
}

/**
 * The frozen Run input contract of one Definition version. The Backend generates it
 * deterministically from the Input Nodes; clients never submit or edit it.
 */
export type RunInputSchema = JsonSchema;

/** The deployment target an agent node's workspace releases to. */
export interface AgentDeployTarget {
  service: string;
  environment: string;
}

/** The repository binding of an agent node. The Backend validates it against the registered repositories. */
export interface AgentWorkspaceConfig {
  repositoryId: string;
  /** Full 40-character lowercase commit hash the workspace starts from. */
  baseCommit: string;
  /** Relative paths inside the repository the Agent may change; no leading slash, no `..`. */
  allowedPaths: string[];
  deployTarget?: AgentDeployTarget;
}

/** How far an Agent Run may take a repair: prepare the patch only, or also deploy it. */
export type AgentGoal = 'PREPARE_ONLY' | 'FIX_AND_DEPLOY';

/**
 * The agent node config keys Studio knows about. Studio stores a node's config as a
 * JsonObject and never drops keys it does not render, so a Definition saved from the
 * editor keeps every one of these as the Backend returned it.
 */
export interface AgentNodeConfig {
  instructions: string;
  modelId: string;
  allowedTools: string[];
  maxTurns: number;
  timeoutMs: number;
  modelConfig?: JsonObject;
  imageModelId?: string;
  reviewModelId?: string;
  workspace?: AgentWorkspaceConfig;
  /** JSON Pointer into the node input that selects the AgentGoal. */
  goalPointer?: string;
  approvalTimeoutMs?: number;
  releaseConfirmTimeoutMs?: number;
  voidGraceMs?: number;
  maxReleaseRetries?: number;
  humanWaitTimeoutMs?: number;
}

export interface Definition {
  workflowId: string;
  version: number;
  name: string;
  description: string;
  nodes: Node[];
  edges: Edge[];
  runInputSchema: RunInputSchema;
  validation: Validation;
  createdAt: string;
}

export interface DefinitionLastRun {
  id: string;
  status: RunStatus;
  createdAt: string;
  /** Only the stable code of the Run error summary; `null` unless the Run is `FAILED`. */
  error: RunErrorCode | null;
  /** The step the Run error summary describes; `null` unless the Run is `FAILED`. */
  failedStep: RunStepRef | null;
}

export interface DefinitionListItem {
  workflowId: string;
  name: string;
  description: string;
  latestVersion: number;
  updatedAt: string;
  /** `null` for a Definition that has never run. */
  lastRun: DefinitionLastRun | null;
}

export interface DefinitionListResponse {
  items: DefinitionListItem[];
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

export interface ValidationError {
  code: string;
  /** Path into the Definition request body, for example `edges[2]`. */
  path: string;
  /** Present for node-level errors; omitted for whole-graph DAG errors. */
  nodeId?: string;
  message: string;
}

export type ValidateResponse =
  { valid: true; runInputSchema: RunInputSchema } | { valid: false; errors: ValidationError[] };

// ---------------------------------------------------------------------------
// Registry metadata
// ---------------------------------------------------------------------------

export type PortDataType = 'text' | 'image' | 'json' | 'any';

export interface PortMetadata {
  name: string;
  dataType: PortDataType;
  required: boolean;
}

export type ExecutionKind = 'SYNC' | 'ASYNC' | 'MANAGED_AGENT';

export type SideEffectKind = 'NONE' | 'EXTERNAL';

export type IdempotencyMode = 'SAFE' | 'KEYED' | 'UNKNOWN';

export interface SideEffectPolicy {
  kind: SideEffectKind;
  idempotency: IdempotencyMode;
}

export type UiGroup = 'BASIC' | 'MODEL' | 'MODEL_PARAMETERS';

export type UiWidget =
  'DEFAULT' | 'TEXTAREA' | 'PROMPT_EDITOR' | 'SELECT' | 'MODEL_SELECTOR' | 'TOOL_SELECTOR';

/**
 * UI-only layout metadata. It never carries defaults or validation rules: those belong
 * to `configSchema` alone.
 */
export interface UiSchemaField {
  path: string;
  order: number;
  group: UiGroup;
  widget: UiWidget;
  /**
   * Model capability the Model Selector filters on. Only meaningful for model widgets; absent,
   * the selector lists every registered model and the Backend checks the capability on save.
   */
  capability?: string;
}

export interface UiSchema {
  fields: UiSchemaField[];
}

/**
 * Registered Provider polling bounds for an ASYNC Node Type or Tool. Absent when the
 * registration declares no polling; Studio only displays it.
 */
export interface PollPolicy {
  intervalMs: number;
  maxPolls: number;
}

/** Which document of a Tool call a fact pointer reads. */
export type FactPointerSource = 'ARGUMENTS' | 'RESULT';

/** An RFC 6901 JSON Pointer into a Tool call's arguments or result. */
export interface FactPointer {
  source: FactPointerSource;
  pointer: string;
}

/** The execution fact a Tool's successful result establishes. Studio only displays it. */
export interface FactProduction {
  factType: string;
  subjectPointer: FactPointer;
  bindArguments: Record<string, FactPointer>;
  verdictPointer?: FactPointer | null;
  basisFactType?: string;
}

/** A committed execution fact an Action of the Tool needs before it is claimed. */
export interface FactRequirement {
  factType: string;
  /** JSON Pointer into the call's arguments. */
  subjectArgument: string;
  matchBindings: string[];
  requireVerdict?: boolean | null;
}

export interface NodeMetadata {
  type: string;
  displayName: string;
  category: string;
  executionKind: ExecutionKind;
  inputs: PortMetadata[];
  outputs: PortMetadata[];
  configSchema: JsonSchema;
  uiSchema: UiSchema;
  sideEffect: SideEffectPolicy;
  poll?: PollPolicy | null;
  /** Execution fact types of the current Run this Node Type reads. */
  factInputs: string[];
}

export interface ModelMetadata {
  id: string;
  displayName: string;
  /** Optional one-line English note shown with the model; omitted when the model has none. */
  description?: string;
  capabilities: string[];
  configSchema: JsonSchema;
  /** Stable ID of the asynchronous Provider that runs this model's tasks; omitted for a model served only synchronously. */
  providerId?: string;
}

export interface ToolMetadata {
  name: string;
  description: string;
  inputSchema: JsonSchema;
  outputSchema: JsonSchema;
  sideEffect: SideEffectPolicy;
  executionKind: ExecutionKind;
  poll?: PollPolicy | null;
  /** Absent when the Tool produces no execution fact. */
  produces?: FactProduction | null;
  requires: FactRequirement[];
  countsTowardGenerationLimit: boolean;
  /**
   * True when the Tool runs with the image model selected on its Agent node
   * (`imageModelId`); the Backend then requires that selection on Save.
   */
  usesImageModel: boolean;
  /**
   * True when the Tool reviews with the review model optionally selected on its Agent node
   * (`reviewModelId`); without a selection it applies the mock review policy, unless
   * `requiresReviewModel` is set.
   */
  usesReviewModel: boolean;
  /**
   * The model capability the selected review model must declare for this Tool, such as
   * `vision_input` for an image review or `structured_decision` for a text review. Absent
   * when the Tool uses no review model.
   */
  reviewModelCapability?: string;
  /**
   * True when the Tool gives an independent second opinion: its Agent node must select a
   * review model, and that model must differ from the node's own `modelId`.
   */
  requiresReviewModel: boolean;
  /**
   * The authorization the Tool's Actions need. Absent means LOW risk: the Backend treats an
   * undeclared authorization as LOW.
   */
  authorization?: ToolAuthorization;
  /** The records a result of this Tool may carry; a result carrying any other kind is invalid. */
  records: ToolRecordKind[];
  /** True when the Tool runs against the workspace bound on its Agent node. */
  usesWorkspace: boolean;
  /**
   * JSON Pointer into the call's arguments naming the repository path the Tool writes; the
   * Backend refuses a path outside the Agent's allowed paths before the Tool runs. Absent
   * when the Tool declares no single written path.
   */
  workspaceWritePath?: string;
  /**
   * JSON Pointer into the arguments naming the workspace patch digest a call acts on; the
   * Backend refuses a call naming a digest other than the current patch's with
   * `STALE_PATCH`. Absent when the Tool declares none.
   */
  workspacePatchDigestPath?: string;
  /** True when an uncertain asynchronous result of this Tool is reconciled by polling. */
  reconcilable: boolean;
  /**
   * JSON Pointer into a successful result naming the material object a Tool that reads
   * text by range returns; the Backend records such a result in the Turn's Context
   * Manifest as a `MATERIAL_RANGE` item. Absent when the Tool returns no material.
   */
  materialPointer?: string;
  /**
   * True when an unmet fact requirement is returned to the Agent as a REJECTED tool message
   * instead of ending the Agent Run.
   */
  returnUnmetRequirement: boolean;
  /**
   * Fact types whose failed verdicts bound this Tool's calls: once the Agent Run has
   * collected more failed facts of one of them than its configuration allows, further
   * calls are refused before any Attempt exists. Empty when the Tool is never bounded.
   */
  countedFailedFacts: string[];
}

export type ToolAuthorizationRisk = 'LOW' | 'PRODUCTION';

export interface ToolAuthorization {
  risk: ToolAuthorizationRisk;
  reversible: boolean;
}

export type ToolRecordKind = 'WORKSPACE_PATCH' | 'CHANGE_REQUEST';

export interface NodeTypeListResponse {
  items: NodeMetadata[];
}

export interface ModelListResponse {
  items: ModelMetadata[];
}

export interface ToolListResponse {
  items: ToolMetadata[];
}

// ---------------------------------------------------------------------------
// Asset
// ---------------------------------------------------------------------------

/** Immutable reference stored in `Run.input`. It never exposes an internal storage key. */
export interface AssetRef {
  assetId: string;
  mediaType: string;
  sizeBytes: number;
  sha256: string;
}

/** Immutable reference to a Backend-produced Artifact (the ImageRef `ARTIFACT` branch). */
export interface ArtifactRef {
  artifactId: string;
  mediaType: string;
  sizeBytes: number;
  sha256: string;
}

export type ImageSource = 'ASSET' | 'ARTIFACT' | 'EXTERNAL';

/**
 * Mirrors `backend/internal/domain/imageref.go`. `source` selects exactly one of `asset`,
 * `artifact`, or `uri`; the other reference fields stay unset for that branch. Never carries
 * a signed URL, Secret, or credential.
 */
export interface ImageRef {
  source: ImageSource;
  asset?: AssetRef;
  artifact?: ArtifactRef;
  uri?: string;
  mediaType?: string;
  width?: number;
  height?: number;
}

// ---------------------------------------------------------------------------
// Execution facts
// ---------------------------------------------------------------------------

/**
 * Aggregated status of one Execution, derived by the Backend from all NodeRuns. A Run has
 * no `SUCCEEDED`: its success terminal is `COMPLETED`. Studio must never compute this
 * value from NodeRun statuses.
 */
export type RunStatus = 'RUNNING' | 'PAUSED' | 'COMPLETED' | 'FAILED';

export type NodeRunStatus = 'READY' | 'RUNNING' | 'WAITING_CALLBACK' | 'SUCCEEDED' | 'FAILED';

export type NodeAttemptStatus = 'STARTED' | 'DISPATCHED' | 'SUCCEEDED' | 'FAILED';

export interface ExecutionError {
  code: string;
  message: string;
  details?: JsonValue;
}

export interface TokenUsage {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
}

/**
 * The charge a Provider reported for one call, recorded with the call's result and never
 * changed. `amount` is decimal text so no precision is lost; `currency` is the ISO 4217
 * code the Provider bills in; `source` is the Model ID of the call. A call whose charge
 * is not known carries `null` instead, never a zero.
 */
export interface Cost {
  amount: string;
  currency: string;
  source: string;
}

/** The known charges of one currency summed, as decimal text. */
export interface CostTotal {
  amount: string;
  currency: string;
}

/**
 * A Run's read-time cost: its known charges summed per currency, and the number of
 * finished external calls whose charge is not known. The two are never merged: an
 * unknown charge is a count, not an estimate, and a reported zero is a known charge.
 */
export interface RunCost {
  totals: CostTotal[];
  unknownCalls: number;
}

export interface Run {
  id: string;
  workflowId: string;
  definitionVersion: number;
  status: RunStatus;
  input: JsonObject;
  /** Formed only once the single Output Node succeeded; `null` in every other state. */
  output: JsonValue | null;
  error: ExecutionError | null;
  /**
   * Read-time total of the NodeRuns' `tokenUsage`, summed field by field; `null` when no
   * NodeRun reported usage. It is a counter, never an input to the Run status.
   */
  tokenUsage: TokenUsage | null;
  /**
   * Timestamps beyond the spec's minimal Run shape. The Run Rail derives start time and
   * duration from them; they are optional here so a minimal Snapshot still type-checks.
   */
  startedAt?: string;
  completedAt?: string | null;
  lastSeq?: number;
}

// ---------------------------------------------------------------------------
// Run list
// ---------------------------------------------------------------------------

/** The code of a Run error summary. Lists never carry the message or details. */
export interface RunErrorCode {
  code: string;
}

/** One node of the Definition version a Run is bound to. */
export interface RunStepRef {
  nodeId: string;
  nodeName: string;
}

/** A NodeRun of a `PAUSED` Run that is waiting for an external result. */
export interface RunWaitingStep extends RunStepRef {
  waitingAt: string | null;
}

export interface RunListItem {
  id: string;
  workflowId: string;
  /** The Workflow's current name. */
  workflowName: string;
  definitionVersion: number;
  /** The Workflow's latest version; a smaller `definitionVersion` is an older version. */
  latestVersion: number;
  status: RunStatus;
  /** Equal to `startedAt`: a Run starts in the transaction that creates it. */
  createdAt: string;
  startedAt: string;
  completedAt: string | null;
  error: RunErrorCode | null;
  failedStep: RunStepRef | null;
  /** Filled only for a `PAUSED` Run, ordered by `waitingAt`; empty otherwise. */
  waitingSteps: RunWaitingStep[];
  cost: RunCost;
}

/** Server-side counts for the current workflow, time and code filters, ignoring `status`. */
export interface RunStatusCounts {
  all: number;
  running: number;
  paused: number;
  completed: number;
  failed: number;
}

export interface RunListResponse {
  /** Newest first. */
  items: RunListItem[];
  /** Continues to older Runs; `null` on the last page. */
  nextCursor: string | null;
  /** Continues to newer Runs; `null` on the first page. */
  prevCursor: string | null;
  counts: RunStatusCounts;
}

export interface RunListQuery {
  workflowId?: string;
  /** Keeps Runs bound to this version of `workflowId`; refused (400) without `workflowId`. */
  definitionVersion?: number;
  status?: RunStatus;
  /** RFC 3339; keeps Runs started at or after this instant. */
  startedAfter?: string;
  /** A full Run ID, or 8 to 64 letters or digits from the start of the Run ID after `run_`. */
  code?: string;
  /** A `nextCursor` or `prevCursor` issued for the same filters. */
  cursor?: string;
  /** 1 to 100; the Backend defaults to 20. */
  limit?: number;
}

export interface NodeRun {
  id: string;
  runId: string;
  nodeId: string;
  nodeType: string;
  status: NodeRunStatus;
  input: JsonValue | null;
  output: JsonValue | null;
  error: ExecutionError | null;
  readyAt: string;
  startedAt: string | null;
  waitingAt: string | null;
  completedAt: string | null;
  latencyMs: number | null;
  tokenUsage: TokenUsage | null;
  /** Retry bookkeeping. Absent from the spec's Node Detail example; present on the wire. */
  attemptCount?: number;
  nextAttemptAt?: string | null;
  /**
   * When a READY step was first refused a slot of its Provider and which Provider that
   * was. Both are set together, stay set once the step is admitted, and are `null` for a
   * step that never waited. Present on the wire.
   */
  capacityDeferredAt?: string | null;
  capacityProviderId?: string | null;
}

/**
 * The live place of a queued step in its Provider's queue, read with the step: `held` of
 * `limit` slots are in use, and `position` 1 is admitted next. The NODE_WAITING_CAPACITY
 * Event keeps the position at the first refusal; this one moves as other Runs finish.
 */
export interface CapacityWait {
  providerId: string;
  limit: number;
  held: number;
  position: number;
}

/** Projection of the Callback Binding. It never carries a token hash or credential. */
export interface CallbackBindingSummary {
  id: string;
  providerId: string;
  externalTaskId: string;
  createdAt?: string;
}

export interface NodeAttempt {
  id: string;
  nodeRunId?: string;
  attemptNo: number;
  status: NodeAttemptStatus;
  input: JsonValue | null;
  result: JsonValue | null;
  startedAt: string;
  /** Absolute: entering WAITING_CALLBACK does not restart the clock. */
  deadlineAt: string | null;
  dispatchedAt: string | null;
  completedAt: string | null;
  error: ExecutionError | null;
  /**
   * For a `FAILED` Attempt, whether its external outcome was uncertain when it failed.
   * `null` on any other Attempt, and on one that failed before this was recorded.
   */
  uncertain: boolean | null;
  /** `null` for a synchronous Attempt. */
  callbackBinding: CallbackBindingSummary | null;
  /** The charge the Provider reported for this Attempt; `null` when none is known. */
  cost: Cost | null;
}

/**
 * Consistent read of the Run and its NodeRuns. `lastSeq` is taken in the same read, and
 * is the `afterSeq` the client hands to the first SSE connection.
 */
export interface RunSnapshot {
  run: Run;
  nodeRuns: NodeRun[];
  lastSeq: number;
}

/** NodeRun plus its Attempts, ordered by `attemptNo`. MANAGED_AGENT NodeRuns have none. */
export interface NodeRunDetail {
  nodeRun: NodeRun;
  attempts: NodeAttempt[];
  /** `null` unless the step is READY and waiting for a Provider slot. */
  capacityWait?: CapacityWait | null;
}

// ---------------------------------------------------------------------------
// Agent Runs, approvals and Agent Trace
// ---------------------------------------------------------------------------

export * from './agent-types';

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

export type RunEventType =
  'RUN_CREATED' | 'RUN_PAUSED' | 'RUN_RESUMED' | 'RUN_COMPLETED' | 'RUN_FAILED';

export type NodeEventType =
  | 'NODE_READY'
  | 'NODE_STARTED'
  | 'NODE_RETRYING'
  | 'NODE_WAITING_CAPACITY'
  | 'NODE_DISPATCHED'
  | 'NODE_CALLBACK_RECEIVED'
  | 'NODE_COMPLETED'
  | 'NODE_FAILED';

export type AgentEventType =
  | 'AGENT_STARTED'
  | 'AGENT_TURN_READY'
  | 'AGENT_TURN_STARTED'
  | 'AGENT_DECISION_COMMITTED'
  | 'AGENT_ACTION_STARTED'
  | 'AGENT_ACTION_WAITING'
  | 'AGENT_ACTION_COMPLETED'
  | 'AGENT_ACTION_FAILED'
  | 'AGENT_STATE_UPDATED'
  | 'AGENT_COMPLETED'
  | 'AGENT_FAILED'
  | 'AGENT_ACTION_WAITING_APPROVAL'
  | 'APPROVAL_DECIDED'
  | 'AGENT_PAUSED'
  | 'AGENT_RESUMED'
  | 'RELEASE_OUTCOME_UNKNOWN'
  | 'RELEASE_VOIDED'
  | 'AGENT_ACTION_WAITING_HUMAN'
  | 'AGENT_COMPLETION_EVALUATED';

/**
 * The 32 Event types the Backend may write. RUN_CANCELLED, NODE_SKIPPED, NODE_CANCELLED and
 * the MODEL_STREAM_* types are Phase 2 and are absent on purpose.
 */
export type EventType = RunEventType | NodeEventType | AgentEventType;

/**
 * Append-only proof of a committed state change. `seq` is strictly increasing within one
 * Run and is also the SSE event id. Payloads carry bounded summaries only.
 */
export interface RunEvent {
  id: string;
  runId: string;
  /** `null` for Run-level events. */
  nodeRunId: string | null;
  type: EventType;
  seq: number;
  timestamp: string;
  payload: JsonObject;
}

export interface EventListResponse {
  items: RunEvent[];
}

// ---------------------------------------------------------------------------
// Requests and responses
// ---------------------------------------------------------------------------

export interface CreateDefinitionRequest {
  name: string;
  description: string;
  nodes: Node[];
  edges: Edge[];
}

/**
 * A save always carries the version it was derived from. When `baseVersion` is not the
 * current latest, the Backend answers `409 VERSION_CONFLICT` and creates no version.
 */
export interface SaveDefinitionRequest extends CreateDefinitionRequest {
  baseVersion: number;
}

export interface CreateRunRequest {
  workflowId: string;
  /** Explicit: a Run binds to one immutable Definition version for its whole life. */
  definitionVersion: number;
  input: JsonObject;
}

export interface CreateRunResponse {
  id: string;
  workflowId: string;
  definitionVersion: number;
  status: RunStatus;
  lastSeq: number;
}

/**
 * Optional request settings for `POST /runs`. `idempotencyKey` is sent as the
 * `Idempotency-Key` header: 1 to 128 printable ASCII characters, scoped to the Workflow.
 */
export interface CreateRunOptions {
  idempotencyKey?: string;
}

/**
 * `CreateRunResponse` plus whether the Backend answered with an existing Run (200 with
 * `Idempotent-Replayed: true`) instead of creating one (201). On a replay the body reports
 * that Run's current status.
 */
export interface CreateRunResult extends CreateRunResponse {
  replayed: boolean;
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

export type ErrorCode =
  | 'VALIDATION_FAILED'
  | 'INVALID_CALLBACK_PAYLOAD'
  | 'INVALID_CALLBACK_CREDENTIAL'
  | 'DEFINITION_NOT_FOUND'
  | 'ASSET_NOT_FOUND'
  | 'RUN_NOT_FOUND'
  | 'NODE_RUN_NOT_FOUND'
  | 'INVALID_STATE_TRANSITION'
  | 'VERSION_CONFLICT'
  | 'IDEMPOTENCY_KEY_MISMATCH'
  | 'RUNTIME_BINDING_UNAVAILABLE'
  | 'PAYLOAD_TOO_LARGE'
  | 'DAG_HAS_CYCLE'
  | 'INCOMPATIBLE_EDGE'
  | 'DEPENDENCY_UNAVAILABLE'
  | 'INTERNAL';

export interface ApiError {
  error: {
    /** A code outside ErrorCode is still surfaced verbatim rather than being swallowed. */
    code: ErrorCode | (string & {});
    message: string;
    details?: JsonValue;
  };
}

/**
 * The 200 body of the Backend's liveness (`/health`) and readiness (`/ready`) probes,
 * served at the root path rather than under `/api`. `version` and `commit` are build
 * labels; either is `"unknown"` when the build did not record it.
 */
export interface BackendProbeResponse {
  status: 'ok' | 'ready';
  version: string;
  commit: string;
}
