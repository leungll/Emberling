import type {
  AgentActionStatus,
  AgentTurnStatus,
  NodeAttemptStatus,
  NodeRunStatus,
  RunStatus,
  ToolAttemptStatus,
} from '@/api/types';
import { copy } from '@/lib/copy';

/**
 * The closed set of status tones. Each maps to one icon and one set of status tokens.
 * Caution (an orange triangle) is a note, never a status.
 */
export type Tone = 'running' | 'waiting' | 'succeeded' | 'failed' | 'queued' | 'inactive';

/** A step with no NodeRun yet: NONE while the run goes on, NONE_RUN_FAILED after it failed. */
export type StepDisplayStatus = NodeRunStatus | 'NONE' | 'NONE_RUN_FAILED';
export type ConnectionStatus = 'live' | 'reconnecting' | 'final' | 'stopped';

export type StatusSubject =
  | { kind: 'run'; value: RunStatus }
  | { kind: 'runLong'; value: RunStatus }
  | { kind: 'step'; value: StepDisplayStatus }
  | { kind: 'turn'; value: AgentTurnStatus }
  | { kind: 'action'; value: AgentActionStatus }
  | { kind: 'attempt'; value: NodeAttemptStatus | ToolAttemptStatus }
  | { kind: 'connection'; value: ConnectionStatus };

export interface StatusInfo {
  label: string;
  tone: Tone;
  /** One help sentence for the tooltip. */
  help?: string;
  /** The raw value, shown under the help in the tooltip; never as the visible word. */
  raw?: string;
  /** A different icon than the tone's own: "Did not run" uses circle-minus. */
  icon?: 'circle-minus' | 'hourglass';
}

const RUN_TONE: Record<RunStatus, Tone> = {
  RUNNING: 'running',
  PAUSED: 'waiting',
  COMPLETED: 'succeeded',
  FAILED: 'failed',
};

const STEP_TONE: Record<StepDisplayStatus, Tone> = {
  READY: 'queued',
  RUNNING: 'running',
  WAITING_CALLBACK: 'waiting',
  SUCCEEDED: 'succeeded',
  FAILED: 'failed',
  NONE: 'inactive',
  NONE_RUN_FAILED: 'inactive',
};

const GENERIC_TONE: Record<string, Tone> = {
  READY: 'queued',
  RUNNING: 'running',
  STARTED: 'running',
  WAITING_CALLBACK: 'waiting',
  DISPATCHED: 'waiting',
  COMPLETED: 'succeeded',
  SUCCEEDED: 'succeeded',
  FAILED: 'failed',
};

const CONNECTION_TONE: Record<ConnectionStatus, Tone> = {
  live: 'running',
  reconnecting: 'waiting',
  final: 'inactive',
  stopped: 'inactive',
};

/**
 * Word, tone and help for a status the Backend reported. A lookup only: no status is ever
 * derived from another here. An unknown value shows as itself with the inactive tone.
 */
export function statusInfo(subject: StatusSubject): StatusInfo {
  const s = copy.status;
  switch (subject.kind) {
    case 'run':
    case 'runLong': {
      const entry = s.run[subject.value] as (typeof s.run)[RunStatus] | undefined;
      if (!entry) return { label: subject.value, tone: 'inactive', raw: subject.value };
      return {
        label: subject.kind === 'runLong' ? entry.long : entry.short,
        tone: RUN_TONE[subject.value],
        help: entry.help,
        raw: subject.value,
      };
    }
    case 'step': {
      const entry = s.step[subject.value] as (typeof s.step)[StepDisplayStatus] | undefined;
      if (!entry) return { label: subject.value, tone: 'inactive', raw: subject.value };
      const synthetic = subject.value === 'NONE' || subject.value === 'NONE_RUN_FAILED';
      return {
        label: entry.label,
        tone: STEP_TONE[subject.value],
        help: entry.help,
        ...(synthetic ? {} : { raw: subject.value }),
        ...(subject.value === 'NONE_RUN_FAILED' ? { icon: 'circle-minus' as const } : {}),
      };
    }
    case 'connection': {
      const entry = s.connection[subject.value] as
        (typeof s.connection)[ConnectionStatus] | undefined;
      if (!entry) return { label: subject.value, tone: 'inactive' };
      return { label: entry.label, tone: CONNECTION_TONE[subject.value], help: entry.help };
    }
    case 'turn':
    case 'action':
    case 'attempt': {
      const table: Record<string, string> = s[subject.kind];
      const tone =
        subject.kind === 'action' && subject.value === 'READY'
          ? 'inactive'
          : (GENERIC_TONE[subject.value] ?? 'inactive');
      return { label: table[subject.value] ?? subject.value, tone, raw: subject.value };
    }
  }
}
