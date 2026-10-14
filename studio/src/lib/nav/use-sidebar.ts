import { useCallback, useState, useSyncExternalStore } from 'react';

import { readString, writeString } from '@/lib/storage';

/**
 * The sidebar is expanded (224 px) at 1280 px and wider and a 48 px rail below. The
 * viewer's own choice, from the button or "[", is kept in localStorage `emb.sidebar` and
 * overrides the width rule from then on.
 */
export const SIDEBAR_KEY = 'emb.sidebar';
export const WIDE_QUERY = '(min-width: 1280px)';

type Pref = 'collapsed' | 'expanded' | null;

function readPref(): Pref {
  const v = readString('local', SIDEBAR_KEY);
  return v === 'collapsed' || v === 'expanded' ? v : null;
}

function subscribeWide(onChange: () => void): () => void {
  const mq = typeof window.matchMedia === 'function' ? window.matchMedia(WIDE_QUERY) : null;
  mq?.addEventListener('change', onChange);
  return () => mq?.removeEventListener('change', onChange);
}

function isWide(): boolean {
  return typeof window.matchMedia === 'function' ? window.matchMedia(WIDE_QUERY).matches : true;
}

export function useSidebarCollapsed(): [collapsed: boolean, toggle: () => void] {
  const wide = useSyncExternalStore(subscribeWide, isWide, () => true);
  const [pref, setPref] = useState<Pref>(readPref);
  const collapsed = pref ? pref === 'collapsed' : !wide;
  const toggle = useCallback(() => {
    const next = collapsed ? 'expanded' : 'collapsed';
    writeString('local', SIDEBAR_KEY, next);
    setPref(next);
  }, [collapsed]);
  return [collapsed, toggle];
}
