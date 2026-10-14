import { useEffect, useState } from 'react';

import { listRuns } from '@/api/client';
import type { RunListItem, RunStatusCounts } from '@/api/types';
import { RANGE_MS, STATUS_OF_PARAM, type RangeParam, type StatusParam } from '@/lib/run-words';

/**
 * The run list of one workflow's Runs tab: the newest page under the status and start
 * filters, and older pages on request. Statuses and counts are the server's; the list is
 * read again when `reload` changes, such as when the open run's status changes.
 */

export const RUN_LIST_LIMIT = 50;

interface Loaded {
  key: string;
  items: RunListItem[];
  counts: RunStatusCounts | null;
  next: string | null;
  /** The start bound the first page used; older pages reuse it with the cursor. */
  after: string | undefined;
  failed: boolean;
  more: boolean;
}

export interface WorkflowRuns {
  items: RunListItem[];
  counts: RunStatusCounts | null;
  state: 'loading' | 'error' | 'ready';
  hasMore: boolean;
  loadingMore: boolean;
  loadMore: () => void;
}

/** The start bound for a range, floored to the minute so repeated reads agree. */
export function rangeStart(range: RangeParam | null, now: Date): string | undefined {
  if (!range) return undefined;
  return new Date(Math.floor((now.getTime() - RANGE_MS[range]) / 60_000) * 60_000).toISOString();
}

export function useWorkflowRuns(
  workflowId: string,
  status: StatusParam | null,
  range: RangeParam | null,
  reload: number,
): WorkflowRuns {
  const key = `${workflowId}|${status ?? ''}|${range ?? ''}|${String(reload)}`;
  const [loaded, setLoaded] = useState<Loaded | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const after = rangeStart(range, new Date());
    listRuns(
      {
        workflowId,
        ...(status ? { status: STATUS_OF_PARAM[status] } : {}),
        ...(after ? { startedAfter: after } : {}),
        limit: RUN_LIST_LIMIT,
      },
      controller.signal,
    ).then(
      (page) => {
        setLoaded({
          key,
          items: page.items,
          counts: page.counts,
          next: page.nextCursor,
          after,
          failed: false,
          more: false,
        });
      },
      () => {
        if (controller.signal.aborted) return;
        setLoaded((l) =>
          l?.key === key
            ? { ...l, failed: true }
            : { key, items: [], counts: null, next: null, after, failed: true, more: false },
        );
      },
    );
    return () => {
      controller.abort();
    };
  }, [key, workflowId, status, range]);

  // A reload keeps the rows on screen until the new page arrives.
  const sameFilters =
    loaded?.key.split('|').slice(0, 3).join('|') === key.split('|').slice(0, 3).join('|');
  const current = loaded?.key === key ? loaded : sameFilters ? loaded : null;

  const loadMore = () => {
    if (!current?.next || current.more) return;
    const { next, after } = current;
    setLoaded({ ...current, more: true });
    listRuns({
      workflowId,
      ...(status ? { status: STATUS_OF_PARAM[status] } : {}),
      ...(after ? { startedAfter: after } : {}),
      cursor: next,
      limit: RUN_LIST_LIMIT,
    }).then(
      (page) => {
        setLoaded((l) =>
          l?.key === current.key
            ? {
                ...l,
                items: [
                  ...l.items,
                  ...page.items.filter((i) => !l.items.some((x) => x.id === i.id)),
                ],
                next: page.nextCursor,
                more: false,
              }
            : l,
        );
      },
      () => {
        setLoaded((l) => (l?.key === current.key ? { ...l, more: false } : l));
      },
    );
  };

  return {
    items: current?.items ?? [],
    counts: current?.counts ?? null,
    state: !current
      ? 'loading'
      : current.failed && !current.items.length
        ? 'error'
        : current.key === key
          ? 'ready'
          : 'loading',
    hasMore: Boolean(current?.next),
    loadingMore: current?.more ?? false,
    loadMore,
  };
}
