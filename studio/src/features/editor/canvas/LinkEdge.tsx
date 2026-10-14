import { EdgeLabelRenderer, type Edge as FlowEdge, type EdgeProps } from '@xyflow/react';
import { Plus, X } from 'lucide-react';
import { memo, useContext } from 'react';

import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditor } from '@/features/editor/editor-context';
import { linkPath } from '@/features/editor/model/layout';
import { LinkToolsContext, useCanvasView } from './canvas-view';
import { keyTip } from './key-tip';

export type LinkFlowEdge = FlowEdge<Record<string, never>, 'link'>;

const TOOL_BUTTON =
  'nodrag nopan flex size-control-xs items-center justify-center rounded-full border bg-surface-raised text-fg-secondary shadow-2';

/**
 * A link between two steps. A link that skips a column runs in a lane clear of the steps in
 * between. At its midpoint, + inserts a step into the link and × deletes it; they show for
 * the selected link, the hovered one, and the one a step is being inserted into.
 */
function LinkEdgeInner({
  id,
  source,
  target,
  sourceX,
  sourceY,
  targetX,
  targetY,
  selected,
}: EdgeProps<LinkFlowEdge>) {
  const copy = useCopy();
  const t = copy.editor.canvas;
  const view = useCanvasView();
  const tools = useContext(LinkToolsContext);
  const { ops, quickAdd } = useEditor();
  const s = view.places[source];
  const d = view.places[target];
  let x1 = sourceX;
  let y1 = sourceY;
  let x2 = targetX;
  let y2 = targetY;
  // Far out the port dots are hidden: links meet the cards' edge midpoints.
  if (view.level === 'far' && s && d) {
    x1 = s.x + s.w;
    y1 = s.y + s.h / 2;
    x2 = d.x;
    y2 = d.y + d.h / 2;
  }
  const g = linkPath({ source, target }, x1, y1, x2, y2, view.places, view.zoom);
  const z = view.zoom;
  const lod = view.level !== 'full';
  const hot = tools.hover === id || tools.inserting === id;
  const width = (selected || hot ? 2.5 : 1.5) / (lod ? z : 1);
  const showTools = tools.inserting === id || (!tools.hidden && (selected === true || hot));

  return (
    <>
      <path
        d={g.d}
        fill="none"
        stroke="transparent"
        strokeWidth={14 / (lod ? z : 1)}
        className={cn(
          'react-flow__edge-interaction',
          view.panning ? 'pointer-events-none' : 'cursor-pointer',
        )}
        style={{ pointerEvents: view.panning ? 'none' : 'stroke' }}
        onMouseEnter={() => tools.setHover(id)}
        onMouseLeave={() => tools.setHover(null)}
      />
      <path
        d={g.d}
        fill="none"
        style={{
          stroke: selected ? 'var(--accent)' : hot ? 'var(--fg-muted)' : 'var(--border-control)',
          strokeWidth: width,
          transition: 'stroke var(--dur-1), stroke-width var(--dur-1)',
          pointerEvents: 'none',
        }}
      />
      {showTools ? (
        <EdgeLabelRenderer>
          <div
            data-link-tools={id}
            onMouseEnter={() => tools.setHover(id)}
            onMouseLeave={() => tools.setHover(null)}
            style={{
              position: 'absolute',
              transform: `translate(${String(g.mx)}px, ${String(g.my)}px) translate(-50%, -50%) scale(${String(1 / z)})`,
              pointerEvents: 'all',
            }}
            className="z-canvas-ui flex gap-1"
          >
            <Tooltip content={t.insertTip}>
              <button
                type="button"
                tabIndex={-1}
                aria-label={t.insert}
                onClick={(e) => {
                  e.stopPropagation();
                  const r = e.currentTarget.getBoundingClientRect();
                  quickAdd({ x: r.left, y: r.bottom + 8, link: { kind: 'insert', edgeId: id } });
                }}
                className={cn(
                  TOOL_BUTTON,
                  selected ? 'border-accent' : 'border-border-control',
                  'hover:border-accent hover:bg-surface-selected hover:text-fg',
                )}
              >
                <Plus aria-hidden="true" className="size-icon-xs" />
              </button>
            </Tooltip>
            <Tooltip content={keyTip(t.deleteLinkTip, '⌫')}>
              <button
                type="button"
                tabIndex={-1}
                aria-label={t.deleteLink}
                onClick={(e) => {
                  e.stopPropagation();
                  tools.setHover(null);
                  ops.deleteEdge(id);
                }}
                className={cn(
                  TOOL_BUTTON,
                  selected ? 'border-accent' : 'border-border-control',
                  'hover:border-danger-fg hover:bg-st-failed-bg hover:text-danger-fg',
                )}
              >
                <X aria-hidden="true" className="size-icon-xs" />
              </button>
            </Tooltip>
          </div>
        </EdgeLabelRenderer>
      ) : null}
    </>
  );
}

export const LinkEdge = memo(LinkEdgeInner);
