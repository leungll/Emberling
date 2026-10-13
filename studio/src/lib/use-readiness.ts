import { useEffect, useState } from 'react';

import { getReadiness, type Readiness } from '@/api/client';

/** The sidebar's connection state: unknown until the first probe answers. */
export type Connection = Readiness | 'checking';

export const READY_POLL_MS = 15_000;
/** A Backend that cannot be reached is asked less often. */
export const UNREACHABLE_POLL_MS = 60_000;

export function nextPollMs(state: Readiness): number {
  return state === 'unreachable' ? UNREACHABLE_POLL_MS : READY_POLL_MS;
}

/**
 * Polls the readiness probe while the page is visible: every 15 s, backing off to 60 s
 * while the Backend cannot be reached. A hidden page stops polling and checks again as soon
 * as it is shown. Nothing else depends on this; it only tells the viewer.
 */
export function useReadiness(probe: (signal: AbortSignal) => Promise<Readiness> = getReadiness) {
  const [state, setState] = useState<Connection>('checking');

  useEffect(() => {
    let timer: number | undefined;
    let controller: AbortController | null = null;
    let stopped = false;

    const stop = () => {
      window.clearTimeout(timer);
      timer = undefined;
      controller?.abort();
      controller = null;
    };
    const check = () => {
      stop();
      const current = new AbortController();
      controller = current;
      probe(current.signal).then(
        (result) => {
          if (stopped || current.signal.aborted) return;
          setState(result);
          if (document.visibilityState !== 'hidden') {
            timer = window.setTimeout(check, nextPollMs(result));
          }
        },
        () => {
          // An aborted probe is replaced by the next one; nothing to record.
        },
      );
    };
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') stop();
      else check();
    };

    if (document.visibilityState !== 'hidden') check();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stopped = true;
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [probe]);

  return state;
}
