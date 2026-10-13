import type {
  AgentActionTrace,
  AgentTrace,
  AgentTurnTrace,
  Definition,
  Node,
  NodeAttempt,
  NodeMetadata,
  NodeRun,
  NodeRunDetail,
  RunEvent,
  RunSnapshot,
  ToolAttemptTrace,
} from '@/api/types';
import {
  statusInfo,
  type StatusSubject,
  type StepDisplayStatus,
  type Tone,
} from '@/components/status/status-info';
import { copy } from '@/lib/copy';

/**
 * The run view's model: the bound Definition version's steps joined with the Snapshot's
 * NodeRuns, their Attempts and Agent Traces, as one tree. Every status here is a value the
 * Backend reported; nothing is derived from another status. The only values computed are
 * placements on one time axis and durations between two timestamps of one record.
 */

export type ElementKind =
  'run' | 'step' | 'group' | 'turn' | 'decision' | 'action' | 'attempt' | 'wait';

export interface Step {
  node: Node;
  nodeRun: NodeRun | null;
  /** The NodeRun's status, or NONE / NONE_RUN_FAILED for a step that never started. */
  statusKey: StepDisplayStatus;
  /** "Agent", "Text input": the registered display name in sentence case. */
  typeLabel: string;
  detail: NodeRunDetail | null;
  trace: AgentTrace | null;
}

export interface TraceElement {
  id: string;
  kind: ElementKind;
  parent: TraceElement | null;
  children: TraceElement[];
  depth: number;
  /** The visible name, plain text. Tool names in it are listed in `tools`. */
  label: string;
  tools: readonly string[];
  /** A quieter second word after the name: "Agent", "Tool call", "reported back". */
  type: string;
  status: StatusSubject;
  /** A word other than the status table's, for waits and repeated turns. */
  statusLabel?: string;
  tone: Tone;
  /** An ended wait: the hourglass outline in the inactive tone. */
  endedWait?: boolean;
  start: string | null;
  notRun?: boolean;
  step: Step | null;
  turn?: AgentTurnTrace;
  action?: AgentActionTrace;
  nodeAttempt?: NodeAttempt;
  toolAttempt?: ToolAttemptTrace;
  /** Repeated turns: the turns folded into this row, the repeated tools and how often. */
  turns?: AgentTurnTrace[];
  unit?: string[];
  reps?: number;
  /** A wait: still waiting, the service and its task. */
  live?: boolean;
  provider?: string | null;
  task?: string | null;
  actionId?: string;
}

export interface RunModel {
  snapshot: RunSnapshot;
  runStart: string;
  events: readonly RunEvent[];
  definition: Definition;
  /** In execution order: by the server's startedAt, steps that never started last. */
  steps: Step[];
  byNode: ReadonlyMap<string, Step>;
  failedStep: Step | null;
  waitingStep: Step | null;
  terminal: boolean;
  root: TraceElement;
  byId: ReadonlyMap<string, TraceElement>;
  /** NODE_RETRYING Events by `<nodeRunId>:<attemptNo>`: the next attempt's time. */
  retryEvent: ReadonlyMap<string, RunEvent>;
}

export interface RunModelInput {
  snapshot: RunSnapshot;
  events: readonly RunEvent[];
  definition: Definition;
  details: Readonly<Record<string, NodeRunDetail>>;
  traces: Readonly<Record<string, AgentTrace>>;
  nodeTypes: readonly NodeMetadata[];
}

const t = () => copy.runs.trace;

function sentenceCase(s: string): string {
  const lower = s.toLowerCase();
  return lower ? `${lower.charAt(0).toUpperCase()}${lower.slice(1)}` : lower;
}

export function typeLabelOf(node: Node, nodeTypes: readonly NodeMetadata[]): string {
  const meta = nodeTypes.find((m) => m.type === node.type);
  return sentenceCase(meta ? meta.displayName : node.type.replace(/_/g, ' '));
}

function time(iso: string | null | undefined): number {
  return iso ? new Date(iso).getTime() : Number.NaN;
}

