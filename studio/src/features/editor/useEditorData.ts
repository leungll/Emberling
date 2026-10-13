import { useCallback, useEffect, useMemo, useState } from 'react';

import {
  ApiRequestError,
  getDefinition,
  getRun,
  listDefinitions,
  listModels,
  listNodeTypes,
  listTools,
} from '@/api/client';
import type {
  Definition,
  DefinitionLastRun,
  ModelMetadata,
  NodeMetadata,
  NodeRun,
  RunSnapshot,
  ToolMetadata,
} from '@/api/types';
import { noteRecent } from '@/lib/nav/recent';
import { catalogOf, type Catalog } from '@/features/editor/model/steps';

/** The registered step types, models and tools the Editor reads. */
export interface Registry {
  nodeTypes: NodeMetadata[];
  catalog: Catalog;
  models: ReadonlyMap<string, ModelMetadata>;
  tools: ReadonlyMap<string, ToolMetadata>;
  toolList: ToolMetadata[];
  loaded: boolean;
  failed: boolean;
}

export interface LastRunInfo {
  summary: DefinitionLastRun | null;
  snapshot: RunSnapshot | null;
  byNode: ReadonlyMap<string, NodeRun>;
  loaded: boolean;
}

export type LoadState =
  | { kind: 'loading' }
  | { kind: 'new' }
  | { kind: 'ready'; definition: Definition }
  | { kind: 'missing' }
  | { kind: 'error'; message: string };

const NO_RUN: LastRunInfo = { summary: null, snapshot: null, byNode: new Map(), loaded: false };
const NEW_RUN: LastRunInfo = { ...NO_RUN, loaded: true };

/**
 * Loads what the Editor shows: the registry, the workflow's latest version and its last run.
 * A failure to load the last run only empties the strip; it never blocks editing.
 */
export function useEditorData(workflowId: string | null, reloadKey: number) {
  const [nodeTypes, setNodeTypes] = useState<NodeMetadata[] | null>(null);
  const [models, setModels] = useState<ModelMetadata[] | null>(null);
  const [tools, setTools] = useState<ToolMetadata[] | null>(null);
  const [catalogFailed, setCatalogFailed] = useState(false);
  // Both results remember the workflow they belong to, so a stale one never shows for another.
  const [loaded, setLoaded] = useState<{ id: string; state: LoadState } | null>(null);
  const [run, setRun] = useState<{ id: string; info: LastRunInfo } | null>(null);

  useEffect(() => {
    const ctl = new AbortController();
    const fail = () => {
      if (!ctl.signal.aborted) setCatalogFailed(true);
    };
    listNodeTypes(ctl.signal).then(setNodeTypes, fail);
    listModels(ctl.signal).then(setModels, fail);
    listTools(ctl.signal).then(setTools, fail);
    return () => ctl.abort();
  }, []);

  useEffect(() => {
    if (!workflowId) return;
    const ctl = new AbortController();
    const setLoad = (state: LoadState) => setLoaded({ id: workflowId, state });
    const setLastRun = (info: LastRunInfo) => setRun({ id: workflowId, info });
    getDefinition(workflowId, ctl.signal).then(
      (definition) => {
        noteRecent(definition.workflowId);
        setLoad({ kind: 'ready', definition });
      },
      (error: unknown) => {
        if (ctl.signal.aborted) return;
        if (error instanceof ApiRequestError && error.status === 404) setLoad({ kind: 'missing' });
        else
          setLoad({
            kind: 'error',
            message: error instanceof Error ? error.message : String(error),
          });
      },
    );
    listDefinitions(ctl.signal)
      .then(async (items) => {
        const summary = items.find((d) => d.workflowId === workflowId)?.lastRun ?? null;
        if (!summary) {
          setLastRun({ ...NO_RUN, loaded: true });
          return;
        }
        const snapshot = await getRun(summary.id, ctl.signal);
        setLastRun({
          summary,
          snapshot,
          byNode: new Map(snapshot.nodeRuns.map((nr) => [nr.nodeId, nr])),
          loaded: true,
        });
      })
      .catch(() => {
        if (!ctl.signal.aborted) setLastRun({ ...NO_RUN, loaded: true });
      });
    return () => ctl.abort();
  }, [workflowId, reloadKey]);

  const registry = useMemo<Registry>(
    () => ({
      nodeTypes: nodeTypes ?? [],
      catalog: catalogOf(nodeTypes ?? []),
      models: new Map((models ?? []).map((m) => [m.id, m])),
      tools: new Map((tools ?? []).map((t) => [t.name, t])),
      toolList: tools ?? [],
      loaded: nodeTypes !== null && models !== null && tools !== null,
      failed: catalogFailed,
    }),
    [nodeTypes, models, tools, catalogFailed],
  );

  const load: LoadState = !workflowId
    ? { kind: 'new' }
    : loaded?.id === workflowId
      ? loaded.state
      : { kind: 'loading' };
  const lastRun: LastRunInfo = !workflowId ? NEW_RUN : run?.id === workflowId ? run.info : NO_RUN;
  /** Shows a definition this page just saved without waiting for a reload. */
  const setLoad = useCallback((id: string, state: LoadState) => setLoaded({ id, state }), []);

  return { registry, load, setLoad, lastRun };
}
