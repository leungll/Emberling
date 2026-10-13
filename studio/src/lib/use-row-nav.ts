import { useCallback, type KeyboardEvent, type FocusEvent } from 'react';

/**
 * Keyboard model for list tables: one Tab stop for the whole table. Each row's primary
 * element (marked data-row-primary) carries the roving tabindex. ↑/↓ move between rows,
 * Home/End (on the primary element) jump to the first or last row, →/← move between the
 * row's other controls (marked data-row-control), and Shift+F10 or the Menu key opens the
 * row's actions menu (marked data-row-menu). Enter follows the primary link natively.
 *
 * Attach the returned handlers to the tbody; call `reset` after the rows change to give
 * the Tab stop to `keep` (or the first row).
 */
export function useRowNav() {
  const rowsOf = (body: HTMLElement) =>
    Array.from(body.querySelectorAll<HTMLElement>('tr[data-row]'));
  const primaryOf = (row: HTMLElement | undefined) =>
    row?.querySelector<HTMLElement>('[data-row-primary]') ?? null;
  const controlsOf = (row: HTMLElement) =>
    Array.from(row.querySelectorAll<HTMLElement>('[data-row-control]'));

  const reset = useCallback((body: HTMLElement | null, keep?: string | null) => {
    if (!body) return;
    const rows = rowsOf(body);
    const keepRow = keep ? rows.find((r) => r.dataset.row === keep) : undefined;
    rows.forEach((row, i) => {
      for (const c of controlsOf(row)) c.tabIndex = -1;
      const p = primaryOf(row);
      if (p) p.tabIndex = (keepRow ? row === keepRow : i === 0) ? 0 : -1;
    });
  }, []);

  const onFocus = useCallback((e: FocusEvent<HTMLElement>) => {
    const row = e.target.closest<HTMLElement>('tr[data-row]');
    if (!row) return;
    for (const r of rowsOf(e.currentTarget)) {
      const p = primaryOf(r);
      if (p) p.tabIndex = r === row ? 0 : -1;
    }
  }, []);

  const onKeyDown = useCallback((e: KeyboardEvent<HTMLElement>) => {
    const target = e.target as HTMLElement;
    const row = target.closest<HTMLElement>('tr[data-row]');
    if (!row) return;
    if ((e.key === 'F10' && e.shiftKey) || e.key === 'ContextMenu') {
      const menu = row.querySelector<HTMLElement>('[data-row-menu]');
      if (menu && !target.closest('[data-row-menu]')) {
        e.preventDefault();
        menu.focus();
        menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      }
      return;
    }
    if (e.metaKey || e.ctrlKey || e.altKey || target.matches('input, textarea')) return;
    const rows = rowsOf(e.currentTarget);
    const i = rows.indexOf(row);
    const primary = primaryOf(row);
    const controls = controlsOf(row);
    const ci = controls.indexOf(target);
    let next: HTMLElement | null | undefined;
    if (e.key === 'ArrowDown') next = primaryOf(rows[i + 1]);
    else if (e.key === 'ArrowUp') next = primaryOf(rows[i - 1]);
    else if (e.key === 'Home' && target === primary) next = primaryOf(rows[0]);
    else if (e.key === 'End' && target === primary) next = primaryOf(rows.at(-1));
    else if (e.key === 'ArrowRight') next = controls[Math.min(controls.length - 1, ci + 1)];
    else if (e.key === 'ArrowLeft') next = ci <= 0 ? primary : controls[ci - 1];
    else return;
    e.preventDefault();
    next?.focus();
  }, []);

  return { reset, onFocus, onKeyDown };
}
