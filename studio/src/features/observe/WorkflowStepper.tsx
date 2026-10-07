import { StatusGlyph } from './StatusGlyph';
import { topologicalNodeOrder } from './topology';
import type { Definition, NodeRun, RunEvent } from '@/api/types';
import { formatClock, formatMillis } from '@/lib/format';
import { nodeRunStatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

/** Registered Node Type of a MANAGED_AGENT NodeRun, shown as the Agent card. */
const AGENT_NODE_TYPE = 'agent';

/** Helper-line note for one NodeRun, read from its own server timestamps and error. */
function statusNote(nodeRun: NodeRun): string {
  switch (nodeRun.status) {
    case 'SUCCEEDED':
      return nodeRun.latencyMs !== null
        ? formatMillis(nodeRun.latencyMs)
        : `completed ${formatClock(nodeRun.completedAt)}`;
    case 'RUNNING':
      return `started ${formatClock(nodeRun.startedAt)}`;
    case 'WAITING_CALLBACK':
      return `since ${formatClock(nodeRun.waitingAt)}`;
    case 'READY':
      return `ready ${formatClock(nodeRun.readyAt)}`;
    case 'FAILED':
      return nodeRun.error?.code ?? 'failed';
  }
}

/** `turnNo` of the newest committed Turn Event of one Agent NodeRun, if any. */
function latestTurnNo(events: readonly RunEvent[], nodeRunId: string): number | null {
  let latest: RunEvent | null = null;
  for (const event of events) {
    if (event.nodeRunId !== nodeRunId) continue;
    if (event.type !== 'AGENT_TURN_READY' && event.type !== 'AGENT_TURN_STARTED') continue;
    if (typeof event.payload.turnNo !== 'number') continue;
    if (!latest || event.seq > latest.seq) latest = event;
  }
  return latest ? (latest.payload.turnNo as number) : null;
}

interface WorkflowStepperProps {
  /** The Run's own bound Definition version. */
  definition: Definition;
  nodeRuns: NodeRun[];
  /** Committed Events, read only for the Agent card's current Turn number. */
  events: readonly RunEvent[];
  selectedNodeRunId: string | null;
  onSelect: (nodeRunId: string) => void;
}

/**
 * Vertical WORKFLOW · READ ONLY stepper of the Agent view mock. Each step is one
 * node of the bound Definition with the status of its own NodeRun as the Backend reported
 * it; a node without a NodeRun yet is numbered and neutral. Nothing here aggregates a Run
 * status. Selecting a step selects its NodeRun for the Detail column.
 */
export function WorkflowStepper({
  definition,
  nodeRuns,
  events,
  selectedNodeRunId,
  onSelect,
}: WorkflowStepperProps) {
  const nodeRunByNodeId = new Map<string, NodeRun>();
  for (const nodeRun of nodeRuns) nodeRunByNodeId.set(nodeRun.nodeId, nodeRun);
  const steps = topologicalNodeOrder(definition);

  return (
    <ol data-testid="workflow-stepper" className="relative space-y-3">
      {/* The connecting rail behind the glyphs. */}
      <span
        aria-hidden="true"
        className="absolute top-5 bottom-5 left-[19px] w-px bg-[var(--border)]"
      />
      {steps.map((node, index) => {
        const nodeRun = nodeRunByNodeId.get(node.id);
        const status = nodeRun?.status;
        const numbered = status === undefined || status === 'READY';
        const tone = status ? nodeRunStatusTone(status) : 'neutral';
        const agent = node.type === AGENT_NODE_TYPE;
        const selected = nodeRun?.id === selectedNodeRunId;
        const turnNo = agent && nodeRun ? latestTurnNo(events, nodeRun.id) : null;
        const helper = nodeRun
          ? agent
            ? `${nodeRun.status}${turnNo !== null ? ` · Turn ${turnNo}` : ''}`
            : `${nodeRun.status} · ${statusNote(nodeRun)}`
          : 'Not started · no NodeRun yet';

        return (
          <li
            key={node.id}
            data-testid={`stepper-item-${node.id}`}
            data-status={status ?? 'NOT_STARTED'}
            className="relative flex items-center gap-4"
          >
            {numbered ? (
              <span
                aria-hidden="true"
                data-testid="stepper-glyph"
                data-tone="neutral"
                className="relative inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-full border-2 border-[var(--status-neutral-dot)] bg-[var(--background)] text-[14px] font-semibold text-[var(--status-neutral-fg)] tabular-nums"
              >
                {index + 1}
              </span>
            ) : (
              <span data-testid="stepper-glyph" data-tone={tone} className="relative">
                <StatusGlyph tone={tone} className="h-10 w-10" />
              </span>
            )}
            <button
              type="button"
              disabled={!nodeRun}
              aria-pressed={selected}
              onClick={() => nodeRun && onSelect(nodeRun.id)}
              className={cn(
                'min-w-0 flex-1 rounded-lg border px-4 py-3 text-left enabled:hover:bg-[var(--accent)]',
                agent
                  ? cn(
                      'border-2 bg-[var(--node-selected-agent-fill)]',
                      selected ? 'border-[#a48afb]' : 'border-[var(--primary)]/50',
                    )
                  : selected
                    ? 'border-[var(--muted-foreground)] bg-[var(--accent)]'
                    : 'border-transparent',
              )}
            >
              {agent ? (
                <span className="block text-xs font-semibold tracking-[0.06em] uppercase">
                  Agent NodeRun
                </span>
              ) : null}
              <span className="block truncate text-[15px] font-semibold">{node.name}</span>
              <span
                className={cn(
                  'mt-0.5 block truncate text-[13px]',
                  agent ? 'text-[var(--foreground)]' : 'text-[var(--muted-foreground)]',
                )}
              >
                {helper}
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}
