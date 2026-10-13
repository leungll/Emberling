import { Handle, Position } from '@xyflow/react';
import { Plus } from 'lucide-react';
import type { CSSProperties } from 'react';

import type { PortDataType, PortMetadata } from '@/api/types';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { portLabel, typeWord } from '@/features/editor/model/steps';

/** Port colour encodes the data type only; status never reuses it. */
const PORT_COLOR: Record<PortDataType, string> = {
  text: 'var(--port-text)',
  image: 'var(--port-image)',
  json: 'var(--port-json)',
  any: 'var(--port-any)',
};

export type PortHint = 'ok' | 'busy' | 'bad' | null;

function dotStyle(
  type: PortDataType,
  filled: boolean,
  hint: PortHint,
  extra?: CSSProperties,
): CSSProperties {
  const c = PORT_COLOR[type] ?? PORT_COLOR.any;
  return {
    width: 12,
    height: 12,
    minWidth: 0,
    minHeight: 0,
    borderRadius: 9999,
    background: filled ? c : 'var(--surface)',
    border: '2px solid var(--surface)',
    outline:
      hint === 'ok'
        ? '2px solid var(--accent)'
        : hint === 'busy'
          ? '2px solid var(--border-control)'
          : `1px solid ${c}`,
    outlineOffset: hint === 'ok' || hint === 'busy' ? 2 : 0,
    opacity: hint === 'bad' ? 0.3 : 1,
    ...extra,
  };
}

interface InPortProps {
  port: PortMetadata;
  hint?: PortHint;
  /** Absolute place on a one-row or collapsed card; omitted on a full card's row. */
  top?: number;
  hidden?: boolean;
  label?: boolean;
}

/** An input dot. Links end here; dragging never starts from an input. */
export function InPort({ port, hint = null, top, hidden = false, label = false }: InPortProps) {
  const copy = useCopy();
  const t = copy.editor;
  const name = portLabel(port);
  return (
    <Tooltip
      content={
        <>
          <span className="block font-medium">{name}</span>
          <span className="block text-fg-secondary">
            {t.portTip(typeWord(port.dataType), port.required)}
          </span>
        </>
      }
    >
      <Handle
        type="target"
        id={port.name}
        position={Position.Left}
        isConnectableStart={false}
        data-port-type={port.dataType}
        data-hint={hint ?? undefined}
        style={dotStyle(port.dataType, port.required, hint, {
          left: -1,
          top: top ?? '50%',
          visibility: hidden ? 'hidden' : undefined,
        })}
      >
        {label ? (
          <span
            aria-hidden="true"
            style={{ right: 16 }}
            className="pointer-events-none absolute top-1/2 -translate-y-1/2 rounded-sm bg-surface-raised px-1 text-xs whitespace-nowrap text-fg-secondary opacity-0 shadow-1 group-hover/card:opacity-100"
          >
            {t.portLabel(name, typeWord(port.dataType), !port.required)}
          </span>
        ) : null}
      </Handle>
    </Tooltip>
  );
}

interface OutPortProps {
  nodeId: string;
  nodeName: string;
  port: PortMetadata;
  /** Nothing is linked from this output yet: its + stays visible. */
  free: boolean;
  top?: number;
  hidden?: boolean;
  /** Offset from the anchor's right edge to the card's border. */
  right?: number;
  dimmed?: boolean;
  label?: boolean;
  showAdd: boolean;
  onAdd: (e: React.MouseEvent<HTMLButtonElement>) => void;
}

/**
 * An output dot. Dragging from it draws a link; its + adds a connected step. The + shows
 * while the card is hovered or selected, and stays when nothing is linked yet.
 */
export function OutPort({
  nodeName,
  port,
  free,
  top,
  hidden = false,
  right = -1,
  dimmed = false,
  label = false,
  showAdd,
  onAdd,
}: OutPortProps) {
  const copy = useCopy();
  const t = copy.editor;
  const name = portLabel(port);
  return (
    <>
      <Tooltip
        content={
          <>
            <span className="block font-medium">{name}</span>
            <span className="block text-fg-secondary">{t.portOutTip(typeWord(port.dataType))}</span>
          </>
        }
      >
        <Handle
          type="source"
          id={port.name}
          position={Position.Right}
          data-port-type={port.dataType}
          className="cursor-crosshair"
          style={dotStyle(port.dataType, true, null, {
            right,
            top: top ?? '50%',
            visibility: hidden ? 'hidden' : undefined,
            opacity: dimmed ? 0.35 : 1,
          })}
        >
          {label ? (
            <span
              aria-hidden="true"
              style={{ left: 40 }}
              className="pointer-events-none absolute top-1/2 -translate-y-1/2 rounded-sm bg-surface-raised px-1 text-xs whitespace-nowrap text-fg-secondary opacity-0 shadow-1 group-hover/card:opacity-100"
            >
              {t.portLabel(name, typeWord(port.dataType), false)}
            </span>
          ) : null}
        </Handle>
      </Tooltip>
      {hidden ? null : (
        <Tooltip content={t.canvas.addAfterTip}>
          <button
            type="button"
            tabIndex={-1}
            aria-label={t.canvas.addAfter(name, nodeName)}
            onClick={onAdd}
            style={{ left: `calc(100% + ${String(-right + 6)}px)`, top: top ?? '50%' }}
            className={cn(
              'nodrag nopan absolute flex -translate-y-1/2 items-center p-0',
              showAdd || free
                ? 'opacity-100'
                : 'pointer-events-none opacity-0 group-hover/card:pointer-events-auto group-hover/card:opacity-100',
            )}
          >
            <span aria-hidden="true" className="h-px w-3 bg-border-control" />
            <span className="flex size-control-xs items-center justify-center rounded-md border border-border-control bg-surface-raised text-fg-secondary hover:border-accent hover:bg-surface-selected hover:text-fg">
              <Plus aria-hidden="true" className="size-icon-xs" />
            </span>
          </button>
        </Tooltip>
      )}
    </>
  );
}