/** Milliseconds between two timestamps of one record, or null when either is missing. */
export function between(a: string | null | undefined, b: string | null | undefined): number | null {
  return a && b ? time(b) - time(a) : null;
}

function byStart(a: { start: string | null }, b: { start: string | null }): number {
  if (a.start && b.start) return time(a.start) - time(b.start);
  return a.start ? -1 : b.start ? 1 : 0;
}

/** "Attempt 2 of 3" when the step's policy carries the maximum; a tool call has none. */
export function attemptWord(attemptNo: number, node: Node | null): string {
  const max = node?.executionPolicy?.maxAttempts ?? null;
  return t().attempt(attemptNo, max && max > 1 ? max : null);
}

function subjectTone(status: StatusSubject): Tone {
  return statusInfo(status).tone;
}

function buildTree(
  steps: readonly Step[],
  snapshot: RunSnapshot,
  actionSource: ReadonlyMap<string, string>,
): { root: TraceElement; byId: Map<string, TraceElement> } {
  const byId = new Map<string, TraceElement>();
  const add = (
    el: Omit<TraceElement, 'parent' | 'children' | 'depth' | 'tone'> & { tone?: Tone },
    parent: TraceElement | null,
  ): TraceElement => {
    const full: TraceElement = {
      ...el,
      tone: el.tone ?? subjectTone(el.status),
      parent,
      children: [],
      depth: parent ? parent.depth + 1 : 0,
    };
    byId.set(full.id, full);
    parent?.children.push(full);
    return full;
  };

  const run = snapshot.run;
  const root = add(
    {
      id: 'run',
      kind: 'run',
      label: t().run,
      tools: [],
      type: '',
      status: { kind: 'run', value: run.status },
      start: run.startedAt ?? null,
      step: null,
    },
    null,
  );

  const addWait = (
    attempt: ToolAttemptTrace | undefined,
    parent: TraceElement,
    actionId: string,
  ) => {
    if (!attempt?.dispatchedAt) return;
    const binding = attempt.callbackBinding;
    const who = copy.runs.provider(binding?.providerId);
    const live = !attempt.completedAt;
    const source = actionSource.get(actionId);
    add(
      {
        id: `wait:${parent.id}`,
        kind: 'wait',
        label: live ? t().waitingFor(who) : t().waitedFor(who),
        tools: [],
        type: live
          ? ''
          : attempt.status === 'FAILED'
            ? t().noResult
            : source === 'PROVIDER_POLL'
              ? t().resultFetched
              : t().reportedBack,
        status: { kind: 'attempt', value: live ? 'DISPATCHED' : attempt.status },
        statusLabel: live
          ? t().waitStatus.live
          : attempt.status === 'FAILED'
            ? t().waitStatus.none
            : t().waitStatus.received,
        tone: live ? 'waiting' : 'inactive',
        endedWait: !live,
        start: attempt.dispatchedAt,
        step: parent.step,
        turn: parent.turn,
        toolAttempt: attempt,
        live,
        provider: binding?.providerId ?? null,
        task: binding?.externalTaskId ?? null,
        actionId,
      },
      parent,
    );
  };

  const addTurn = (turn: AgentTurnTrace, step: Step, parent: TraceElement) => {
    const action = turn.action;
    const decision = turn.decision;
    const tool = decision?.tool ?? '';
    let what: string;
    if (!decision) what = t().turnDeciding;
    else if (decision.kind === 'FINAL') what = t().turnFinished;
    else if (!action) what = t().turnCalling(tool);
    else if (action.status === 'SUCCEEDED') what = t().turnCalled(tool);
    else if (
      action.status === 'WAITING_CALLBACK' ||
      action.status === 'RUNNING' ||
      action.status === 'READY'
    )
      what = t().turnCalling(tool);
    else if (turn.toolAttempts.length) what = t().turnToolFailed(tool);
    else what = t().turnBlocked(tool);
    const turnStatus: StatusSubject = action
      ? { kind: 'action', value: action.status }
      : { kind: 'turn', value: turn.status };
    const turnEl = add(
      {
        id: turn.id,
        kind: 'turn',
        label: t().turn(turn.turnNo, what),
        tools: tool ? [tool] : [],
        type: '',
        status: turnStatus,
        start: turn.startedAt,
        step,
        turn,
      },
      parent,
    );
    if (decision) {
      add(
        {
          id: `dec:${turn.id}`,
          kind: 'decision',
          label: decision.kind === 'FINAL' ? t().decidedFinish : t().decidedCall(tool),
          tools: decision.kind === 'FINAL' ? [] : [tool],
          type: t().modelDecision,
          status: { kind: 'turn', value: turn.status },
          start: turn.startedAt,
          step,
          turn,
        },
        turnEl,
      );
    }
    if (!action || !decision) return;
    const actionEl = add(
      {
        id: action.id,
        kind: 'action',
        label: decision.kind === 'FINAL' ? t().finalAnswer : tool,
        tools: decision.kind === 'FINAL' ? [] : [tool],
        type: decision.kind === 'FINAL' ? '' : t().toolCall,
        status: { kind: 'action', value: action.status },
        start: action.startedAt,
        step,
        turn,
        action,
      },
      turnEl,
    );
    if (turn.toolAttempts.length > 1) {
      for (const x of turn.toolAttempts) {
        const at = add(
          {
            id: x.id,
            kind: 'attempt',
            label: tool,
            tools: [tool],
            type: attemptWord(x.attemptNo, null),
            status: { kind: 'attempt', value: x.status },
            start: x.startedAt,
            step,
            turn,
            toolAttempt: x,
          },
          actionEl,
        );
        addWait(x, at, action.id);
      }
    } else {
      addWait(turn.toolAttempts[0], actionEl, action.id);
    }
  };

  // Successful tool calls whose tool sequence repeats (a unit of 1 to 3 tools, at least two
  // repeats, at least three turns) fold into one row. Failed and waiting turns never fold.
  const keyOf = (turn: AgentTurnTrace): string | null =>
    turn.decision?.kind === 'TOOL_CALL' && turn.action?.status === 'SUCCEEDED'
      ? turn.decision.tool
      : null;

  const addTurns = (step: Step, stepEl: TraceElement, turns: readonly AgentTurnTrace[]) => {
    let i = 0;
    while (i < turns.length) {
      let group: { size: number; reps: number; unit: string[] } | null = null;
      const first = turns[i];
      if (first && keyOf(first)) {
        for (let size = 1; size <= 3 && !group; size += 1) {
          const unit = turns.slice(i, i + size).map(keyOf);
          if (unit.length < size || unit.some((k) => !k)) break;
          let reps = 1;
          for (;;) {
            const next = turns.slice(i + reps * size, i + reps * size + size).map(keyOf);
            if (next.length === size && next.every((k, j) => k === unit[j])) reps += 1;
            else break;
          }
          if (reps >= 2 && reps * size >= 3) {
            group = { size, reps, unit: unit.filter((k): k is string => k !== null) };
          }
        }
      }
      if (group && first) {
        const n = group.size * group.reps;
        const folded = turns.slice(i, i + n);
        const last = folded[folded.length - 1] ?? first;
        const unitText = group.unit.join(' → ');
        const g = add(
          {
            id: `grp:${first.id}`,
            kind: 'group',
            label: t().group(first.turnNo, last.turnNo, unitText, group.reps),
            tools: group.unit,
            type: '',
            status: { kind: 'turn', value: 'COMPLETED' },
            statusLabel: t().groupStatus,
            start: first.startedAt,
            step,
            turns: folded,
            unit: group.unit,
            reps: group.reps,
          },
          stepEl,
        );
        for (const turn of folded) addTurn(turn, step, g);
        i += n;
      } else if (first) {
        addTurn(first, step, stepEl);
        i += 1;
      }
    }
  };

  for (const step of steps) {
    const nr = step.nodeRun;
    const stepEl = add(
      {
        id: step.node.id,
        kind: 'step',
        label: step.node.name,
        tools: [],
        type: step.typeLabel.toLowerCase() === step.node.name.toLowerCase() ? '' : step.typeLabel,
        status: { kind: 'step', value: step.statusKey },
        start: nr?.startedAt ?? null,
        notRun: !nr,
        step,
      },
      root,
    );
    if (step.trace) {
      addTurns(step, stepEl, step.trace.turns);
    } else {
      const attempts = step.detail?.attempts ?? [];
      if (attempts.length > 1) {
        for (const a of attempts) {
          add(
            {
              id: a.id,
              kind: 'attempt',
              label: attemptWord(a.attemptNo, step.node),
              tools: [],
              type: '',
              status: { kind: 'attempt', value: a.status },
              start: a.startedAt,
              step,
              nodeAttempt: a,
            },
            stepEl,
          );
        }
      }
    }
  }

  const order = (el: TraceElement) => {
    if (el.kind !== 'group') el.children.sort(byStart);
    el.children.forEach(order);
  };
  order(root);
  return { root, byId };
}

