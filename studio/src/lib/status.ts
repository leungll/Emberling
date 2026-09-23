import type { BadgeProps } from '@/components/ui/badge';
import type {
  AgentActionStatus,
  AgentTurnStatus,
  NodeAttemptStatus,
  NodeRunStatus,
  RunStatus,
  ToolAttemptStatus,
} from '@/api/types';

type BadgeVariant = NonNullable<BadgeProps['variant']>;

/**
 * Display lookup tables for server-reported statuses.
 *
 * These are total maps over the MVP status sets, not derivations. Studio never computes a
 * Run status from NodeRun statuses: `PAUSED` in particular is a server aggregate, and a
 * single `WAITING_CALLBACK` NodeRun must not be turned into it here or anywhere else.
 */

const RUN_STATUS_LABELS: Record<RunStatus, string> = {
  RUNNING: 'Running',
  PAUSED: 'Waiting for external result',
  COMPLETED: 'Completed',
  FAILED: 'Failed',
};

const RUN_STATUS_VARIANTS: Record<RunStatus, BadgeVariant> = {
  RUNNING: 'running',
  PAUSED: 'waiting',
  COMPLETED: 'succeeded',
  FAILED: 'failed',
};

const NODE_RUN_STATUS_LABELS: Record<NodeRunStatus, string> = {
  READY: 'Ready',
  RUNNING: 'Running',
  WAITING_CALLBACK: 'Waiting for callback',
  SUCCEEDED: 'Succeeded',
  FAILED: 'Failed',
};

const NODE_RUN_STATUS_VARIANTS: Record<NodeRunStatus, BadgeVariant> = {
  READY: 'neutral',
  RUNNING: 'running',
  WAITING_CALLBACK: 'waiting',
  SUCCEEDED: 'succeeded',
  FAILED: 'failed',
};

export function runStatusLabel(status: RunStatus): string {
  return RUN_STATUS_LABELS[status] ?? status;
}

export function runStatusVariant(status: RunStatus): BadgeVariant {
  return RUN_STATUS_VARIANTS[status] ?? 'neutral';
}

export function nodeRunStatusLabel(status: NodeRunStatus): string {
  return NODE_RUN_STATUS_LABELS[status] ?? status;
}

export function nodeRunStatusVariant(status: NodeRunStatus): BadgeVariant {
  return NODE_RUN_STATUS_VARIANTS[status] ?? 'neutral';
}

const NODE_ATTEMPT_STATUS_VARIANTS: Record<NodeAttemptStatus, BadgeVariant> = {
  STARTED: 'running',
  DISPATCHED: 'waiting',
  SUCCEEDED: 'succeeded',
  FAILED: 'failed',
};

export function nodeAttemptStatusVariant(status: NodeAttemptStatus): BadgeVariant {
  return NODE_ATTEMPT_STATUS_VARIANTS[status] ?? 'neutral';
}

/**
 * Agent Trace statuses. They are separate maps rather than reuses of the NodeRun tables
 * because the vocabularies differ: a Turn succeeds as `COMPLETED` while an Action and a
 * Tool Attempt succeed as `SUCCEEDED`.
 */
const AGENT_TURN_STATUS_VARIANTS: Record<AgentTurnStatus, BadgeVariant> = {
  READY: 'neutral',
  RUNNING: 'running',
  COMPLETED: 'succeeded',
  FAILED: 'failed',
};

const AGENT_ACTION_STATUS_VARIANTS: Record<AgentActionStatus, BadgeVariant> = {
  READY: 'neutral',
  RUNNING: 'running',
  WAITING_CALLBACK: 'waiting',
  SUCCEEDED: 'succeeded',
  FAILED: 'failed',
};

const TOOL_ATTEMPT_STATUS_VARIANTS: Record<ToolAttemptStatus, BadgeVariant> = {
  STARTED: 'running',
  DISPATCHED: 'waiting',
  SUCCEEDED: 'succeeded',
  FAILED: 'failed',
};

export function agentTurnStatusVariant(status: AgentTurnStatus): BadgeVariant {
  return AGENT_TURN_STATUS_VARIANTS[status] ?? 'neutral';
}

export function agentActionStatusVariant(status: AgentActionStatus): BadgeVariant {
  return AGENT_ACTION_STATUS_VARIANTS[status] ?? 'neutral';
}

export function toolAttemptStatusVariant(status: ToolAttemptStatus): BadgeVariant {
  return TOOL_ATTEMPT_STATUS_VARIANTS[status] ?? 'neutral';
}

/** Border colour used by Canvas nodes. A node with no NodeRun stays neutral idle. */
export function nodeRunBorderClass(status: NodeRunStatus | undefined): string {
  switch (status) {
    case 'READY':
      return 'border-[var(--muted-foreground)]';
    case 'RUNNING':
      return 'border-[var(--status-running-dot)]';
    case 'WAITING_CALLBACK':
      return 'border-[var(--status-waiting-dot)]';
    case 'SUCCEEDED':
      return 'border-[var(--status-succeeded-dot)]';
    case 'FAILED':
      return 'border-[var(--status-failed-dot)]';
    default:
      return 'border-[var(--border)]';
  }
}
