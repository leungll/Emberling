/**
 * Time and duration display. One rule for every page:
 *
 * - within 24 hours of now a time is relative ("just now", "26 min ago", "in 2 min");
 *   otherwise it is absolute, "YYYY-MM-DD HH:mm", 24-hour, in the viewer's time zone;
 * - seconds appear only in tooltips, never milliseconds;
 * - a duration is formatted from a value the Backend reported (or the difference of two
 *   timestamps on the same record); this module never invents one.
 *
 * Every function here is pure: the caller passes the time zone and the current instant.
 */
import { copy } from '@/lib/copy';

const t = copy.time;

/** The viewer's preference: the browser's zone, UTC, or an IANA zone name. */
export type TimeZonePref = 'local' | 'UTC' | (string & {});

export function zoneIsValid(zone: string): boolean {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

export function resolveZone(pref: TimeZonePref, local: string = browserZone()): string {
  return pref === 'local' ? local : pref;
}

const formatters = new Map<string, Intl.DateTimeFormat>();
function formatter(kind: 'parts' | 'offset' | 'abbr', zone: string): Intl.DateTimeFormat {
  const key = `${kind}|${zone}`;
  let f = formatters.get(key);
  if (!f) {
    const options: Intl.DateTimeFormatOptions =
      kind === 'parts'
        ? {
            timeZone: zone,
            hourCycle: 'h23',
            year: 'numeric',
            month: '2-digit',
            day: '2-digit',
            hour: '2-digit',
            minute: '2-digit',
            second: '2-digit',
          }
        : kind === 'offset'
          ? { timeZone: zone, timeZoneName: 'longOffset', hour: '2-digit' }
          : { timeZone: zone, timeZoneName: 'short' };
    f = new Intl.DateTimeFormat('en-US', options);
    formatters.set(key, f);
  }
  return f;
}

interface Parts {
  year: string;
  month: string;
  day: string;
  hour: string;
  minute: string;
  second: string;
}

function parts(date: Date, zone: string): Parts {
  const p: Record<string, string> = {};
  for (const x of formatter('parts', zone).formatToParts(date)) p[x.type] = x.value;
  return {
    year: p.year ?? '',
    month: p.month ?? '',
    day: p.day ?? '',
    hour: p.hour === '24' ? '00' : (p.hour ?? ''),
    minute: p.minute ?? '',
    second: p.second ?? '',
  };
}

function parse(iso: string | null | undefined): Date | null {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** Minutes east of UTC for a zone at an instant. */
export function zoneOffsetMinutes(zone: string, at: Date): number {
  try {
    const name =
      formatter('offset', zone)
        .formatToParts(at)
        .find((x) => x.type === 'timeZoneName')?.value ?? 'GMT';
    const m = /GMT([+-])(\d{1,2})(?::?(\d{2}))?/.exec(name);
    if (!m) return 0;
    return (m[1] === '-' ? -1 : 1) * (Number(m[2]) * 60 + Number(m[3] ?? '0'));
  } catch {
    return 0;
  }
}

/** "UTC−7", "UTC+5:30", "UTC+0": a true minus sign, minutes only when not zero. */
export function offsetLabel(minutes: number): string {
  const a = Math.abs(minutes);
  const h = Math.floor(a / 60);
  const m = a % 60;
  return `UTC${minutes < 0 ? '−' : '+'}${h}${m ? `:${String(m).padStart(2, '0')}` : ''}`;
}

export function zoneOffset(zone: string, at: Date): string {
  return offsetLabel(zoneOffsetMinutes(zone, at));
}

// Browsers still report a few zones by their old IANA names; show today's city name.
const CITY_NOW: Record<string, string> = {
  Calcutta: 'Kolkata',
  Saigon: 'Ho Chi Minh',
  Kiev: 'Kyiv',
  Rangoon: 'Yangon',
  Katmandu: 'Kathmandu',
  Godthab: 'Nuuk',
};

/** "Los Angeles" for America/Los_Angeles; "UTC" for UTC. */
export function zoneCity(zone: string): string {
  if (zone === 'UTC') return 'UTC';
  const last = zone.split('/').pop() ?? zone;
  return (CITY_NOW[last] ?? last).replace(/_/g, ' ');
}

export function zoneAbbreviation(zone: string, at: Date): string {
  try {
    return (
      formatter('abbr', zone)
        .formatToParts(at)
        .find((x) => x.type === 'timeZoneName')?.value ?? ''
    );
  } catch {
    return '';
  }
}

/** Always relative, at any distance: for tooltips and wherever a span is the point. */
export function relativeWords(iso: string | null | undefined, now: Date): string {
  const d = parse(iso);
  if (!d) return t.missing;
  const s = Math.round((now.getTime() - d.getTime()) / 1000);
  const a = Math.abs(s);
  if (a < 45) return t.justNow;
  const m = Math.round(a / 60);
  const h = Math.round(a / 3600);
  const days = Math.round(a / 86400);
  let text: string;
  if (m < 60) text = `${m} ${t.min}`;
  else if (h < 24) text = `${h} ${t.hours}`;
  else if (days < 60) text = `${days} ${t.day(days)}`;
  else {
    const months = Math.round(days / 30);
    const years = Math.round(days / 365);
    text = months < 12 ? `${months} ${t.months}` : `${years} ${t.year(years)}`;
  }
  return s < 0 ? t.in(text) : t.ago(text);
}

/** "YYYY-MM-DD HH:mm" in the zone; the year is always shown. */
export function absoluteTime(iso: string | null | undefined, zone: string): string {
  const d = parse(iso);
  if (!d) return t.missing;
  const p = parts(d, zone);
  return `${p.year}-${p.month}-${p.day} ${p.hour}:${p.minute}`;
}

/** Relative within 24 hours of now, absolute otherwise. */
export function listTime(iso: string | null | undefined, now: Date, zone: string): string {
  const d = parse(iso);
  if (!d) return t.missing;
  return Math.abs(now.getTime() - d.getTime()) < 86_400_000
    ? relativeWords(iso, now)
    : absoluteTime(iso, zone);
}

/** "YYYY-MM-DD HH:mm:ss" in the zone. */
export function exactSeconds(iso: string | null | undefined, zone: string): string {
  const d = parse(iso);
  if (!d) return '';
  const p = parts(d, zone);
  return `${p.year}-${p.month}-${p.day} ${p.hour}:${p.minute}:${p.second}`;
}

/** The tooltip of a time: seconds, the zone, and the relative form. */
export function exactTime(iso: string | null | undefined, now: Date, zone: string): string {
  const d = parse(iso);
  if (!d) return '';
  const where = zone === 'UTC' ? 'UTC' : copy.tz.zoneTime(zoneCity(zone), zoneOffset(zone, d));
  const rel = relativeWords(iso, now);
  return `${exactSeconds(iso, zone)}, ${where}. ${rel.charAt(0).toUpperCase()}${rel.slice(1)}.`;
}

/** "<1 ms", "850 ms", "4.2 s", "1 min 04 s", "2 h 03 min": at most two units. */
export function duration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '';
  const v = Math.max(0, ms);
  if (v < 1) return '<1 ms';
  if (v < 1000) return `${Math.round(v)} ms`;
  if (v < 60_000) return `${(v / 1000).toFixed(1).replace(/\.0$/, '')} s`;
  const sec = Math.round(v / 1000);
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return `${m} min ${String(s).padStart(2, '0')} s`;
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')} min`;
}

/** The exact value of a duration for its tooltip: "22.612 s". */
export function secondsText(ms: number): string {
  return `${(Math.max(0, ms) / 1000).toFixed(3)} s`;
}

/** An offset from a run's start: "+0 ms", "+3.2 s", "+1 min 04 s". */
export function offsetText(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '';
  return `+${ms <= 0 ? '0 ms' : duration(ms)}`;
}

/** A deadline relative to now, to the second: "in 4 min 00 s", "12 s ago". */
export function untilText(iso: string, now: Date): string {
  const d = parse(iso);
  if (!d) return t.missing;
  const diff = d.getTime() - now.getTime();
  const rounded = Math.round(Math.abs(diff) / 1000) * 1000;
  return diff >= 0 ? t.in(duration(rounded)) : t.ago(duration(rounded));
}

/** Human byte sizes: "0 B", "512 B", "1.2 KB", "3.4 MB". */
export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  const text = i === 0 ? String(Math.round(v)) : v.toFixed(v < 10 ? 1 : 0).replace(/\.0$/, '');
  return `${text} ${units[i] ?? ''}`;
}

