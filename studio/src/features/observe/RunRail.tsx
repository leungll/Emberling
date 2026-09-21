import type { NodeRun, RunSnapshot } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import { formatDuration, formatTimestamp } from '@/lib/format';
import { nodeRunStatusLabel, nodeRunStatusVariant } from '@/lib/status';
import { cn } from '@/lib/utils';

interface RunRailProps {
  snapshot: RunSnapshot;
  selectedNodeRunId: string | null;
  onSelect: (nodeRunId: string) => void;
}

/**
 * Run identity plus the Active NodeRuns, grouped as Running and Waiting. The grouping is
 * a display filter over server statuses, not an aggregation: it never produces a Run
 * status of its own.
 */
export function RunRail({ snapshot, selectedNodeRunId, onSelect }: RunRailProps) {
  const running = snapshot.nodeRuns.filter((n) => n.status === 'RUNNING' || n.status === 'READY');
  const waiting = snapshot.nodeRuns.filter((n) => n.status === 'WAITING_CALLBACK');

  return (
    <aside className="flex w-72 shrink-0 flex-col border-r border-[var(--border)]">
      <div className="space-y-1 border-b border-[var(--border)] p-3 text-xs">
        <Row label="Definition" value={`v${snapshot.run.definitionVersion}`} />
        <Row label="Started" value={formatTimestamp(snapshot.run.startedAt)} />
        <Row
          label="Duration"
          value={formatDuration(snapshot.run.startedAt, snapshot.run.completedAt)}
        />
        <Row label="Last seq" value={String(snapshot.lastSeq)} />
      </div>

      <ScrollArea className="flex-1 p-3">
        <Group
          title="Running"
          nodeRuns={running}
          selectedNodeRunId={selectedNodeRunId}
          onSelect={onSelect}
        />
        <Group
          title="Waiting"
          nodeRuns={waiting}
          selectedNodeRunId={selectedNodeRunId}
          onSelect={onSelect}
        />
      </ScrollArea>
    </aside>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-2">
      <span className="text-[var(--muted-foreground)]">{label}</span>
      <span className="tabular-nums">{value}</span>
    </div>
  );
}

function Group({
  title,
  nodeRuns,
  selectedNodeRunId,
  onSelect,
}: {
  title: string;
  nodeRuns: NodeRun[];
  selectedNodeRunId: string | null;
  onSelect: (nodeRunId: string) => void;
}) {
  return (
    <section className="mb-4">
      <h3 className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
        {title}
      </h3>
      {nodeRuns.length === 0 ? (
        <p className="text-[10px] text-[var(--muted-foreground)]">None</p>
      ) : (
        <ul className="space-y-1">
          {nodeRuns.map((nodeRun) => (
            <li key={nodeRun.id}>
              <button
                type="button"
                onClick={() => onSelect(nodeRun.id)}
                className={cn(
                  'w-full rounded border border-[var(--border)] px-2 py-1.5 text-left text-xs hover:bg-[var(--accent)]',
                  nodeRun.id === selectedNodeRunId && 'ring-1 ring-[var(--ring)]',
                )}
              >
                <span className="block font-medium">{nodeRun.nodeId}</span>
                <Badge variant={nodeRunStatusVariant(nodeRun.status)} className="mt-1">
                  {nodeRunStatusLabel(nodeRun.status)}
                </Badge>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