/** Builds the model of one run. */
export function buildRunModel(input: RunModelInput): RunModel {
  const { snapshot, events, definition, details, traces, nodeTypes } = input;
  const run = snapshot.run;
  const runStart = run.startedAt ?? events[0]?.timestamp ?? new Date(0).toISOString();
  const terminal = run.status === 'COMPLETED' || run.status === 'FAILED';

  const steps: Step[] = definition.nodes.map((node) => {
    const nodeRun = snapshot.nodeRuns.find((n) => n.nodeId === node.id) ?? null;
    const statusKey: StepDisplayStatus = nodeRun
      ? nodeRun.status
      : run.status === 'FAILED'
        ? 'NONE_RUN_FAILED'
        : 'NONE';
    return {
      node,
      nodeRun,
      statusKey,
      typeLabel: typeLabelOf(node, nodeTypes),
      detail: nodeRun ? (details[nodeRun.id] ?? null) : null,
      trace: nodeRun?.nodeType === 'agent' ? (traces[nodeRun.id] ?? null) : null,
    };
  });
  const byNode = new Map(steps.map((s) => [s.node.id, s]));
  steps.sort((a, b) => {
    const x = a.nodeRun?.startedAt;
    const y = b.nodeRun?.startedAt;
    if (x && y) return time(x) - time(y);
    return x ? -1 : y ? 1 : 0;
  });

  const actionSource = new Map<string, string>();
  const retryEvent = new Map<string, RunEvent>();
  for (const e of events) {
    const p = e.payload as Record<string, unknown>;
    if (e.type === 'AGENT_ACTION_COMPLETED' && typeof p.actionId === 'string') {
      if (typeof p.completionSource === 'string') actionSource.set(p.actionId, p.completionSource);
    }
    if (e.type === 'NODE_RETRYING' && e.nodeRunId && typeof p.attemptNo === 'number') {
      retryEvent.set(`${e.nodeRunId}:${String(p.attemptNo)}`, e);
    }
  }
  // The Agent Trace carries the completion source when it has one; Events fill the rest.
  for (const s of steps) {
    for (const turn of s.trace?.turns ?? []) {
      if (turn.action?.completionSource)
        actionSource.set(turn.action.id, turn.action.completionSource);
    }
  }

  const { root, byId } = buildTree(steps, snapshot, actionSource);
  return {
    snapshot,
    runStart,
    events,
    definition,
    steps,
    byNode,
    failedStep: steps.find((s) => s.nodeRun?.status === 'FAILED') ?? null,
    waitingStep: steps.find((s) => s.nodeRun?.status === 'WAITING_CALLBACK') ?? null,
    terminal,
    root,
    byId,
    retryEvent,
  };
}

