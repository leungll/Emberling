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
