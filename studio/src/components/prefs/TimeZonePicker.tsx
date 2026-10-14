import { Check, Clock, Search } from 'lucide-react';
import { useMemo, useRef, useState, type KeyboardEvent } from 'react';

import { controlClasses } from '@/components/ui/field';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import {
  allZones,
  offsetLabel,
  zoneCity,
  zoneMatches,
  zoneOffset,
  zoneOffsetMinutes,
  zoneQuery,
  zoneSearchText,
  type TimeZonePref,
} from '@/lib/time';
import { cn } from '@/lib/utils';
import { usePrefsStore } from '@/stores/prefs-store';

interface ZoneRow {
  value: TimeZonePref;
  name: string;
  suffix?: string;
  offset: string;
  title: string;
}

/**
 * The time zone picker, the one control in every header's preferences. The trigger shows
 * the offset; the list has Detected, UTC and All time zones, and a search that matches a
 * city, an IANA ID, an abbreviation or an offset like "+8".
 */
export function TimeZonePicker() {
  const copy = useCopy();
  const tz = copy.tz;
  const pref = usePrefsStore((s) => s.timeZonePref);
  const local = usePrefsStore((s) => s.localZone);
  const setTimeZone = usePrefsStore((s) => s.setTimeZone);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const listRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const now = useMemo(() => new Date(), [open]); // eslint-disable-line react-hooks/exhaustive-deps -- re-read the offsets each time the list opens
  const zone = pref === 'local' ? local : pref;
  const trigger = pref === 'UTC' ? 'UTC' : zoneOffset(zone, now);
  const tip = pref === 'UTC' ? tz.triggerUtc : tz.trigger(zoneCity(zone), zoneOffset(zone, now));

  const zones = useMemo(() => (open ? allZones(now) : []), [open, now]);
  const q = zoneQuery(query);
  const groups: { label: string; rows: ZoneRow[] }[] = [];
  if (open) {
    const localMin = zoneOffsetMinutes(local, now);
    if (zoneMatches(`${zoneSearchText(local, localMin, now)} local this device detected`, q)) {
      groups.push({
        label: tz.detected,
        rows: [
          {
            value: 'local',
            name: zoneCity(local),
            suffix: tz.thisDevice,
            offset: offsetLabel(localMin),
            title: local,
          },
        ],
      });
    }
    if (zoneMatches('utc gmt coordinated universal time z +0 utc+0 gmt+0 +00:00', q)) {
      groups.push({
        label: tz.utc,
        rows: [{ value: 'UTC', name: tz.utcName, offset: 'UTC', title: 'UTC' }],
      });
    }
    const all = zones
      .filter((z) => zoneMatches(z.search, q))
      .map((z) => ({ value: z.id, name: z.city, offset: z.offset, title: z.id }));
    if (all.length) groups.push({ label: tz.all, rows: all });
  }

  // The list is one Tab stop: the selected zone, or the first one shown.
  const shownValues = groups.flatMap((g) => g.rows.map((r) => r.value));
  const tabStop = shownValues.includes(pref) ? pref : shownValues[0];

  const pick = (value: TimeZonePref) => {
    setTimeZone(value);
    setOpen(false);
    setQuery('');
  };

  const onListKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const options = Array.from(
      listRef.current?.querySelectorAll<HTMLElement>('[role="option"]') ?? [],
    );
    const i = options.indexOf(document.activeElement as HTMLElement);
    let next: HTMLElement | undefined;
    if (e.key === 'ArrowDown') next = options[i + 1];
    else if (e.key === 'ArrowUp') {
      if (i <= 0) {
        e.preventDefault();
        inputRef.current?.focus();
        return;
      }
      next = options[i - 1];
    } else if (e.key === 'Home') next = options[0];
    else if (e.key === 'End') next = options.at(-1);
    else return;
    e.preventDefault();
    next?.focus();
  };

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setQuery('');
      }}
    >
      <Tooltip content={tip}>
        <PopoverTrigger asChild>
          <button
            type="button"
            aria-label={tip}
            aria-haspopup="listbox"
            className="inline-flex h-control-sm shrink-0 items-center gap-1.5 rounded-md px-2 text-sm whitespace-nowrap text-fg-muted tabular-nums hover:bg-surface-hover hover:text-fg aria-expanded:bg-surface-hover aria-expanded:text-fg"
          >
            <Clock aria-hidden="true" className="size-icon-sm" />
            <span>{trigger}</span>
          </button>
        </PopoverTrigger>
      </Tooltip>
      <PopoverContent
        label={tz.label}
        align="end"
        style={{ minWidth: 340, maxWidth: 420 }}
        className="w-max"
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          inputRef.current?.focus();
        }}
      >
        <div className="relative flex items-center p-1">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute left-4 size-icon-sm text-fg-muted"
          />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
            }}
            onKeyDown={(e) => {
              const first = listRef.current?.querySelector<HTMLElement>('[role="option"]');
              if (e.key === 'Enter' && first) {
                e.preventDefault();
                first.click();
              } else if (e.key === 'ArrowDown' && first) {
                e.preventDefault();
                first.focus();
              }
            }}
            placeholder={tz.search}
            aria-label={tz.search}
            autoComplete="off"
            className={cn(controlClasses, 'h-control-md pl-8')}
          />
        </div>
        <div
          ref={listRef}
          role="listbox"
          aria-label={tz.listLabel}
          onKeyDown={onListKey}
          style={{ maxHeight: 'min(60vh, 400px)', scrollbarGutter: 'stable' }}
          className="overflow-y-auto px-1 pb-1"
        >
          {groups.length === 0 ? (
            <p style={{ maxWidth: 320 }} className="m-0 px-2 py-3 text-sm text-fg-muted">
              {tz.empty(query.trim())}
            </p>
          ) : (
            groups.map((g) => (
              <div key={g.label} role="group" aria-label={g.label}>
                <div
                  aria-hidden="true"
                  className="px-2 pt-2 pb-1 text-xs font-medium text-fg-muted"
                >
                  {g.label}
                </div>
                {g.rows.map((r) => {
                  const selected = r.value === pref;
                  return (
                    <button
                      key={r.value}
                      type="button"
                      role="option"
                      aria-selected={selected}
                      tabIndex={r.value === tabStop ? 0 : -1}
                      title={r.title}
                      onClick={() => {
                        pick(r.value);
                      }}
                      style={{ gridTemplateColumns: 'var(--icon-md) minmax(0, 1fr) auto' }}
                      className="grid h-control-sm w-full items-center gap-x-2 rounded-md px-2 text-left text-sm text-fg-secondary hover:bg-surface-hover hover:text-fg focus:bg-surface-hover focus:text-fg focus-visible:-outline-offset-2 aria-selected:font-medium aria-selected:text-fg"
                    >
                      <span className="inline-flex text-accent-fg">
                        {selected ? <Check aria-hidden="true" className="size-icon-sm" /> : null}
                      </span>
                      <span className="min-w-0 wrap-anywhere">
                        {r.name}
                        {r.suffix ? (
                          <span className="ml-1.5 font-normal text-fg-muted">{r.suffix}</span>
                        ) : null}
                      </span>
                      <span className="pl-4 font-normal whitespace-nowrap text-fg-muted tabular-nums">
                        {r.offset}
                      </span>
                    </button>
                  );
                })}
              </div>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
