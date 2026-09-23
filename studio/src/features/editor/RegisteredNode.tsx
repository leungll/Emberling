import { Handle, Position as FlowPosition, type NodeProps, type Node } from '@xyflow/react';

import { nodeSummaryLine } from './nodeSummary';
import { nodeAccentColor, portHandleStyle } from './portStyles';
import type { JsonObject, ModelMetadata, NodeMetadata, NodeRunStatus } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { nodeRunBorderClass, nodeRunStatusLabel, nodeRunStatusVariant } from '@/lib/status';

export interface RegisteredNodeData extends Record<string, unknown> {
  label: string;
  metadata: NodeMetadata | undefined;
  /** The Definition node's own config. Undefined for a caller that has none to offer. */
  config?: JsonObject;
  /** Registered Models, used to resolve a `modelId` config value to its display name. */
  models?: ModelMetadata[];
  /** Server-reported NodeRun status in observe mode. Undefined means no NodeRun exists. */
  nodeRunStatus?: NodeRunStatus;
}

export type RegisteredFlowNode = Node<RegisteredNodeData, 'registered'>;

/**
 * Every card's fixed footprint (04 §2.5). Declaring it on each `RegisteredFlowNode`'s
 * `width`/`height` (see `EditPage`/`ObserveCanvas`) tells React Flow the node's size up
 * front, so it skips its measure-then-reveal pass — without that, controlled `nodes` (this
 * app never hands ownership of node state to React Flow) never receive the internal
 * dimension change back, and every card stays permanently `visibility: hidden`.
 */
// 176px keeps a 24px gutter between cards laid out on a 200px column grid (the spacing the
// acceptance fixtures use), so neighbouring cards never overlap.
export const NODE_CARD_WIDTH = 176;
export const NODE_CARD_HEIGHT = 128;

/**
 * First port row's centre and the row pitch (04 §2.5). Port rows sit below the two-line
 * summary, and their labels are drawn inside the card so they never collide with the
 * neighbouring card's labels across the gutter.
 */
const PORT_ROW_START = 100;
const PORT_ROW_HEIGHT = 16;

/**
 * Canvas node. Ports come from the registered NodeMetadata; status, when shown, is the
 * NodeRun status returned by the Backend. The summary line is derived from NodeMetadata
 * and the node's own config only — nothing here is computed or guessed locally.
 *
 * Every card shares one fixed width and stable height so the graph reads consistently
 * regardless of node type (04 §2.5): long names and summaries truncate with an ellipsis
 * rather than resizing the card.
 */
export function RegisteredNode({ data, selected, isConnectable }: NodeProps<RegisteredFlowNode>) {
  const metadata = data.metadata;
  const inputs = metadata?.inputs ?? [];
  const outputs = metadata?.outputs ?? [];
  const accent = nodeAccentColor(metadata);
  const isAgent = metadata?.category === 'Agent';
  // Selected state follows 04 §2.5's two literal treatments rather than the node's own
  // accent: every non-Agent category gets the amber border on the warm tint, Agent gets
  // the purple border on its own tint. That treatment applies only to a card without a
  // NodeRun status: status colours are never overridden (04 §4), so a selected card that
  // carries a status keeps its status border and is marked by an outer neutral ring.
  const hasStatus = data.nodeRunStatus !== undefined;
  const editSelected = selected && !hasStatus;
  const selectedFill = isAgent ? 'var(--node-selected-agent-fill)' : 'var(--node-selected-fill)';
  const selectedBorder = isAgent ? '#a48afb' : '#fdb022';

  return (
    <div
      title={data.label}
      data-selected={selected ? 'true' : 'false'}
      className={cn(
        'flex h-[128px] w-[176px] flex-col rounded-[9px] border-2 px-3 py-2.5 text-[var(--card-foreground)] shadow-sm',
        !editSelected && 'bg-[var(--card)]',
        hasStatus
          ? nodeRunBorderClass(data.nodeRunStatus)
          : !editSelected && 'border-[var(--border)]',
        selected &&
          hasStatus &&
          'ring-2 ring-[var(--foreground)] ring-offset-2 ring-offset-[var(--background)]',
      )}
      style={
        editSelected ? { borderColor: selectedBorder, backgroundColor: selectedFill } : undefined
      }
    >
      <div className="flex items-center gap-1.5 text-[12px] font-semibold tracking-[0.06em] text-[var(--muted-foreground)]">
        <span
          aria-hidden="true"
          className="h-2 w-2 shrink-0 rounded-full"
          style={{ backgroundColor: accent }}
        />
        <span className="truncate">
          {(metadata?.displayName ?? 'Unregistered type').toUpperCase()}
        </span>
      </div>
      <div className="mt-1 truncate text-[15px] leading-5 font-semibold">{data.label}</div>
      {/* Two lines while editing (model · spec · mode rarely fits one at this width); one
          in Observe, where the NodeRun status badge takes the second line. */}
      <div
        className={cn(
          'mt-0.5 text-xs leading-4 text-[var(--muted-foreground)]',
          data.nodeRunStatus ? 'truncate' : 'line-clamp-2',
        )}
      >
        {metadata
          ? nodeSummaryLine(metadata, data.config, data.models)
          : 'Type not found in the Registry'}
      </div>

      {data.nodeRunStatus ? (
        <Badge
          variant={nodeRunStatusVariant(data.nodeRunStatus)}
          className="mt-1 w-fit px-2 py-0.5 text-[12px]"
        >
          {nodeRunStatusLabel(data.nodeRunStatus)}
        </Badge>
      ) : null}

      {inputs.map((port, index) => (
        <Handle
          key={`in-${port.name}`}
          id={port.name}
          type="target"
          position={FlowPosition.Left}
          isConnectable={isConnectable}
          style={{
            ...portHandleStyle(port.dataType, port.required),
            top: PORT_ROW_START + index * PORT_ROW_HEIGHT,
          }}
          title={`${port.name}: ${port.dataType}${port.required ? ' (required)' : ' (optional)'}`}
          data-testid={`handle-in-${port.name}`}
          data-data-type={port.dataType}
          data-required={port.required}
        >
          <span
            aria-hidden="true"
            data-port-hint
            className="pointer-events-none absolute top-1/2 left-full ml-1.5 -translate-y-1/2 whitespace-nowrap text-[length:var(--port-hint-font,10px)] group-data-[port-hints=hidden]/canvas:hidden text-[var(--muted-foreground)]"
          >
            {port.name}
            {port.required ? '' : '?'}
          </span>
        </Handle>
      ))}

      {outputs.map((port, index) => (
        <Handle
          key={`out-${port.name}`}
          id={port.name}
          type="source"
          position={FlowPosition.Right}
          isConnectable={isConnectable}
          style={{
            ...portHandleStyle(port.dataType, port.required),
            top: PORT_ROW_START + index * PORT_ROW_HEIGHT,
          }}
          title={`${port.name}: ${port.dataType}${port.required ? ' (required)' : ' (optional)'}`}
          data-testid={`handle-out-${port.name}`}
          data-data-type={port.dataType}
          data-required={port.required}
        >
          <span
            aria-hidden="true"
            data-port-hint
            className="pointer-events-none absolute top-1/2 right-full mr-1.5 -translate-y-1/2 whitespace-nowrap text-[length:var(--port-hint-font,10px)] group-data-[port-hints=hidden]/canvas:hidden text-[var(--muted-foreground)]"
          >
            {port.name}
            {port.required ? '' : '?'}
          </span>
        </Handle>
      ))}
    </div>
  );
}
