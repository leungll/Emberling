import { isWithin, stepOfNodeRun, type RunModel, type TraceElement } from './run-model';
import type { EventType, RunEvent } from '@/api/types';
import { copy } from '@/lib/copy';
import { duration, offsetText } from '@/lib/time';

/**
 * Events in the run view: which element of the trace an Event belongs to, its filter
 * groups, and a short summary from its own bounded payload. Raw error messages never
 * appear here; they stay in the details' Technical details.
 */

export type EventGroup = 'all' | 'state' | 'decisions' | 'tools' | 'waits' | 'errors';
export const EVENT_GROUPS: readonly EventGroup[] = [
  'all',
  'state',
  'decisions',
  'tools',
  'waits',
  'errors',
];

const GROUPS_OF: Record<EventType, readonly EventGroup[]> = {
  RUN_CREATED: ['state'],
  RUN_COMPLETED: ['state'],
  RUN_FAILED: ['state', 'errors'],
  RUN_PAUSED: ['state', 'waits'],
  RUN_RESUMED: ['state', 'waits'],
  NODE_READY: ['state'],
  NODE_STARTED: ['state'],
  NODE_COMPLETED: ['state'],
  NODE_FAILED: ['state', 'errors'],
  NODE_RETRYING: ['state', 'errors'],
  NODE_DISPATCHED: ['waits'],
  NODE_CALLBACK_RECEIVED: ['waits'],
  AGENT_STARTED: ['state'],
  AGENT_TURN_READY: ['state'],
  AGENT_TURN_STARTED: ['state'],
  AGENT_STATE_UPDATED: ['state'],
  AGENT_COMPLETED: ['state'],
  AGENT_FAILED: ['state', 'errors'],
  AGENT_DECISION_COMMITTED: ['decisions'],
  AGENT_ACTION_STARTED: ['tools'],
  AGENT_ACTION_COMPLETED: ['tools'],
  AGENT_ACTION_WAITING: ['tools', 'waits'],
  AGENT_ACTION_FAILED: ['tools', 'errors'],
};

function payloadOf(e: RunEvent): Record<string, unknown> {
  return e.payload;
}

function str(v: unknown): string | null {
  return typeof v === 'string' ? v : null;
}

/** The trace element an Event is about: the narrowest one its payload names. */
export function elementOfEvent(model: RunModel, e: RunEvent): TraceElement {
  const p = payloadOf(e);
  const by = model.byId;
  const toolAttemptId = str(p.toolAttemptId);
  const turnId = str(p.turnId);
  const actionId = str(p.actionId);
  if (toolAttemptId && by.has(toolAttemptId)) return by.get(toolAttemptId) ?? model.root;
  if (e.type === 'AGENT_DECISION_COMMITTED' && turnId && by.has(`dec:${turnId}`)) {
    return by.get(`dec:${turnId}`) ?? model.root;
  }
  if (actionId && by.has(actionId)) return by.get(actionId) ?? model.root;
  if (turnId && by.has(turnId)) return by.get(turnId) ?? model.root;
  if (e.nodeRunId) {
    const step = stepOfNodeRun(model, e.nodeRunId);
    const stepEl = step ? by.get(step.node.id) : undefined;
    if (stepEl && step && typeof p.attemptNo === 'number') {
      const attempt = step.detail?.attempts.find((a) => a.attemptNo === p.attemptNo);
      const attemptEl = attempt ? by.get(attempt.id) : undefined;
      if (attemptEl) return attemptEl;
    }
    if (stepEl) return stepEl;
  }
  return model.root;
}

/** The Events of an element and of everything it contains. */
export function eventsFor(model: RunModel, el: TraceElement): RunEvent[] {
  if (el === model.root) return model.events.slice();
  return model.events.filter((e) => isWithin(elementOfEvent(model, e), el));
}

/** A failure: a FAILED type, a retry, or any Event whose payload carries an error. */
export function isFailureEvent(e: RunEvent): boolean {
  return e.type.endsWith('FAILED') || e.type === 'NODE_RETRYING' || Boolean(payloadOf(e).error);
}

/**
 * The Events filter groups. An Agent action without a tool attempt is the Agent finishing,
 * a state change rather than a tool call; an Event whose payload carries an error is also
 * an error.
 */
export function eventGroups(e: RunEvent): EventGroup[] {
  const groups = [...(GROUPS_OF[e.type] ?? ['state'])];
  const p = payloadOf(e);
  if (e.type.startsWith('AGENT_ACTION_') && !p.toolAttemptId && groups.includes('tools')) {
    groups.splice(groups.indexOf('tools'), 1);
    if (!groups.includes('state')) groups.push('state');
  }
  if (p.error && !groups.includes('errors')) groups.push('errors');
  return groups;
}

export function inGroup(e: RunEvent, g: EventGroup): boolean {
  return g === 'all' || eventGroups(e).includes(g);
}

