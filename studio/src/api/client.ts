import type {
  AgentTrace,
  ApiError,
  ApprovalDecisionRequest,
  ApprovalDecisionResponse,
  ApprovalDetail,
  ApprovalListResponse,
  ApprovalStatus,
  ApprovalView,
  AssetRef,
  CreateDefinitionRequest,
  CreateRunOptions,
  CreateRunRequest,
  CreateRunResponse,
  CreateRunResult,
  Definition,
  DefinitionListItem,
  DefinitionListResponse,
  EventListResponse,
  ModelListResponse,
  ModelMetadata,
  NodeMetadata,
  NodeRunDetail,
  NodeTypeListResponse,
  RunEvent,
  RunListQuery,
  RunListResponse,
  RunSnapshot,
  SaveDefinitionRequest,
  ToolListResponse,
  ToolMetadata,
  ValidateResponse,
} from './types';

export const API_BASE = '/api';

/**
 * A non-2xx response carrying the Backend error envelope. The envelope is preserved whole
 * so the UI can show the stable `code` instead of a rewritten message.
 */
export class ApiRequestError extends Error {
  readonly status: number;
  readonly body: ApiError;

  constructor(status: number, body: ApiError) {
    super(body.error.message || `request failed with status ${status}`);
    this.name = 'ApiRequestError';
    this.status = status;
    this.body = body;
  }

  get code(): string {
    return this.body.error.code;
  }
}

function isApiError(value: unknown): value is ApiError {
  if (typeof value !== 'object' || value === null) return false;
  const candidate = (value as { error?: unknown }).error;
  if (typeof candidate !== 'object' || candidate === null) return false;
  return typeof (candidate as { code?: unknown }).code === 'string';
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  headers?: Record<string, string>;
  /** Receives the response headers of a successful request, before the body is returned. */
  onHeaders?: (headers: Headers) => void;
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal, headers, onHeaders } = options;

  const init: RequestInit = { method };
  if (signal) init.signal = signal;
  if (body instanceof FormData) {
    // Let the browser set Content-Type with its multipart boundary.
    init.body = body;
  } else if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }
  if (headers)
    init.headers = { ...(init.headers as Record<string, string> | undefined), ...headers };

  const response = await fetch(`${API_BASE}${path}`, init);
  const text = await response.text();

  let parsed: unknown = undefined;
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text) as unknown;
    } catch {
      parsed = undefined;
    }
  }

  if (!response.ok) {
    throw new ApiRequestError(
      response.status,
      isApiError(parsed)
        ? parsed
        : {
            error: {
              code: 'INTERNAL',
              message: text.slice(0, 500) || `request failed with status ${response.status}`,
            },
          },
    );
  }

  onHeaders?.(response.headers);
  return parsed as T;
}

// --- Readiness --------------------------------------------------------------

/** Whether the Backend answers: ready (200), still starting (503), or not reachable at all. */
export type Readiness = 'ready' | 'starting' | 'unreachable';

/**
 * Reads the Backend's readiness probe, which lives beside /api rather than under it. Any
 * other status, or a request that fails, means Studio cannot rely on the Backend.
 */
export async function getReadiness(signal?: AbortSignal): Promise<Readiness> {
  try {
    const response = await fetch('/ready', signal ? { signal } : undefined);
    if (response.ok) return 'ready';
    return response.status === 503 ? 'starting' : 'unreachable';
  } catch (error) {
    if (signal?.aborted) throw error;
    return 'unreachable';
  }
}

// --- Definition -------------------------------------------------------------

export function listDefinitions(signal?: AbortSignal): Promise<DefinitionListItem[]> {
  return request<DefinitionListResponse>('/definitions', { ...(signal ? { signal } : {}) }).then(
    (r) => r.items,
  );
}

