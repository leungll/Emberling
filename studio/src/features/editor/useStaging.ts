import { useCallback, useMemo, useState } from 'react';

import { uploadAsset } from '@/api/client';
import { copy } from '@/lib/copy';
import { readString, writeString } from '@/lib/storage';
import { showToast } from '@/lib/toast';
import { useEditorStore } from './editor-store';
import { MB, formatNames } from './model/fields';
import { stagedKey, type Staged } from './model/images';

function readStaged(key: string): Staged {
  const raw = readString('session', key);
  if (!raw) return {};
  try {
    const v = JSON.parse(raw) as unknown;
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Staged) : {};
  } catch {
    return {};
  }
}

/**
 * Images staged on input steps for the next run started from this page. They are uploaded
 * at once, kept for this browser tab only, and never saved with the workflow.
 */
export function useStaging(workflowId: string | null) {
  const key = stagedKey(workflowId ?? 'new');
  const [state, setState] = useState<{ key: string; staged: Staged }>(() => ({
    key,
    staged: readStaged(key),
  }));
  const staged = state.key === key ? state.staged : readStaged(key);
  if (state.key !== key) setState({ key, staged });

  const write = useCallback(
    (fn: (s: Staged) => Staged) => {
      setState((cur) => {
        const next = fn(cur.key === key ? cur.staged : readStaged(key));
        writeString('session', key, Object.keys(next).length ? JSON.stringify(next) : null);
        return { key, staged: next };
      });
    },
    [key],
  );

  const stageFiles = useCallback(
    (nodeId: string, files: File[], replace: boolean) => {
      const t = copy.editor.toasts;
      const node = useEditorStore.getState().doc.nodes.find((n) => n.id === nodeId);
      if (!node) return;
      const images = files.filter((f) => f.type.startsWith('image/'));
      if (!images.length) {
        showToast(t.chooseImage);
        return;
      }
      const accepted = Array.isArray(node.config.acceptedMediaTypes)
        ? node.config.acceptedMediaTypes
        : [];
      const maxBytes = typeof node.config.maxSizeBytes === 'number' ? node.config.maxSizeBytes : 0;
      const list = replace ? images.slice(0, 1) : images;
      if (accepted.length && list.some((f) => !accepted.includes(f.type))) {
        showToast(t.acceptsOnly(node.name, formatNames(accepted).join(', ')));
        return;
      }
      if (maxBytes && list.some((f) => f.size > maxBytes)) {
        showToast(t.tooLarge(`${String(+(maxBytes / MB).toFixed(2))} MB`));
        return;
      }
      void Promise.all(list.map((f) => uploadAsset(f).then((ref) => ({ ref, name: f.name })))).then(
        (items) => {
          write((s) => ({ ...s, [nodeId]: replace ? items : [...(s[nodeId] ?? []), ...items] }));
          showToast(t.imageReady(items.length, node.name), { done: true });
        },
        () => showToast(t.uploadFailed),
      );
    },
    [write],
  );

  const unstage = useCallback(
    (nodeId: string) => {
      write((s) => Object.fromEntries(Object.entries(s).filter(([id]) => id !== nodeId)));
      showToast(copy.editor.toasts.removedFromRun);
    },
    [write],
  );

  /** Drops what was staged on steps that no longer exist. */
  const forget = useCallback(
    (ids: readonly string[]) => {
      write((s) => Object.fromEntries(Object.entries(s).filter(([id]) => !ids.includes(id))));
    },
    [write],
  );

  const pickFiles = useCallback(
    (nodeId: string, replace: boolean) => {
      const input = document.createElement('input');
      input.type = 'file';
      input.accept = 'image/*';
      input.multiple = !replace;
      input.addEventListener('change', () => {
        stageFiles(nodeId, Array.from(input.files ?? []), replace);
      });
      input.click();
    },
    [stageFiles],
  );

  return useMemo(
    () => ({ staged, stageFiles, unstage, forget, pickFiles }),
    [staged, stageFiles, unstage, forget, pickFiles],
  );
}