/** The Event's name in words, and the tool it names, if any. */
export function eventLabel(e: RunEvent): { text: string; tools: string[] } {
  const p = payloadOf(e);
  if (e.type === 'AGENT_DECISION_COMMITTED') {
    const tool = str(p.toolName);
    return p.kind === 'FINAL' || !tool
      ? { text: copy.runs.trace.decidedFinish, tools: [] }
      : { text: copy.runs.trace.decidedCall(tool), tools: [tool] };
  }
  const label = (copy.events.label as Partial<Record<EventType, string>>)[e.type];
  const fallback = e.type.toLowerCase().replace(/_/g, ' ');
  return { text: label ?? `${fallback.charAt(0).toUpperCase()}${fallback.slice(1)}`, tools: [] };
}

function errorShort(code: string | null): string {
  const entry = code
    ? (copy.errors as Record<string, { short: string } | undefined>)[code]
    : undefined;
  return entry?.short ?? copy.runs.events.summary.failed;
}

/** A short summary from the Event's own payload; '' when it adds nothing. */
export function eventSummary(model: RunModel, e: RunEvent): string {
  const p = payloadOf(e);
  const s = copy.runs.events.summary;
  const error = p.error as { code?: unknown } | undefined;
  const code = typeof error?.code === 'string' ? error.code : null;
  const attemptNo = typeof p.attemptNo === 'number' ? p.attemptNo : null;
  switch (e.type) {
    case 'RUN_CREATED':
      return typeof p.definitionVersion === 'number' ? s.version(p.definitionVersion) : '';
    case 'RUN_FAILED': {
      const runCode = model.snapshot.run.error?.code ?? null;
      return runCode ? errorShort(runCode) : s.stopped;
    }
    case 'NODE_STARTED':
      return attemptNo !== null && attemptNo > 1 ? s.attempt(attemptNo) : '';
    case 'NODE_RETRYING': {
      const next = str(p.nextAttemptAt);
      return attemptNo === null
        ? ''
        : s.retry(
            attemptNo,
            next ? offsetText(new Date(next).getTime() - new Date(model.runStart).getTime()) : null,
          );
    }
    case 'NODE_COMPLETED':
      return typeof p.latencyMs === 'number' ? s.took(duration(p.latencyMs)) : '';
    case 'NODE_FAILED':
    case 'AGENT_ACTION_FAILED':
      return code ? errorShort(code) : '';
    case 'AGENT_STARTED':
      return typeof p.maxTurns === 'number' ? s.upTo(p.maxTurns) : '';
    case 'AGENT_ACTION_STARTED': {
      const claim = str(p.claimSource);
      const words = claim
        ? (copy.status.claimSource as Record<string, string | null | undefined>)[claim]
        : null;
      return words ?? '';
    }
    case 'AGENT_ACTION_COMPLETED': {
      const src = str(p.completionSource);
      const words = src
        ? (copy.status.completionSource as Record<string, string | null | undefined>)[src]
        : null;
      return words ?? '';
    }
    case 'AGENT_COMPLETED':
    case 'AGENT_FAILED': {
      const term = str(p.termination);
      const words = term
        ? (copy.status.termination as Record<string, string | undefined>)[term]
        : undefined;
      return words ?? '';
    }
    default:
      return '';
  }
}

/** "Delivery Agent › Turn 2": where an element sits, by step, turn and step attempt. */
export function elementCrumb(model: RunModel, el: TraceElement): string {
  if (el === model.root) return copy.runs.trace.run;
  const parts: string[] = [];
  let x: TraceElement | null = el;
  while (x && x !== model.root) {
    if (x.kind === 'step' && x.step) parts.unshift(x.step.node.name);
    else if (x.kind === 'turn' && x.turn)
      parts.unshift(copy.runs.details.crumb.turn(x.turn.turnNo));
    else if (x.kind === 'attempt' && x.nodeAttempt)
      parts.unshift(copy.runs.details.crumb.attempt(x.nodeAttempt.attemptNo));
    x = x.parent;
  }
  return parts.join(' › ');
}

/** The crumb plus the element's own name when the crumb stops above it. */
export function elementPath(model: RunModel, el: TraceElement): string {
  const crumb = elementCrumb(model, el);
  const own =
    el.kind === 'step' ||
    el.kind === 'turn' ||
    el.kind === 'run' ||
    (el.kind === 'attempt' && Boolean(el.nodeAttempt));
  return own ? crumb : `${crumb} › ${el.label}`;
}

export interface EventFilter {
  group: EventGroup;
  query: string;
  /** Only the selected element's Events (and what it contains). */
  only: boolean;
}

/** The Events list under the filters, for the selected element. */
export function filterEvents(
  model: RunModel,
  selected: TraceElement,
  filter: EventFilter,
  group: EventGroup = filter.group,
): RunEvent[] {
  let list =
    filter.only && selected !== model.root ? eventsFor(model, selected) : model.events.slice();
  if (group !== 'all') list = list.filter((e) => inGroup(e, group));
  const q = filter.query.trim().toLowerCase();
  if (q) {
    list = list.filter((e) =>
      `${eventLabel(e).text} ${elementCrumb(model, elementOfEvent(model, e))} ${eventSummary(model, e)} ${String(e.seq)}`
        .toLowerCase()
        .includes(q),
    );
  }
  return list;
}