export function getDefinition(workflowId: string, signal?: AbortSignal): Promise<Definition> {
  return request<Definition>(`/definitions/${encodeURIComponent(workflowId)}`, {
    ...(signal ? { signal } : {}),
  });
}

export function getDefinitionVersion(
  workflowId: string,
  version: number,
  signal?: AbortSignal,
): Promise<Definition> {
  return request<Definition>(`/definitions/${encodeURIComponent(workflowId)}/versions/${version}`, {
    ...(signal ? { signal } : {}),
  });
}

/** Creates the first immutable version. It carries no `baseVersion`. */
export function createDefinition(body: CreateDefinitionRequest): Promise<Definition> {
  return request<Definition>('/definitions', { method: 'POST', body });
}

/** Creates a new immutable version. Never overwrites the version named by `baseVersion`. */
export function saveDefinition(
  workflowId: string,
  body: SaveDefinitionRequest,
): Promise<Definition> {
  return request<Definition>(`/definitions/${encodeURIComponent(workflowId)}`, {
    method: 'PUT',
    body,
  });
}

/** Validates without saving. The Backend runs the same chain that Save runs. */
export function validateDefinition(body: CreateDefinitionRequest): Promise<ValidateResponse> {
  return request<ValidateResponse>('/definitions/validate', { method: 'POST', body });
}

// --- Registry ---------------------------------------------------------------

export function listNodeTypes(signal?: AbortSignal): Promise<NodeMetadata[]> {
  return request<NodeTypeListResponse>('/node-types', { ...(signal ? { signal } : {}) }).then(
    (r) => r.items,
  );
}

export function listModels(signal?: AbortSignal): Promise<ModelMetadata[]> {
  return request<ModelListResponse>('/models', { ...(signal ? { signal } : {}) }).then(
    (r) => r.items,
  );
}

export function listTools(signal?: AbortSignal): Promise<ToolMetadata[]> {
  return request<ToolListResponse>('/tools', { ...(signal ? { signal } : {}) }).then(
    (r) => r.items,
  );
}

// --- Asset --------------------------------------------------------------------

/**
 * Uploads one image as the `file` multipart part (the single accepted part name)
 * and returns the immutable `AssetRef` the Backend commits. The caller writes this value
 * into `Run.input`; Studio never invents an `assetId` or writes a browser-local URL there.
 */
export function uploadAsset(file: File, signal?: AbortSignal): Promise<AssetRef> {
  const formData = new FormData();
  formData.append('file', file);
  return request<AssetRef>('/assets', {
    method: 'POST',
    body: formData,
    ...(signal ? { signal } : {}),
  });
}

// --- Run --------------------------------------------------------------------

/**
 * Starts a Run. With `options.idempotencyKey`, a resubmission the key already answered
 * returns that Run with `replayed: true` instead of creating another; the same key with
 * different content fails with `IDEMPOTENCY_KEY_MISMATCH`.
 */
export async function createRun(
  body: CreateRunRequest,
  options: CreateRunOptions = {},
): Promise<CreateRunResult> {
  let replayed = false;
  const created = await request<CreateRunResponse>('/runs', {
    method: 'POST',
    body,
    ...(options.idempotencyKey !== undefined
      ? { headers: { 'Idempotency-Key': options.idempotencyKey } }
      : {}),
    onHeaders: (headers) => {
      replayed = headers.get('Idempotent-Replayed') === 'true';
    },
  });
  return { ...created, replayed };
}

/**
 * One page of the Run list, newest first, with server-computed status counts. Cursors are
 * opaque and bound to the filters they were issued for.
 */
export function listRuns(query: RunListQuery = {}, signal?: AbortSignal): Promise<RunListResponse> {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query) as [string, string | number | undefined][]) {
    if (value !== undefined && value !== '') params.set(key, String(value));
  }
  const encoded = params.toString();
  const suffix = encoded === '' ? '' : `?${encoded}`;
  return request<RunListResponse>(`/runs${suffix}`, { ...(signal ? { signal } : {}) });
}

