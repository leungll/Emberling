/** Display helpers. None of these derive execution facts; they only format server values. */

export function formatTimestamp(value: string | null | undefined): string {
  if (!value) return '—';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, 'Z');
}

export function formatDuration(fromIso: string | null | undefined, toIso?: string | null): string {
  if (!fromIso) return '—';
  const from = new Date(fromIso).getTime();
  const to = toIso ? new Date(toIso).getTime() : Date.now();
  if (Number.isNaN(from) || Number.isNaN(to)) return '—';
  return formatMillis(Math.max(0, to - from));
}

export function formatMillis(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return '—';
  if (ms < 1000) return `${ms} ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${Math.floor(seconds % 60)}s`;
}

/** Time-of-day part of a server timestamp, in UTC like `formatTimestamp` (`12:00:03Z`). */
export function formatClock(value: string | null | undefined): string {
  const full = formatTimestamp(value);
  return full === '—' ? full : (full.split(' ')[1] ?? full);
}

/**
 * Stopwatch-style elapsed time between two server timestamps (`00:42`, `1:02:05`), with
 * `toIso` defaulting to now. Formatting only: both ends are timestamps the Backend reported.
 */
export function formatElapsed(fromIso: string | null | undefined, toIso?: string | null): string {
  if (!fromIso) return '—';
  const from = new Date(fromIso).getTime();
  const to = toIso ? new Date(toIso).getTime() : Date.now();
  if (Number.isNaN(from) || Number.isNaN(to)) return '—';
  const total = Math.floor(Math.max(0, to - from) / 1000);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${pad(minutes)}:${pad(seconds)}`;
}

/**
 * Run clock for the Observe banner and summaries: the same short units as `formatDuration`
 * (`27 ms`, `1.2 s`) below a minute, and a stopwatch (`04:53`, `1:02:05`) from a minute up,
 * so a sub-second Run never reads `00:00`. Formatting only, over server timestamps.
 */
export function formatRunClock(fromIso: string | null | undefined, toIso?: string | null): string {
  if (!fromIso) return '—';
  const from = new Date(fromIso).getTime();
  const to = toIso ? new Date(toIso).getTime() : Date.now();
  if (Number.isNaN(from) || Number.isNaN(to)) return '—';
  const ms = Math.max(0, to - from);
  return ms < 60_000 ? formatMillis(ms) : formatElapsed(fromIso, toIso);
}

export function truncate(value: string, max = 160): string {
  return value.length <= max ? value : `${value.slice(0, max)}…`;
}

/**
 * Coarse relative-time label for the Definitions table's "Updated" column (04 §1.1).
 * Formatting only: it reads a server timestamp and never derives one.
 */
export function formatRelativeTime(value: string | null | undefined): string {
  if (!value) return '—';
  const parsed = new Date(value).getTime();
  if (Number.isNaN(parsed)) return '—';
  const diffMs = Date.now() - parsed;
  if (diffMs < 0) return 'just now';

  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;

  if (diffMs < minute) return 'just now';
  if (diffMs < hour) {
    const minutes = Math.floor(diffMs / minute);
    return `${minutes} min ago`;
  }
  if (diffMs < day) {
    const hours = Math.floor(diffMs / hour);
    return `${hours} hr ago`;
  }
  if (diffMs < 2 * day) return 'Yesterday';
  const days = Math.floor(diffMs / day);
  return `${days} days ago`;
}
