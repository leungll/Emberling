import type { JsonObject, JsonValue } from '@/api/types';
import { useEditorStore } from '@/features/editor/editor-store';

/**
 * Edits one step's settings. A typed value records one undo step per field while the user
 * keeps typing in it; a picked value records its own step.
 */
export function configEdit(nodeId: string) {
  const store = useEditorStore.getState;
  return {
    set(key: string, value: JsonValue | undefined, typing = false) {
      store().begin(typing ? `${nodeId}.${key}` : undefined);
      store().setConfig(nodeId, key, value);
    },
    patch(patch: JsonObject, remove: readonly string[] = [], typing?: string) {
      store().begin(typing ? `${nodeId}.${typing}` : undefined);
      store().patchConfig(nodeId, patch, remove);
    },
  };
}

/** Reads a number from an input: empty is no value, anything else must parse. */
export function readNumber(raw: string): number | undefined | null {
  const v = raw.trim();
  if (!v) return undefined;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
}
