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
  /** Model capability the Model Selector filters on. Only meaningful for model widgets. */
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
}

export interface ModelMetadata {
  id: string;
  displayName: string;
  capabilities: string[];
  configSchema: JsonSchema;
}

export interface ToolMetadata {
  name: string;
  description: string;
  inputSchema: JsonSchema;
  outputSchema: JsonSchema;
  sideEffect: SideEffectPolicy;
  executionKind: ExecutionKind;
  poll?: PollPolicy | null;
}

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
   * Timestamps beyond the spec's minimal Run shape. The Run Rail derives start time and
   * duration from them; they are optional here so a minimal Snapshot still type-checks.
   */
  startedAt?: string;
  completedAt?: string | null;
  lastSeq?: number;
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
  /** `null` for a synchronous Attempt. */
  callbackBinding: CallbackBindingSummary | null;
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
}

/**
 * Why the Agent Loop stopped, as decided and persisted by the Backend. `null` while the
 * Agent Run is still going; Studio never infers a termination from the Turns it can see.
 */
export type AgentTermination =
  'FINAL_RESPONSE' | 'MAX_TURNS' | 'TIMEOUT' | 'MODEL_ERROR' | 'TOOL_ERROR' | 'INVALID_ACTION';

export type AgentTurnStatus = 'READY' | 'RUNNING' | 'COMPLETED' | 'FAILED';

/** Agent Action success is `SUCCEEDED`, matching NodeRun rather than Turn vocabulary. */
export type AgentActionStatus = 'READY' | 'RUNNING' | 'WAITING_CALLBACK' | 'SUCCEEDED' | 'FAILED';

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
  /** Absolute Agent deadline; entering WAITING_CALLBACK does not restart it. */
  deadline: string | null;
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

export interface AgentActionTrace {
  id: string;
  type: string;
  status: AgentActionStatus;
  startedAt: string | null;
  completedAt: string | null;
  error: ExecutionError | null;
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
}

/** One Turn with its Decision, Action and Tool Attempts, as persisted. */
export interface AgentTurnTrace {
  id: string;
  turnNo: number;
  status: AgentTurnStatus;
  startedAt: string | null;
  completedAt: string | null;
  error: ExecutionError | null;
  /** `null` before the Turn committed a Decision. */
  decision: AgentDecisionTrace | null;
  /** `null` for a Turn whose Decision created no Action. */
  action: AgentActionTrace | null;
  toolAttempts: ToolAttemptTrace[];
}

/**
 * Read-only projection of one Agent NodeRun's internals. `turns` is in persisted order,
 * by `turnNo`. A MANAGED_AGENT NodeRun's Node Detail carries no Attempts; its inner facts
 * are read here instead.
 */
export interface AgentTrace {
  agentRun: AgentRunTrace;
  turns: AgentTurnTrace[];
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

export type RunEventType =
  'RUN_CREATED' | 'RUN_PAUSED' | 'RUN_RESUMED' | 'RUN_COMPLETED' | 'RUN_FAILED';

export type NodeEventType =
  | 'NODE_READY'
  | 'NODE_STARTED'
  | 'NODE_RETRYING'
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
  | 'AGENT_FAILED';

/**
 * The 23 MVP Event types. RUN_CANCELLED, NODE_SKIPPED, NODE_CANCELLED and the
 * MODEL_STREAM_* types are Phase 2 and are absent on purpose.
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