/* ---------- Walking the tree -------------------------------------------------------- */

export function ancestors(el: TraceElement): TraceElement[] {
  const out: TraceElement[] = [];
  let x = el.parent;
  while (x) {
    out.push(x);
    x = x.parent;
  }
  return out;
}

export function isWithin(el: TraceElement | null, root: TraceElement): boolean {
  let x = el;
  while (x) {
    if (x === root) return true;
    x = x.parent;
  }
  return false;
}

export function stepElementOf(el: TraceElement | null | undefined): TraceElement | null {
  let x = el ?? null;
  while (x && x.kind !== 'step') x = x.parent;
  return x;
}

/** The step that stopped this one: walks upstream links to the first failed step. */
export function blockedBy(model: RunModel, nodeId: string, seen = new Set<string>()): Step | null {
  if (seen.has(nodeId)) return null;
  seen.add(nodeId);
  const ins = model.definition.edges
    .filter((e) => e.target === nodeId)
    .map((e) => model.byNode.get(e.source))
    .filter((s): s is Step => Boolean(s));
  const failed = ins.find((s) => s.nodeRun?.status === 'FAILED');
  if (failed) return failed;
  for (const s of ins) {
    if (!s.nodeRun) {
      const up = blockedBy(model, s.node.id, seen);
      if (up) return up;
    }
  }
  return null;
}

