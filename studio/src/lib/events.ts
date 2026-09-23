import type { EventType, JsonObject } from '@/api/types';
import type { StatusTone } from '@/lib/status';

/**
 * Display helpers for committed Events. Like every other projection in Studio these are
 * total lookup tables over server facts: nothing here derives, orders or infers an Event.
 */

const EVENT_LABELS: Record<EventType, string> = {
  RUN_CREATED: 'Run created',
  RUN_PAUSED: 'Run paused',
  RUN_RESUMED: 'Run resumed',
  RUN_COMPLETED: 'Run completed',
  RUN_FAILED: 'Run failed',

  NODE_READY: 'Node ready',
  NODE_STARTED: 'Node started',
  NODE_RETRYING: 'Node retrying',
  NODE_DISPATCHED: 'Dispatched to provider',
  NODE_CALLBACK_RECEIVED: 'Callback received',
  NODE_COMPLETED: 'Node completed',
  NODE_FAILED: 'Node failed',

  AGENT_STARTED: 'Agent started',
  AGENT_TURN_READY: 'Turn ready',
  AGENT_TURN_STARTED: 'Turn started',
  AGENT_DECISION_COMMITTED: 'Decision committed',
  AGENT_ACTION_STARTED: 'Action started',
  AGENT_ACTION_WAITING: 'Action waiting for callback',
  AGENT_ACTION_COMPLETED: 'Action completed',
  AGENT_ACTION_FAILED: 'Action failed',
  AGENT_STATE_UPDATED: 'State updated',
  AGENT_COMPLETED: 'Agent completed',
  AGENT_FAILED: 'Agent failed',
};

export function eventLabel(type: EventType): string {
  return EVENT_LABELS[type] ?? type;
}

/**
 * Timeline dot colour per committed Event type (04 §4 status colours). It classifies the
 * transition the Event itself records; it never looks at other Events or NodeRuns, so it
 * cannot turn into a client-side status derivation.
 */
const EVENT_TONES: Record<EventType, StatusTone> = {
  RUN_CREATED: 'succeeded',
  RUN_PAUSED: 'waiting',
  RUN_RESUMED: 'running',
  RUN_COMPLETED: 'succeeded',
  RUN_FAILED: 'failed',

  NODE_READY: 'neutral',
  NODE_STARTED: 'running',
  NODE_RETRYING: 'running',
  NODE_DISPATCHED: 'waiting',
  NODE_CALLBACK_RECEIVED: 'running',
  NODE_COMPLETED: 'succeeded',
  NODE_FAILED: 'failed',

  AGENT_STARTED: 'running',
  AGENT_TURN_READY: 'neutral',
  AGENT_TURN_STARTED: 'running',
  AGENT_DECISION_COMMITTED: 'succeeded',
  AGENT_ACTION_STARTED: 'running',
  AGENT_ACTION_WAITING: 'waiting',
  AGENT_ACTION_COMPLETED: 'succeeded',
  AGENT_ACTION_FAILED: 'failed',
  AGENT_STATE_UPDATED: 'succeeded',
  AGENT_COMPLETED: 'succeeded',
  AGENT_FAILED: 'failed',
};

export function eventTone(type: EventType): StatusTone {
  return EVENT_TONES[type] ?? 'neutral';
}

/**
 * The bounded scalar payload fields the timeline is allowed to print, with the wording
 * used for each. This is an allowlist on purpose: a payload key Studio does not know is
 * never rendered, so no field the Backend adds later can leak into the timeline. The full
 * payload of the selected Event is still shown verbatim in the Detail panel.
 *
 * `providerId` and `externalTaskId` are deliberately absent. They are Callback Binding
 * facts projected by the Node Detail query and are not copied into Event payloads.
 */
const PAYLOAD_FIELDS: ReadonlyArray<{ key: string; format: (value: string) => string }> = [
  { key: 'attemptNo', format: (value) => `attempt ${value}` },
  { key: 'turnNo', format: (value) => `turn ${value}` },
  { key: 'callbackBindingId', format: (value) => `binding ${value}` },
  { key: 'claimSource', format: (value) => `claimed by ${value}` },
  { key: 'completionSource', format: (value) => `via ${value}` },
  { key: 'failureSource', format: (value) => `via ${value}` },
];

/**
 * Compact one-line summary of the Event payload: the Run status transition when the Event
 * carries one, then the allowlisted fields it actually contains, in a stable order.
 */
export function eventPayloadSummary(payload: JsonObject): string[] {
  const summary: string[] = [];

  const { from, to } = payload;
  if (typeof from === 'string' && typeof to === 'string') {
    summary.push(`${from} → ${to}`);
  }

  for (const field of PAYLOAD_FIELDS) {
    const value = payload[field.key];
    if (typeof value === 'string' || typeof value === 'number') {
      summary.push(field.format(String(value)));
    }
  }

  return summary;
}
