import { useEffect, useRef } from 'react';

/** True while focus is somewhere text can be typed: single-key shortcuts stay off there. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  if (target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return true;
  if (target instanceof HTMLInputElement) {
    return !['button', 'checkbox', 'radio', 'range', 'submit', 'reset', 'file', 'color'].includes(
      target.type,
    );
  }
  return target.getAttribute('role') === 'textbox' || target.getAttribute('role') === 'combobox';
}

/** True when a dialog or another modal layer is open, so page shortcuts stay off. */
export function modalOpen(): boolean {
  return (
    document.querySelector('[role="dialog"][aria-modal="true"], [role="alertdialog"]') !== null
  );
}

export interface ShortcutOptions {
  /** Also fire while a dialog is open (the dialog's own keys). Default false. */
  inDialogs?: boolean;
  enabled?: boolean;
}

/**
 * A single-key shortcut such as "?" or "[". It never fires while the viewer types in a
 * field, with a modifier held, or (by default) while a dialog is open.
 */
export function useKeyShortcut(
  key: string,
  handler: (e: KeyboardEvent) => void,
  { inDialogs = false, enabled = true }: ShortcutOptions = {},
): void {
  const ref = useRef(handler);
  useEffect(() => {
    ref.current = handler;
  });
  useEffect(() => {
    if (!enabled) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== key || e.defaultPrevented) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (isTypingTarget(e.target)) return;
      if (!inDialogs && modalOpen()) return;
      e.preventDefault();
      ref.current(e);
    };
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
    };
  }, [key, inDialogs, enabled]);
}

/** One row of the shortcut sheet: the keys and what they do. */
export interface ShortcutRow {
  keys: readonly string[];
  description: string;
}

export interface ShortcutGroup {
  title: string;
  rows: readonly ShortcutRow[];
}
