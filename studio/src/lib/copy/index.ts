import { en } from './en';

/**
 * The shape every language table must match. Only English exists; a second table would be
 * typed as `Copy` so its keys and function signatures stay complete.
 */
export type Copy = typeof en;

/** The copy table for code outside React (helpers, toasts, document titles). */
export const copy: Copy = en;

/** The copy table for components. It returns English until a second language is complete. */
export function useCopy(): Copy {
  return en;
}
