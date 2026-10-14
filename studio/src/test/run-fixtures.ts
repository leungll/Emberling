// Small builders for run view tests: a Definition, its Snapshot, Node details and Agent
// Traces, with only the fields a test names set and the rest at neutral values.
import type {
  AgentActionTrace,
  AgentTrace,
  AgentTurnTrace,
  Definition,
  Node,
  NodeAttempt,
  NodeRun,
  NodeRunDetail,
  RunEvent,
  RunSnapshot,
  RunStatus,
  ToolAttemptTrace,
} from '@/api/types';
import { buildRunModel, type RunModel } from '@/features/observe/run-model';

export const T0 = '2026-10-01T10:00:00.000Z';

/** T0 plus a number of seconds, as an ISO timestamp. */
export function at(seconds: number): string {
  return new Date(new Date(T0).getTime() + seconds * 1000).toISOString();
}

export function node(id: string, type: string, name: string, extra: Partial<Node> = {}): Node {
  return { id, type, name, position: { x: 0, y: 0 }, config: {}, ...extra };
}

export function definition(nodes: Node[], links: [string, string][]): Definition {
  return {
    workflowId: 'wf_1',
    version: 1,
    name: 'Review flow',
    description: '',
    nodes,
    edges: links.map(([source, target], i) => ({
      id: `e${String(i)}`,
      source,
      sourceHandle: 'out',
      target,
      targetHandle: 'in',
    })),
    runInputSchema: { type: 'object' },
    validation: { status: 'VALID', validatorVersion: '1', validatedAt: T0 },
    createdAt: T0,
  };
}

export function nodeRun(nodeId: string, nodeType: string, extra: Partial<NodeRun> = {}): NodeRun {
  return {
    id: `nr_${nodeId}`,
    runId: 'run_1',
    nodeId,
    nodeType,
    status: 'SUCCEEDED',
    input: null,
    output: null,
    error: null,
    readyAt: T0,
    startedAt: T0,
    waitingAt: null,
    completedAt: at(1),
    latencyMs: 1000,
    tokenUsage: null,
    ...extra,
  };
}

export function snapshot(
  status: RunStatus,
  nodeRuns: NodeRun[],
  extra: Partial<RunSnapshot['run']> = {},
): RunSnapshot {
  const ended = status === 'COMPLETED' || status === 'FAILED';
  return {
    run: {
      id: 'run_1',
      workflowId: 'wf_1',
      definitionVersion: 1,
      status,
      input: {},
      output: null,
      error: null,
      tokenUsage: null,
      startedAt: T0,
      completedAt: ended ? at(10) : null,
      ...extra,
    },
    nodeRuns,
    lastSeq: 0,
  };
}

export function nodeAttempt(attemptNo: number, extra: Partial<NodeAttempt> = {}): NodeAttempt {
  return {
    id: `att_${String(attemptNo)}`,
    attemptNo,
    status: 'SUCCEEDED',
    input: null,
    result: null,
    startedAt: at(attemptNo),
    deadlineAt: null,
    dispatchedAt: null,
    completedAt: at(attemptNo + 0.5),
    error: null,
    uncertain: null,
    callbackBinding: null,
    ...extra,
  };
}

export function toolAttempt(
  id: string,
  tool: string,
  extra: Partial<ToolAttemptTrace> = {},
): ToolAttemptTrace {
  return {
    id,
    toolName: tool,
    attemptNo: 1,
    status: 'SUCCEEDED',
    callbackBinding: null,
    startedAt: T0,
    dispatchedAt: null,
    completedAt: T0,
    error: null,
    ...extra,
  };
}

function action(id: string, extra: Partial<AgentActionTrace> = {}): AgentActionTrace {
  return {
    id,
    type: 'TOOL_CALL',
    status: 'SUCCEEDED',
    startedAt: T0,
    completedAt: T0,
    error: null,
    claimSource: 'IMMEDIATE',
    completionSource: 'SYNC_EXECUTION',
    failureSource: null,
    ...extra,
  };
}

/** A Turn that called `tool` (or finished when `tool` is null), succeeding at `second`. */
export function turn(
  turnNo: number,
  tool: string | null,
  second: number,
  extra: {
    action?: Partial<AgentActionTrace>;
    attempts?: ToolAttemptTrace[];
    turn?: Partial<AgentTurnTrace>;
  } = {},
): AgentTurnTrace {
  const id = `turn_${String(turnNo)}`;
  const start = at(second);
  return {
    id,
    turnNo,
    status: 'COMPLETED',
    startedAt: start,
    completedAt: start,
    error: null,
    usage: null,
    claimSource: 'IMMEDIATE',
    decision: { kind: tool ? 'TOOL_CALL' : 'FINAL', tool, hasStatePatch: false },
    action: action(`act_${String(turnNo)}`, {
      type: tool ? 'TOOL_CALL' : 'FINAL',
      startedAt: start,
      completedAt: start,
      ...extra.action,
    }),
    toolAttempts:
      extra.attempts ??
      (tool
        ? [toolAttempt(`ta_${String(turnNo)}`, tool, { startedAt: start, completedAt: start })]
        : []),
    ...extra.turn,
  };
}

export function agentTrace(
  turns: AgentTurnTrace[],
  extra: Partial<AgentTrace['agentRun']> = {},
): AgentTrace {
  return {
    agentRun: {
      id: 'ar_1',
      nodeRunId: 'nr_agent',
      termination: null,
      currentTurnNo: turns.length,
      currentContextVersion: 1,
      currentStateVersion: 1,
      deadline: null,
      terminatedAt: null,
      error: null,
      ...extra,
    },
    turns,
    facts: { items: [], truncated: false },
    generationBudget: { maxGenerationCalls: null, generationCallsUsed: 0 },
  };
}

export function event(
  seq: number,
  type: RunEvent['type'],
  extra: Partial<RunEvent> = {},
): RunEvent {
  return {
    id: `ev_${String(seq)}`,
    runId: 'run_1',
    nodeRunId: null,
    type,
    seq,
    timestamp: at(seq),
    payload: {},
    ...extra,
  };
}

export interface ModelParts {
  snapshot: RunSnapshot;
  definition: Definition;
  events?: RunEvent[];
  details?: Record<string, NodeRunDetail>;
  traces?: Record<string, AgentTrace>;
}

export function model(parts: ModelParts): RunModel {
  return buildRunModel({
    snapshot: parts.snapshot,
    definition: parts.definition,
    events: parts.events ?? [],
    details: parts.details ?? {},
    traces: parts.traces ?? {},
    nodeTypes: [],
  });
}

/** Input, Agent and Output steps, linked in that order. */
export const AGENT_FLOW = definition(
  [
    node('in', 'text_input', 'Task'),
    node('agent', 'agent', 'Reviewer'),
    node('out', 'text_output', 'Result'),
  ],
  [
    ['in', 'agent'],
    ['agent', 'out'],
  ],
);
