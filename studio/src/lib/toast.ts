import { useSyncExternalStore } from 'react';

/**
 * Toasts report an asynchronous result that happened off screen: a save, a copy, an
 * undoable delete, a failed jump. One line and at most one action; a new toast replaces the
 * one showing. Never for field validation or page errors.
 */
export interface ToastAction {
  label: string;
  onAction: () => void;
}

export interface ToastMessage {
  id: number;
  text: string;
  action?: ToastAction;
  /** Shows a check icon before the text (copied, saved). */
  done?: boolean;
}

/** How long a toast stays: 5 s, or 8 s when it carries an action; hover pauses it. */
export function toastDuration(message: Pick<ToastMessage, 'action'>): number {
  return message.action ? 8000 : 5000;
}

let current: ToastMessage | null = null;
let nextId = 1;
const listeners = new Set<() => void>();

function emit(): void {
  for (const l of listeners) l();
}

export function showToast(
  text: string,
  options: { action?: ToastAction; done?: boolean } = {},
): void {
  current = { id: nextId++, text, ...options };
  emit();
}

export function dismissToast(id: number): void {
  if (current?.id !== id) return;
  current = null;
  emit();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useToast(): ToastMessage | null {
  return useSyncExternalStore(
    subscribe,
    () => current,
    () => null,
  );
}
