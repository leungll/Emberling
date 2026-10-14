import { create } from 'zustand';

import { readString, writeString } from '@/lib/storage';
import { browserZone, resolveZone, zoneIsValid, type TimeZonePref } from '@/lib/time';

/**
 * Viewer preferences. Only the time zone exists: there is no theme and no language
 * setting. The choice is kept in localStorage `emb.tz` (absent means the browser's zone)
 * and every time on the page re-renders when it changes.
 */
export const TZ_KEY = 'emb.tz';

function initialPref(): TimeZonePref {
  const saved = readString('local', TZ_KEY);
  return saved && zoneIsValid(saved) ? saved : 'local';
}

interface PrefsState {
  timeZonePref: TimeZonePref;
  /** The browser's own zone, read once. */
  localZone: string;
  setTimeZone: (pref: TimeZonePref) => void;
}

export const usePrefsStore = create<PrefsState>((set) => ({
  timeZonePref: initialPref(),
  localZone: browserZone(),
  setTimeZone: (pref) => {
    if (pref !== 'local' && !zoneIsValid(pref)) return;
    writeString('local', TZ_KEY, pref === 'local' ? null : pref);
    set({ timeZonePref: pref });
  },
}));

/** The IANA zone every time on the page is shown in. */
export function useTimeZone(): string {
  const pref = usePrefsStore((s) => s.timeZonePref);
  const local = usePrefsStore((s) => s.localZone);
  return resolveZone(pref, local);
}