export function getRun(runId: string, signal?: AbortSignal): Promise<RunSnapshot> {
  return request<RunSnapshot>(`/runs/${encodeURIComponent(runId)}`, {
    ...(signal ? { signal } : {}),
  });
}

/**
 * NodeRun and its Attempts, ordered by `attemptNo`, plus each Attempt's Callback Binding
 * summary. A read-only Trace projection; it never advances or infers execution state.
 */
export function getNodeRunDetail(
  runId: string,
  nodeRunId: string,
  signal?: AbortSignal,
): Promise<NodeRunDetail> {
  return request<NodeRunDetail>(
    `/runs/${encodeURIComponent(runId)}/nodes/${encodeURIComponent(nodeRunId)}`,
    { ...(signal ? { signal } : {}) },
  );
}

/**
 * Agent Run plus its Turns in persisted order, each with its committed Decision, Action
 * and Tool Attempts. A read-only Trace projection of an Agent NodeRun; like Node Detail it
 * never advances the Agent Loop or infers a Turn, Action or termination.
 */
export function getAgentTrace(
  runId: string,
  nodeRunId: string,
  signal?: AbortSignal,
): Promise<AgentTrace> {
  return request<AgentTrace>(
    `/runs/${encodeURIComponent(runId)}/nodes/${encodeURIComponent(nodeRunId)}/agent`,
    { ...(signal ? { signal } : {}) },
  );
}

/**
 * Reads committed Events as plain JSON. `limit` is a request; the Backend caps it. The
 * caller still applies Events in strictly ascending `seq` order.
 */
export function listEvents(
  runId: string,
  afterSeq: number,
  limit?: number,
  signal?: AbortSignal,
): Promise<RunEvent[]> {
  const query = new URLSearchParams({ afterSeq: String(afterSeq) });
  if (limit !== undefined) query.set('limit', String(limit));
  return request<EventListResponse>(
    `/runs/${encodeURIComponent(runId)}/events?${query.toString()}`,
    { ...(signal ? { signal } : {}) },
  ).then((r) => r.items);
}

// --- Approvals --------------------------------------------------------------

/**
 * The header the approval routes authenticate with. The credential comes from the viewer
 * for the current page only; it is never stored by the client and never logged.
 */
export const OPERATOR_TOKEN_HEADER = 'X-Emberling-Operator-Token';

function operatorHeaders(token: string): Record<string, string> {
  return { [OPERATOR_TOKEN_HEADER]: token };
}

/** Approvals with the given status, oldest request first. */
export function listApprovals(
  status: ApprovalStatus,
  token: string,
  signal?: AbortSignal,
): Promise<ApprovalView[]> {
  const query = new URLSearchParams({ status });
  return request<ApprovalListResponse>(`/approvals?${query.toString()}`, {
    headers: operatorHeaders(token),
    ...(signal ? { signal } : {}),
  }).then((r) => r.items);
}

/**
 * One approval with the Tool name, the committed call arguments, and the change request and
 * patch the call names, for a person to decide on.
 */
export function getApproval(
  approvalId: string,
  token: string,
  signal?: AbortSignal,
): Promise<ApprovalDetail> {
  return request<ApprovalDetail>(`/approvals/${encodeURIComponent(approvalId)}`, {
    headers: operatorHeaders(token),
    ...(signal ? { signal } : {}),
  });
}

/**
 * Records a person's decision. The Backend settles the approval once; the same decision
 * again is answered as `repeated`, a different one as a 409 conflict.
 */
export function decideApproval(
  approvalId: string,
  body: ApprovalDecisionRequest,
  token: string,
): Promise<ApprovalDecisionResponse> {
  return request<ApprovalDecisionResponse>(
    `/approvals/${encodeURIComponent(approvalId)}/decision`,
    { method: 'POST', body, headers: operatorHeaders(token) },
  );
}
