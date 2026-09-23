import { Handle, Position as FlowPosition, type NodeProps, type Node } from '@xyflow/react';

import { portHandleStyle } from './portStyles';
import type { NodeMetadata, NodeRunStatus } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { nodeRunBorderClass, nodeRunStatusLabel, nodeRunStatusVariant } from '@/lib/status';

export interface RegisteredNodeData extends Record<string, unknown> {
  label: string;
  metadata: NodeMetadata | undefined;
  /** Server-reported NodeRun status in observe mode. Undefined means no NodeRun exists. */
  nodeRunStatus?: NodeRunStatus;
}

export type RegisteredFlowNode = Node<RegisteredNodeData, 'registered'>;

/**
 * Canvas node. Ports come from the registered NodeMetadata; status, when shown, is the
 * NodeRun status returned by the Backend. Nothing here is computed locally.
 */
export function RegisteredNode({ data, selected, isConnectable }: NodeProps<RegisteredFlowNode>) {
  const metadata = data.metadata;
  const inputs = metadata?.inputs ?? [];
  const outputs = metadata?.outputs ?? [];

  return (
    <div
      className={cn(
        'min-w-44 rounded-md border-2 bg-[var(--card)] px-3 py-2 text-[var(--card-foreground)] shadow-sm',
        nodeRunBorderClass(data.nodeRunStatus),
        selected && 'ring-2 ring-[var(--ring)]',
      )}
    >
      <div className="text-[10px] uppercase tracking-wide text-[var(--muted-foreground)]">
        {metadata?.displayName ?? 'Unregistered type'}
      </div>
      <div className="text-sm font-medium">{data.label}</div>

      {data.nodeRunStatus ? (
        <Badge variant={nodeRunStatusVariant(data.nodeRunStatus)} className="mt-1">
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
          style={{ ...portHandleStyle(port.dataType, port.required), top: 32 + index * 16 }}
          title={`${port.name}: ${port.dataType}${port.required ? ' (required)' : ' (optional)'}`}
          data-testid={`handle-in-${port.name}`}
          data-data-type={port.dataType}
          data-required={port.required}
        />
      ))}

      {outputs.map((port, index) => (
        <Handle
          key={`out-${port.name}`}
          id={port.name}
          type="source"
          position={FlowPosition.Right}
          isConnectable={isConnectable}
          style={{ ...portHandleStyle(port.dataType, port.required), top: 32 + index * 16 }}
          title={`${port.name}: ${port.dataType}${port.required ? ' (required)' : ' (optional)'}`}
          data-testid={`handle-out-${port.name}`}
          data-data-type={port.dataType}
          data-required={port.required}
        />
      ))}
    </div>
  );
}
