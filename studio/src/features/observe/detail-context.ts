import type { Failure } from './run-diagnosis';
import type { RunModel, WaitInfo } from './run-model';
import type { ModelMetadata, NodeMetadata, ToolMetadata } from '@/api/types';

/** What every part of the details pane reads: the run, its diagnosis and the registry. */
export interface DetailContext {
  model: RunModel;
  failure: Failure | null;
  wait: WaitInfo | null;
  tools: readonly ToolMetadata[];
  models: readonly ModelMetadata[];
  nodeTypes: readonly NodeMetadata[];
  workflowId: string;
  /** The workflow's latest version, or null while unknown. */
  latestVersion: number | null;
  selectedId: string;
  onSelect: (id: string, tab?: string) => void;
}

/** A model's display name, else its ID as given. */
export function modelName(ctx: DetailContext, id: unknown): string {
  const m = typeof id === 'string' ? ctx.models.find((x) => x.id === id) : undefined;
  return m ? m.displayName : typeof id === 'string' ? id : '';
}