/* ---------- Durations and the time axis -------------------------------------------- */

/** A finished duration, or a live one counted from its server start ("so far"). */
export type ElementDuration = { ms: number } | { live: string } | null;

/** Duration of one element, from two timestamps of its own record. */
export function durationOf(model: RunModel, el: TraceElement): ElementDuration {
  const run = model.snapshot.run;
  switch (el.kind) {
    case 'run': {
      const ms = between(run.startedAt, run.completedAt);
      return ms !== null ? { ms } : run.startedAt ? { live: run.startedAt } : null;
    }
    case 'step': {
      const nr = el.step?.nodeRun;
      if (!nr) return null;
      if (nr.latencyMs !== null) return { ms: nr.latencyMs };
      return nr.startedAt && !nr.completedAt ? { live: nr.startedAt } : null;
    }
    case 'attempt': {
      const a = el.nodeAttempt ?? el.toolAttempt;
      if (!a?.startedAt) return null;
      const ms = between(a.startedAt, a.completedAt);
      return ms !== null ? { ms } : { live: a.startedAt };
    }
    case 'decision': {
      const turn = el.turn;
      if (!turn?.startedAt) return null;
      const ms = between(turn.startedAt, turn.completedAt);
      return ms !== null ? { ms } : { live: turn.startedAt };
    }
    case 'action': {
      const x = el.action;
      if (!x?.startedAt) return null;
      const ms = between(x.startedAt, x.completedAt);
      if (ms !== null) return { ms };
      return x.status !== 'FAILED' ? { live: x.startedAt } : null;
    }
    case 'wait': {
      const a = el.toolAttempt;
      if (!a?.dispatchedAt) return null;
      const ms = between(a.dispatchedAt, a.completedAt);
      return ms !== null ? { ms } : { live: a.dispatchedAt };
    }
    default:
      return null;
  }
}

export interface Axis {
  t0: number;
  span: number;
}

/**
 * One axis for every bar: the run's start to its completedAt, or while it is live to the
 * newest Event's timestamp placed at 70% of the width, leaving room for open bars.
 */
export function axisOf(model: RunModel): Axis {
  const t0 = time(model.runStart);
  const lastEvent = model.events[model.events.length - 1];
  const last = lastEvent ? time(lastEvent.timestamp) : t0;
  const end = model.snapshot.run.completedAt;
  const span = end ? time(end) - t0 : (last - t0) / 0.7;
  return { t0, span: Math.max(1, span) };
}

function openStatus(v: string): boolean {
  return v === 'RUNNING' || v === 'STARTED' || v === 'WAITING_CALLBACK' || v === 'DISPATCHED';
}

/** Start and end on the axis; `end` null means not finished. */
export interface Span {
  start: string;
  end: string | null;
  wait?: boolean;
}