/** A configured limit in milliseconds, in human units; the stored value is unchanged. */
export function limitText(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return '';
  if (ms % 60_000 === 0) return `${ms / 60_000} min`;
  if (ms >= 60_000) return `${(ms / 60_000).toFixed(1).replace(/\.0$/, '')} min`;
  if (ms % 1000 === 0) return `${ms / 1000} s`;
  return `${ms} ms`;
}

export interface ZoneOption {
  id: string;
  city: string;
  minutes: number;
  offset: string;
  search: string;
}

/** Every way a viewer might type a zone: city, IANA ID, abbreviation, or an offset. */
export function zoneSearchText(zone: string, minutes: number, at: Date): string {
  const a = Math.abs(minutes);
  const h = Math.floor(a / 60);
  const m = a % 60;
  const sign = minutes < 0 ? '-' : '+';
  const hh = String(h).padStart(2, '0');
  const mm = String(m).padStart(2, '0');
  const offsets = [`${sign}${h}${m ? `:${mm}` : ''}`, `${sign}${hh}:${mm}`, `${sign}${hh}${mm}`];
  const forms = offsets.flatMap((o) => [o, `utc${o}`, `gmt${o}`]);
  return [zoneCity(zone), zone.replace(/[_/]/g, ' '), zone, zoneAbbreviation(zone, at), ...forms]
    .join(' ')
    .toLowerCase();
}

/** Normalises a search for zones: a typed minus sign and spacing do not matter. */
export function zoneQuery(raw: string): string {
  return raw.trim().toLowerCase().replace(/−/g, '-').replace(/\s+/g, ' ');
}

export function zoneMatches(search: string, query: string): boolean {
  return !query || search.includes(query) || search.includes(query.replace(/\s/g, ''));
}

/** The named zones, sorted by offset and then city. */
export function allZones(at: Date, names?: readonly string[]): ZoneOption[] {
  let list: readonly string[] = names ?? [];
  if (!names) {
    try {
      list = Intl.supportedValuesOf('timeZone');
    } catch {
      list = [];
    }
  }
  return list
    .filter((n) => n !== 'UTC' && n.indexOf('/') > 0 && !n.startsWith('Etc/'))
    .map((id) => {
      const minutes = zoneOffsetMinutes(id, at);
      return {
        id,
        city: zoneCity(id),
        minutes,
        offset: offsetLabel(minutes),
        search: zoneSearchText(id, minutes, at),
      };
    })
    .sort((a, b) => a.minutes - b.minutes || a.city.localeCompare(b.city));
}
