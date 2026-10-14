import { useEffect, useRef } from 'react';
import { NavigationType, useLocation, useNavigate, useNavigationType } from 'react-router';

import { readListState, RETURN_PARAM, writeListState, type ListName } from './list-state';

/** How long the returned row keeps its tint; the fade itself is the CSS animation. */
export const RETURNED_MS = 1600;

/**
 * The row the viewer came back from: scrolled into view and tinted for about 1.5 s. Its
 * primary element becomes the table's Tab stop and takes focus quietly (no ring) so ↑/↓
 * and Enter carry on from it; the first key press shows the ring, a pointer press drops
 * the focus instead.
 */
export function highlightRow(root: ParentNode, key: string | undefined): boolean {
  if (!key) return false;
  const row = root.querySelector<HTMLElement>(`tr[data-row="${CSS.escape(key)}"]`);
  if (!row) return false;
  row.scrollIntoView({ block: 'nearest' });
  row.setAttribute('data-returned', '');
  window.setTimeout(() => {
    row.removeAttribute('data-returned');
  }, RETURNED_MS);
  const primary = row.querySelector<HTMLElement>('[data-row-primary]');
  if (!primary) return true;
  row.parentElement?.querySelectorAll<HTMLElement>('[data-row-primary]').forEach((p) => {
    p.tabIndex = p === primary ? 0 : -1;
  });
  primary.setAttribute('data-quiet-focus', '');
  primary.focus({ preventScroll: true });
  const wake = (e: Event) => {
    document.removeEventListener('keydown', wake, true);
    document.removeEventListener('pointerdown', wake, true);
    if (e.type === 'pointerdown') primary.blur();
    primary.removeAttribute('data-quiet-focus');
  };
  document.addEventListener('keydown', wake, true);
  document.addEventListener('pointerdown', wake, true);
  primary.addEventListener(
    'blur',
    () => {
      primary.removeAttribute('data-quiet-focus');
    },
    { once: true },
  );
  return true;
}

/**
 * A list page remembers its query, scroll position and the row last opened, and restores
 * them when the viewer comes back (a back link with ret=1, or browser Back). `ready` is
 * true once the rows are on screen, so the restore waits for them.
 */
export function useListMemory(list: ListName, ready: boolean): void {
  const location = useLocation();
  const navigate = useNavigate();
  const navigationType = useNavigationType();
  const restoring = useRef(
    new URLSearchParams(location.search).get(RETURN_PARAM) === '1' ||
      navigationType === NavigationType.Pop,
  );

  // The query is saved on every change; the return marker itself is never saved.
  useEffect(() => {
    const q = new URLSearchParams(location.search);
    q.delete(RETURN_PARAM);
    const s = q.toString();
    writeListState(list, { qs: s ? `?${s}` : '' });
  }, [list, location.search]);

  // The scroll position is saved when the page is left, by a link or by closing the tab.
  useEffect(() => {
    const save = () => {
      writeListState(list, { scroll: Math.round(window.scrollY) });
    };
    const onClick = (e: MouseEvent) => {
      const row = (e.target as Element | null)?.closest?.<HTMLElement>('tr[data-row]');
      const key = row?.dataset.row;
      if (key) writeListState(list, { opened: key });
      save();
    };
    window.addEventListener('pagehide', save);
    document.addEventListener('click', onClick, true);
    return () => {
      window.removeEventListener('pagehide', save);
      document.removeEventListener('click', onClick, true);
      save();
    };
  }, [list]);

  useEffect(() => {
    if (!ready || !restoring.current) return;
    restoring.current = false;
    const state = readListState(list);
    if (state.scroll) window.scrollTo(0, state.scroll);
    highlightRow(document, state.opened);
    const q = new URLSearchParams(location.search);
    if (q.has(RETURN_PARAM)) {
      q.delete(RETURN_PARAM);
      const s = q.toString();
      void navigate(
        { pathname: location.pathname, search: s ? `?${s}` : '', hash: location.hash },
        { replace: true, preventScrollReset: true },
      );
    }
  }, [ready, list, location, navigate]);
}