export function spanOf(model: RunModel, el: TraceElement): Span | null {
  const run = model.snapshot.run;
  const span = (start: string | null | undefined, end: string | null, status: string) =>
    start ? { start, end: end ?? (openStatus(status) ? null : start) } : null;
  switch (el.kind) {
    case 'run':
      return run.startedAt
        ? { start: run.startedAt, end: run.completedAt ?? (model.terminal ? run.startedAt : null) }
        : null;
    case 'step': {
      const nr = el.step?.nodeRun;
      return nr ? span(nr.startedAt, nr.completedAt, nr.status) : null;
    }
    case 'group': {
      const turns = el.turns ?? [];
      const first = turns[0];
      const last = turns[turns.length - 1];
      if (!first?.startedAt || !last) return null;
      return { start: first.startedAt, end: last.action?.completedAt ?? last.completedAt };
    }
    case 'turn': {
      const turn = el.turn;
      if (!turn) return null;
      const a = turn.action;
      if (!turn.startedAt) return null;
      if (a?.completedAt) return { start: turn.startedAt, end: a.completedAt };
      if (a && openStatus(a.status)) return { start: turn.startedAt, end: null };
      if (!a && openStatus(turn.status)) return { start: turn.startedAt, end: null };
      return { start: turn.startedAt, end: turn.completedAt ?? turn.startedAt };
    }
    case 'decision': {
      const turn = el.turn;
      return turn ? span(turn.startedAt, turn.completedAt, turn.status) : null;
    }
    case 'action': {
      const a = el.action;
      return a ? span(a.startedAt, a.completedAt, a.status) : null;
    }
    case 'attempt': {
      const a = el.nodeAttempt ?? el.toolAttempt;
      return a ? span(a.startedAt, a.completedAt, a.status) : null;
    }
    case 'wait': {
      const a = el.toolAttempt;
      return a?.dispatchedAt ? { start: a.dispatchedAt, end: a.completedAt, wait: true } : null;
    }
  }
}

/** Position on the axis in percent, clamped to the track. */
export function percentAt(axis: Axis, iso: string): number {
  return Math.max(0, Math.min(100, ((time(iso) - axis.t0) / axis.span) * 100));
}

/** The waits inside an element, drawn inside its own bar. */
export function waitsIn(el: TraceElement): TraceElement[] {
  const out: TraceElement[] = [];
  const walk = (x: TraceElement) => {
    for (const c of x.children) {
      if (c.kind === 'wait') out.push(c);
      walk(c);
    }
  };
  walk(el);
  return out;
}

/* ---------- Where to look ------------------------------------------------------------ */

/**
 * The deepest element where the failure starts: from the failed step, keep descending into
 * the last failed child. The failed tool call, the last failed attempt, or the step.
 */
export function failFocusId(model: RunModel): string | null {
  const fs = model.failedStep;
  if (!fs) return null;
  let el = model.byId.get(fs.node.id) ?? null;
  while (el) {
    const bad = el.children.filter((c) => c.tone === 'failed');
    const last = bad[bad.length - 1];
    if (!last) break;
    el = last;
  }
  return el ? el.id : fs.node.id;
}

export interface WaitInfo {
  step: Step;
  turn: AgentTurnTrace | null;
  attempt: ToolAttemptTrace | null;
  provider: string | null;
  task: string | null;
  since: string | null;
  deadline: string | null;
  /** The element that shows the wait: the wait row, else the tool call, else the step. */
  focus: string;
}

/** The step that is waiting, for what and since when, from stored values only. */
export function waitInfo(model: RunModel): WaitInfo | null {
  const ws = model.waitingStep;
  if (!ws) return null;
  const turn = ws.trace?.turns.find((x) => x.action?.status === 'WAITING_CALLBACK') ?? null;
  const attempt = turn?.toolAttempts.find((a) => a.status === 'DISPATCHED') ?? null;
  const binding = attempt?.callbackBinding ?? null;
  const actionId = turn?.action?.id ?? null;
  let focus = ws.node.id;
  if (actionId) {
    const waitEl = [...model.byId.values()].find(
      (e) => e.kind === 'wait' && e.actionId === actionId && e.live,
    );
    focus = waitEl ? waitEl.id : actionId;
  }
  return {
    step: ws,
    turn,
    attempt,
    provider: binding?.providerId ?? null,
    task: binding?.externalTaskId ?? null,
    since: attempt?.dispatchedAt ?? ws.nodeRun?.waitingAt ?? null,
    deadline: ws.trace && !ws.trace.agentRun.terminatedAt ? ws.trace.agentRun.deadline : null,
    focus,
  };
}

