import { StatusGlyph } from './StatusGlyph';
import { TONE_TEXT } from './toneClasses';
import type { NodeRun, RunSnapshot } from '@/api/types';
import { nodeRunStatusLabel, nodeRunStatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

interface RunRailProps {
  snapshot: RunSnapshot;
  selectedNodeRunId: string | null;
  onSelect: (nodeRunId: string) => void;
  /** Definition node id → display name from the bound version; ids show when absent. */
  nodeNames?: ReadonlyMap<string, string>;
  /** `row` puts the two groups side by side (Run Summary card); `column` stacks them. */
  layout?: 'row' | 'column';
}

/**
 * The Active NodeRuns of the Run Rail (04 §3), grouped as Running and Waiting. The grouping
 * is a display filter over server statuses, not an aggregation: it never produces a Run
 * status of its own.
 */
export function RunRail({
  snapshot,
  selectedNodeRunId,
  onSelect,
  nodeNames,
  layout = 'column',
}: RunRailProps) {
  const running = snapshot.nodeRuns.filter((n) => n.status === 'RUNNING' || n.status === 'READY');
  const waiting = snapshot.nodeRuns.filter((n) => n.status === 'WAITING_CALLBACK');

  return (
    <div
      data-testid="run-rail"
      className={cn('grid gap-4', layout === 'row' ? 'grid-cols-2' : 'grid-cols-1')}
    >
      <Group
        title="Running"
        nodeRuns={running}
        selectedNodeRunId={selectedNodeRunId}
        onSelect={onSelect}
        nodeNames={nodeNames}
      />
      <Group
        title="Waiting"
        nodeRuns={waiting}
        selectedNodeRunId={selectedNodeRunId}
        onSelect={onSelect}
        nodeNames={nodeNames}
      />
    </div>
  );
}

function Group({
  title,
  nodeRuns,
  selectedNodeRunId,
  onSelect,
  nodeNames,
}: {
  title: string;
  nodeRuns: NodeRun[];
  selectedNodeRunId: string | null;
  onSelect: (nodeRunId: string) => void;
  nodeNames?: ReadonlyMap<string, string>;
}) {
  return (
    <section className="min-w-0">
      <h3 className="mb-2 text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase">
        {title}
      </h3>
      {nodeRuns.length === 0 ? (
        <p className="text-sm text-[var(--muted-foreground)]">None</p>
      ) : (
        <ul className="space-y-2">
          {nodeRuns.map((nodeRun) => {
            const name = nodeNames?.get(nodeRun.nodeId);
            const tone = nodeRunStatusTone(nodeRun.status);
            return (
              <li key={nodeRun.id}>
                <button
                  type="button"
                  onClick={() => onSelect(nodeRun.id)}
                  className={cn(
                    'flex w-full items-center gap-3 rounded-lg border border-[var(--border)] bg-[var(--muted)] px-3 py-2 text-left hover:bg-[var(--accent)]',
                    nodeRun.id === selectedNodeRunId &&
                      'border-[var(--primary)] bg-[var(--node-selected-agent-fill)]',
                  )}
                >
                  <StatusGlyph tone={tone} />
                  <span className="min-w-0 flex-1">
                    {name ? (
                      <span className="block truncate text-[14px] font-semibold">{name}</span>
                    ) : null}
                    <span
                      className={cn(
                        'block truncate',
                        name
                          ? 'text-xs text-[var(--muted-foreground)]'
                          : 'text-[14px] font-semibold',
                      )}
                    >
                      {nodeRun.nodeId}
                    </span>
                  </span>
                  <span className={cn('shrink-0 text-xs font-semibold', TONE_TEXT[tone])}>
                    {nodeRunStatusLabel(nodeRun.status)}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
