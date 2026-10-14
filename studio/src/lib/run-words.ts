import type { RunListItem, RunStatus } from '@/api/types';
import { copy } from '@/lib/copy';

/**
 * Words about a run that both run lists share: what it stopped on, what it waits for, and
 * whether it used an older version. Built only from the fields the run list returns.
 */

/** The status filter's URL value for each run status. */
export type StatusParam = 'running' | 'waiting' | 'failed' | 'completed';

export const STATUS_PARAMS: readonly StatusParam[] = ['running', 'waiting', 'failed', 'completed'];

export const STATUS_OF_PARAM: Record<StatusParam, RunStatus> = {
  running: 'RUNNING',
  waiting: 'PAUSED',
  failed: 'FAILED',
  completed: 'COMPLETED',
};

export function statusParam(value: string | null | undefined): StatusParam | null {
  return STATUS_PARAMS.includes(value as StatusParam) ? (value as StatusParam) : null;
}

/** The start-time filter: how far back a run may have started. */
export type RangeParam = '24h' | '7d';

export const RANGE_MS: Record<RangeParam, number> = { '24h': 86_400_000, '7d': 7 * 86_400_000 };

export function rangeParam(value: string | null | undefined): RangeParam | null {
  return value === '24h' || value === '7d' ? value : null;
}

/** One plain sentence per failure: what failed and, when the code says, why. */
export function failureSentence(item: Pick<RunListItem, 'error' | 'failedStep'>): string {
  const f = copy.runs.failure;
  if (!item.error) return f.unknown;
  const step = item.failedStep?.nodeName ?? null;
  const byCode = f.byCode[item.error.code];
  return byCode ? byCode(step) : f.other(step);
}

/** What a waiting run waits for: the steps the server says are waiting. */
export function waitingSentence(item: Pick<RunListItem, 'waitingSteps'>): string {
  return copy.runs.waitingFor(item.waitingSteps.map((s) => s.nodeName));
}

/** The second line of a run in a list: the failure or the wait, else nothing. */
export function runSubline(
  item: Pick<RunListItem, 'status' | 'error' | 'failedStep' | 'waitingSteps'>,
): string {
  if (item.status === 'FAILED') return failureSentence(item);
  if (item.status === 'PAUSED') return waitingSentence(item);
  return '';
}

/** True when the run used an older version than the workflow's latest. */
export function usedOlderVersion(
  item: Pick<RunListItem, 'definitionVersion' | 'latestVersion'>,
): boolean {
  return item.latestVersion > item.definitionVersion;
}

export function olderTip(item: Pick<RunListItem, 'definitionVersion' | 'latestVersion'>): string {
  return copy.runs.olderTip(item.definitionVersion, item.latestVersion);
}

/** A run that has not ended: its duration counts up from its start, marked "so far". */
export function isLive(status: RunStatus): boolean {
  return status === 'RUNNING' || status === 'PAUSED';
}

/** The duration of an ended run from its own two timestamps; null while it is live. */
export function endedDurationMs(
  item: Pick<RunListItem, 'startedAt' | 'completedAt'>,
): number | null {
  if (!item.completedAt) return null;
  return new Date(item.completedAt).getTime() - new Date(item.startedAt).getTime();
}
