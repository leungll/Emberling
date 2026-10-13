import { useEffect, useState } from 'react';

/**
 * The viewer's clock, for relative times and live "so far" durations only. A duration of a
 * finished record always comes from server timestamps, never from this clock.
 *
 * `intervalMs` ticks re-render the caller; pass null to read the time once.
 */
export function useNow(intervalMs: number | null = 30_000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    if (intervalMs === null) return;
    const id = window.setInterval(() => {
      setNow(new Date());
    }, intervalMs);
    return () => {
      window.clearInterval(id);
    };
  }, [intervalMs]);
  return now;
}
