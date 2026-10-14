/**
 * Per-browser conveniences in localStorage and sessionStorage (time zone, sidebar, recent
 * workflows, list positions). None of them is an execution fact. Storage can be missing,
 * full or blocked, so every read falls back to a default and every write may silently fail.
 */
type Area = 'local' | 'session';

function area(kind: Area): Storage | null {
  try {
    return kind === 'local' ? window.localStorage : window.sessionStorage;
  } catch {
    return null;
  }
}

export function readString(kind: Area, key: string): string | null {
  try {
    return area(kind)?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

export function writeString(kind: Area, key: string, value: string | null): void {
  try {
    const store = area(kind);
    if (!store) return;
    if (value === null) store.removeItem(key);
    else store.setItem(key, value);
  } catch {
    // Storage is a convenience; the page keeps working without it.
  }
}

export function readJson<T>(
  kind: Area,
  key: string,
  fallback: T,
  valid: (v: unknown) => v is T,
): T {
  const raw = readString(kind, key);
  if (raw === null) return fallback;
  try {
    const parsed: unknown = JSON.parse(raw);
    return valid(parsed) ? parsed : fallback;
  } catch {
    return fallback;
  }
}

export function writeJson(kind: Area, key: string, value: unknown): void {
  writeString(kind, key, JSON.stringify(value));
}
