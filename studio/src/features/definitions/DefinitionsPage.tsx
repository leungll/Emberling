import { useEffect, useState } from 'react';

import { DefinitionsTable } from './DefinitionsTable';
import { ApiRequestError, listDefinitions } from '@/api/client';
import type { DefinitionListItem } from '@/api/types';

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; items: DefinitionListItem[] }
  | { kind: 'error'; code: string; message: string };

/** Definitions is the light management surface; the dark workspace starts in Studio. */
export function DefinitionsPage() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' });

  // The initial state is already `loading`, so the effect only needs to set state once the
  // request settles; it must not call setState synchronously in the effect body itself.
  useEffect(() => {
    const controller = new AbortController();
    listDefinitions(controller.signal)
      .then((items) => setState({ kind: 'loaded', items }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof ApiRequestError) {
          setState({ kind: 'error', code: error.code, message: error.message });
          return;
        }
        setState({
          kind: 'error',
          code: 'DEPENDENCY_UNAVAILABLE',
          message: error instanceof Error ? error.message : 'Failed to reach the Backend',
        });
      });
    return () => controller.abort();
  }, []);

  return (
    <main className="mx-auto max-w-6xl px-6 py-8">
      <header className="mb-6">
        <h1 className="text-xl font-semibold">Definitions</h1>
        <p className="mt-1 text-[var(--muted-foreground)]">
          Workflow Definitions registered with the Emberling Runtime.
        </p>
      </header>

      {state.kind === 'loading' ? (
        <p className="text-[var(--muted-foreground)]">Loading definitions…</p>
      ) : null}

      {state.kind === 'error' ? (
        <div
          role="alert"
          className="rounded-md border border-red-500/40 bg-red-500/5 px-4 py-3 text-red-600"
        >
          <p className="font-medium">Could not load definitions</p>
          <p className="mt-1 text-xs">
            <span data-testid="error-code">{state.code}</span> — {state.message}
          </p>
        </div>
      ) : null}

      {state.kind === 'loaded' ? <DefinitionsTable items={state.items} /> : null}
    </main>
  );
}