/** What a run opens on: where it failed, the live wait, or the run itself. */
export function openingId(model: RunModel): string {
  return failFocusId(model) ?? waitInfo(model)?.focus ?? 'run';
}

/** The ID a viewer copies for an element: the record's own ID. */
export function recordIdOf(model: RunModel, el: TraceElement): string {
  switch (el.kind) {
    case 'run':
      return model.snapshot.run.id;
    case 'step':
      return el.step?.nodeRun?.id ?? el.step?.node.id ?? model.snapshot.run.id;
    case 'attempt':
    case 'wait':
      return el.nodeAttempt?.id ?? el.toolAttempt?.id ?? model.snapshot.run.id;
    case 'turn':
    case 'decision':
      return el.turn?.id ?? model.snapshot.run.id;
    case 'action':
      return el.action?.id ?? model.snapshot.run.id;
    case 'group':
      return el.turns?.[0]?.id ?? model.snapshot.run.id;
  }
}

/** External tasks the run handed to an outside service, once per task, in the order sent. */
export function externalTasks(
  model: RunModel,
): { id: string; provider: string | null; at: string }[] {
  const out: { id: string; provider: string | null; at: string }[] = [];
  const seen = new Set<string>();
  const add = (
    b: { providerId: string; externalTaskId: string } | null,
    at: string | null | undefined,
  ) => {
    if (!b?.externalTaskId || seen.has(b.externalTaskId)) return;
    seen.add(b.externalTaskId);
    out.push({ id: b.externalTaskId, provider: b.providerId || null, at: at ?? '' });
  };
  for (const s of model.steps) {
    for (const a of s.detail?.attempts ?? []) add(a.callbackBinding, a.dispatchedAt ?? a.startedAt);
    for (const turn of s.trace?.turns ?? []) {
      for (const a of turn.toolAttempts) add(a.callbackBinding, a.dispatchedAt ?? a.startedAt);
    }
  }
  return out.sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : 0));
}

/** "Called sandbox_test 2 times, then finished." from an Agent's turns, in order. */
export function toolSequence(trace: AgentTrace): string {
  const s = copy.runs.details.sequence;
  const order: string[] = [];
  const count = new Map<string, number>();
  let finished = false;
  let blocked: string | null = null;
  let pending: string | null = null;
  let failed: string | null = null;
  for (const turn of trace.turns) {
    const d = turn.decision;
    const a = turn.action;
    if (!d || !a) continue;
    if (d.kind === 'FINAL') {
      finished = a.status === 'SUCCEEDED';
      continue;
    }
    const tool = d.tool ?? '';
    if (a.status === 'WAITING_CALLBACK' || a.status === 'RUNNING') {
      pending = tool;
      continue;
    }
    if (a.status === 'FAILED') {
      if (turn.toolAttempts.length) failed = tool;
      else blocked = tool;
      continue;
    }
    if (!count.has(tool)) order.push(tool);
    count.set(tool, (count.get(tool) ?? 0) + 1);
  }
  const parts = order.map((n) => s.calledTimes(n, count.get(n) ?? 1));
  return s.sentence(copy.runs.listNames(parts), { pending, blocked, failed, finished });
}

/** The Turns tab and graph card count: how many turns the Agent took. */
export function turnCount(step: Step): number {
  return step.trace?.turns.length ?? 0;
}

export function stepOfNodeRun(model: RunModel, nodeRunId: string): Step | null {
  return model.steps.find((s) => s.nodeRun?.id === nodeRunId) ?? null;
}
