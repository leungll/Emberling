/**
 * Named handles on the recorded fixtures, so specs say "a failed run" rather than an ID.
 * Each handle is the first recorded workflow that fits; the recorder keeps them in depth.
 */
import { fixture } from './server';

interface DefinitionItem {
  workflowId: string;
  name: string;
  lastRun: { id: string; status: string } | null;
}

const definitions = (fixture('/api/definitions') as { items: DefinitionItem[] }).items;

function recordedInDepth(item: DefinitionItem): boolean {
  try {
    fixture(`/api/definitions/${item.workflowId}`);
    return item.lastRun !== null && fixture(`/api/runs/${item.lastRun.id}`) !== null;
  } catch {
    return false;
  }
}

function pick(status: string, name?: RegExp): { workflowId: string; runId: string; name: string } {
  const item = definitions.find(
    (d) => d.lastRun?.status === status && (!name || name.test(d.name)) && recordedInDepth(d),
  );
  if (!item?.lastRun) throw new Error(`no recorded workflow whose last run is ${status}`);
  return { workflowId: item.workflowId, runId: item.lastRun.id, name: item.name };
}

export const data = {
  completed: pick('COMPLETED', /Software Delivery/),
  failed: pick('FAILED'),
  photos: pick('COMPLETED', /Photo/),
  /** A well-formed ID that no fixture answers, for not-found states. */
  missingWorkflowId: 'wf_00000000000000000000000000000000',
  missingRunId: 'run_00000000000000000000000000000000',
};
