import { useEffect, useMemo, useRef, useState } from 'react';

import { buildRunModel, type RunModel } from './run-model';
import { observeRun, type ObservedRun } from './runObserver';
import {
  getAgentTrace,
  getDefinitionVersion,
  getNodeRunDetail,
  listModels,
  listNodeTypes,
  listTools,
} from '@/api/client';
import type {
  AgentTrace,
  Definition,
  ModelMetadata,
  NodeMetadata,
  NodeRunDetail,
  ToolMetadata,
} from '@/api/types';

export interface Registry {
  nodeTypes: NodeMetadata[];
  tools: ToolMetadata[];
  models: ModelMetadata[];
}

const EMPTY_REGISTRY: Registry = { nodeTypes: [], tools: [], models: [] };

/**
 * The registered Node Types, Tools and Models, for names and tool facts. Without them the
 * run still shows: types fall back to their keys and tool facts are left out.
 */
export function useRegistry(): Registry {
  const [registry, setRegistry] = useState<Registry>(EMPTY_REGISTRY);
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    void Promise.allSettled([listNodeTypes(signal), listTools(signal), listModels(signal)]).then(
      ([nodeTypes, tools, models]) => {
        if (signal.aborted) return;
        setRegistry({
          nodeTypes: nodeTypes.status === 'fulfilled' ? nodeTypes.value : [],
          tools: tools.status === 'fulfilled' ? tools.value : [],
          models: models.status === 'fulfilled' ? models.value : [],
        });
      },
    );
    return () => {
      controller.abort();
    };
  }, []);
  return registry;
}

const INITIAL: ObservedRun = { snapshot: null, events: [], loadError: null, connection: 'live' };

interface Records {
  runId: string;
  details: Record<string, NodeRunDetail>;
  traces: Record<string, AgentTrace>;
  /** Each NodeRun's read signature; a NodeRun is read again when its signature changes. */
  read: Record<string, string>;
}

export interface RunData {
  observed: ObservedRun;
  definition: Definition | null;
  definitionFailed: boolean;
  model: RunModel | null;
  /** True once the run, its version and every step's records have been read once. */
  ready: boolean;
  reconnect: () => void;
}

/**
 * One run as the run view shows it: the Snapshot and Events from the run observer, the
 * Definition version the run is bound to, and each NodeRun's details and Agent Trace. A
 * NodeRun is read again whenever an Event about it arrives or its status changes, so the
 * trace follows a live run; the observer stays the only source of status.
 */
export function useRunData(
  runId: string,
  attempt: number,
  nodeTypes: readonly NodeMetadata[],
): RunData {
  const [observed, setObserved] = useState<{ runId: string; state: ObservedRun }>({
    runId,
    state: INITIAL,
  });
  const observer = useRef<{ close(): void; reconnect(): void } | null>(null);

  useEffect(() => {
    // No run chosen yet: nothing to read.
    if (!runId) return;
    const o = observeRun(runId, (state) => {
      setObserved({ runId, state });
    });
    observer.current = o;
    return () => {
      o.close();
      observer.current = null;
    };
  }, [runId, attempt]);

  const current = observed.runId === runId ? observed.state : INITIAL;
  const snapshot = current.snapshot;

  // The bound version is immutable, so it is read once per run.
  const workflowId = snapshot?.run.workflowId ?? null;
  const version = snapshot?.run.definitionVersion ?? null;
  const versionKey = workflowId && version !== null ? `${workflowId}@${String(version)}` : null;
  const [definition, setDefinition] = useState<{
    key: string;
    value: Definition | null;
    failed: boolean;
  } | null>(null);
  useEffect(() => {
    if (!versionKey || !workflowId || version === null) return;
    const controller = new AbortController();
    getDefinitionVersion(workflowId, version, controller.signal)
      .then((value) => {
        setDefinition({ key: versionKey, value, failed: false });
      })
      .catch(() => {
        if (!controller.signal.aborted)
          setDefinition({ key: versionKey, value: null, failed: true });
      });
    return () => {
      controller.abort();
    };
  }, [versionKey, workflowId, version]);

  // A NodeRun's signature: its status and how many Events name it.
  const signature = useMemo(() => {
    if (!snapshot) return '';
    const counts = new Map<string, number>();
    for (const e of current.events) {
      if (e.nodeRunId) counts.set(e.nodeRunId, (counts.get(e.nodeRunId) ?? 0) + 1);
    }
    return snapshot.nodeRuns
      .map((n) => `${n.id}|${n.nodeType}|${n.status}|${String(counts.get(n.id) ?? 0)}`)
      .join(',');
  }, [snapshot, current.events]);

  const [records, setRecords] = useState<Records>({ runId, details: {}, traces: {}, read: {} });
  const requested = useRef<{ runId: string; read: Record<string, string> }>({ runId, read: {} });
  // Reads belong to the run, not to one signature: a newer signature for one NodeRun must
  // not cut off another NodeRun's read in flight. They stop when the run changes.
  const reads = useRef<{ runId: string; controller: AbortController } | null>(null);
  useEffect(
    () => () => {
      reads.current?.controller.abort();
      reads.current = null;
      // The cut-off reads are made again when the run is shown again.
      requested.current = { runId: '', read: {} };
    },
    [runId],
  );

  useEffect(() => {
    if (!signature) return;
    if (requested.current.runId !== runId) requested.current = { runId, read: {} };
    if (reads.current?.runId !== runId) {
      reads.current = { runId, controller: new AbortController() };
    }
    const { signal } = reads.current.controller;
    for (const part of signature.split(',')) {
      const [id = '', nodeType = '', status = '', count = ''] = part.split('|');
      const sig = `${status}|${count}`;
      if (requested.current.read[id] === sig) continue;
      requested.current.read[id] = sig;
      const agent = nodeType === 'agent';
      void Promise.allSettled([
        getNodeRunDetail(runId, id, signal),
        agent ? getAgentTrace(runId, id, signal) : Promise.resolve(null),
      ]).then(([detail, trace]) => {
        // A read for another run, or one a newer read of this NodeRun replaces.
        if (signal.aborted || requested.current.read[id] !== sig) return;
        setRecords((r) => {
          const base = r.runId === runId ? r : { runId, details: {}, traces: {}, read: {} };
          return {
            runId,
            details:
              detail.status === 'fulfilled'
                ? { ...base.details, [id]: detail.value }
                : base.details,
            traces:
              trace.status === 'fulfilled' && trace.value
                ? { ...base.traces, [id]: trace.value }
                : base.traces,
            read: { ...base.read, [id]: sig },
          };
        });
      });
    }
  }, [signature, runId]);

  const own = records.runId === runId ? records : null;
  const def = definition?.key === versionKey ? definition : null;
  const model = useMemo(
    () =>
      snapshot && def?.value
        ? buildRunModel({
            snapshot,
            events: current.events,
            definition: def.value,
            details: own?.details ?? {},
            traces: own?.traces ?? {},
            nodeTypes,
          })
        : null,
    [snapshot, current.events, def, own, nodeTypes],
  );

  const ready =
    model !== null && snapshot?.nodeRuns.every((n) => own?.read[n.id] !== undefined) === true;

  return {
    observed: current,
    definition: def?.value ?? null,
    definitionFailed: def?.failed ?? false,
    model,
    ready,
    reconnect: () => {
      observer.current?.reconnect();
    },
  };
}
