import { Handle, Position, type Node, type NodeProps } from '@xyflow/react';
import { Fragment } from 'react';

import { TONE_CARD, TONE_TEXT } from './toneClasses';
import {
  COMPACT_NODE_HEIGHT,
  COMPACT_NODE_WIDTH,
  compactHandleId,
  type CompactSide,
} from './topology';
import type { NodeRunStatus } from '@/api/types';
import { nodeRunBorderClass, nodeRunStatusLabel, nodeRunStatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

export interface CompactStatusNodeData extends Record<string, unknown> {
  label: string;
  /** Server-reported NodeRun status. Undefined means no NodeRun exists yet (idle). */
  nodeRunStatus?: NodeRunStatus;
}

export type CompactStatusFlowNode = Node<CompactStatusNodeData, 'compactStatus'>;

const SIDES: { side: CompactSide; position: Position }[] = [
  { side: 'top', position: Position.Top },
  { side: 'right', position: Position.Right },
  { side: 'bottom', position: Position.Bottom },
  { side: 'left', position: Position.Left },
];

/**
 * One-word NodeRun status for the compact box, which has no room for the longer shared
 * wording ("Waiting for callback"); that wording stays on the status as its tooltip. A
 * display lookup over the server's status, never a derived state.
 */
const SHORT_STATUS: Record<NodeRunStatus, string> = {
  READY: 'Ready',
  RUNNING: 'Running',
  WAITING_CALLBACK: 'Waiting',
  SUCCEEDED: 'Succeeded',
  FAILED: 'Failed',
};

/** Invisible anchor: the compact topology draws edges but offers no port to connect. */
const HIDDEN_HANDLE = { opacity: 0, width: 1, height: 1, minWidth: 0, minHeight: 0, border: 0 };

/**
 * The READ-ONLY TOPOLOGY card's node (as in the Run view mock): node name and NodeRun status only,
 * on the status-coloured border. Everything shown is the Definition node's own name and
 * the status the Backend reported for its NodeRun; a node without one reads "idle".
 * Ports, port hints and config summaries belong to the Edit canvas card (RegisteredNode).
 * Status colours are never overridden: selection adds the status tint and an
 * outer ring on top of the same status border.
 */
export function CompactStatusNode({ data, selected }: NodeProps<CompactStatusFlowNode>) {
  const status = data.nodeRunStatus;
  const tone = status ? nodeRunStatusTone(status) : 'neutral';

  return (
    <div
      title={data.label}
      data-selected={selected ? 'true' : 'false'}
      style={{ width: COMPACT_NODE_WIDTH, height: COMPACT_NODE_HEIGHT }}
      className={cn(
        'relative flex flex-col items-center justify-center rounded-lg border-2 bg-[var(--card)] px-2 text-center text-[var(--card-foreground)]',
        status ? nodeRunBorderClass(status) : 'border-[var(--border)]',
        selected && 'ring-2 ring-[var(--foreground)] ring-offset-2 ring-offset-[var(--card)]',
      )}
    >
      {selected ? (
        // The status tint is translucent, so it sits on the opaque card fill: an edge
        // passing behind the node must never show through it.
        <span
          aria-hidden="true"
          data-testid="compact-node-tint"
          className={cn(
            'pointer-events-none absolute inset-0 rounded-md',
            status ? TONE_CARD[tone] : 'bg-[var(--accent)]',
          )}
        />
      ) : null}
      <span className="relative line-clamp-2 w-full text-[13px] leading-4 font-semibold break-words">
        {data.label}
      </span>
      <span
        data-testid="compact-node-status"
        className={cn(
          'relative mt-0.5 text-[12px] leading-4 font-semibold',
          status ? TONE_TEXT[tone] : 'text-[var(--muted-foreground)]',
        )}
      >
        {status ? (
          <>
            {/* The one-word status is visual; assistive tech reads the full status label. */}
            <span aria-hidden="true">{SHORT_STATUS[status] ?? status}</span>
            <span className="sr-only">{nodeRunStatusLabel(status)}</span>
          </>
        ) : (
          'idle'
        )}
      </span>
      {SIDES.map(({ side, position }) => (
        <Fragment key={side}>
          <Handle
            id={compactHandleId('source', side)}
            type="source"
            position={position}
            isConnectable={false}
            style={HIDDEN_HANDLE}
          />
          <Handle
            id={compactHandleId('target', side)}
            type="target"
            position={position}
            isConnectable={false}
            style={HIDDEN_HANDLE}
          />
        </Fragment>
      ))}
    </div>
  );
}
