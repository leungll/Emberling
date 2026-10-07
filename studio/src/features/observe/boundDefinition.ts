import { useEffect, useState } from 'react';

import { ApiRequestError, getDefinitionVersion, listNodeTypes } from '@/api/client';
import type { Definition, NodeMetadata } from '@/api/types';

const EMPTY: BoundDefinition = { definition: null, nodeTypes: [], error: null };

export interface BoundDefinition {
  definition: Definition | null;
  nodeTypes: NodeMetadata[];
  error: string | null;
}

/**
 * Fetches the Run's own bound Definition version and the registered Node Types needed to
 * render it, exactly as the Editor does for its own Canvas. This is a read-only lookup by
 * immutable version: it never falls back to the Definition's latest version.
 */
export function useBoundDefinition(
  workflowId: string,
  definitionVersion: number,
  skip = false,
): BoundDefinition {
  const key = `${workflowId}@${definitionVersion}`;
  const [state, setState] = useState<BoundDefinition & { key: string }>({
    key,
    definition: null,
    nodeTypes: [],
    error: null,
  });
  // Another Run bound to a different Definition version must never show the previous
  // version's name, topology or allowedTools while its own read is in flight. React's
  // "adjust state during render" pattern drops the stale value before it is ever rendered.
  if (state.key !== key) {
    setState({ key, definition: null, nodeTypes: [], error: null });
  }

  useEffect(() => {
    if (skip) return;
    const controller = new AbortController();
    Promise.all([
      getDefinitionVersion(workflowId, definitionVersion, controller.signal),
      listNodeTypes(controller.signal),
    ])
      .then(([definition, nodeTypes]) => setState({ key, definition, nodeTypes, error: null }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({
          key,
          definition: null,
          nodeTypes: [],
          error:
            error instanceof ApiRequestError
              ? `${error.code}: ${error.message}`
              : 'Could not load the workflow topology',
        });
      });
    return () => controller.abort();
  }, [workflowId, definitionVersion, skip, key]);

  return state.key === key ? state : EMPTY;
}
