import { Panel, useReactFlow, useViewport } from '@xyflow/react';

import { cn } from '@/lib/utils';

export type InteractionMode = 'select' | 'pan';

interface CanvasToolbarProps {
  mode: InteractionMode;
  onModeChange: (mode: InteractionMode) => void;
  showGrid: boolean;
  onShowGridChange: (show: boolean) => void;
}

function ToolbarButton({
  active,
  onClick,
  label,
  children,
}: {
  active?: boolean;
  onClick: () => void;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      aria-pressed={active}
      title={label}
      className={cn(
        'flex h-8 min-w-8 items-center justify-center rounded-md px-2 text-xs font-medium text-[var(--muted-foreground)] hover:bg-[var(--accent)] hover:text-[var(--accent-foreground)]',
        active && 'bg-[var(--accent)] text-[var(--accent-foreground)]',
      )}
    >
      {children}
    </button>
  );
}

/**
 * Floating Canvas toolbar (04 §2.5). It only wraps
 * capabilities React Flow's public API actually provides: Select/Pan interaction mode,
 * Fit View, a Grid (Background) visibility toggle and zoom. The Backend and the codebase
 * have no Undo/Redo of Canvas edits today, so this toolbar does not add dead buttons for
 * them — Undo/Redo is a known gap to raise, not something to fake here.
 */
export function CanvasToolbar({
  mode,
  onModeChange,
  showGrid,
  onShowGridChange,
}: CanvasToolbarProps) {
  const { fitView, zoomIn, zoomOut } = useReactFlow();
  // useViewport subscribes to the store and re-renders this component on every pan/zoom
  // frame, so the percentage below always reflects the live viewport.
  const { zoom } = useViewport();
  const zoomPercent = Math.round(zoom * 100);

  return (
    <Panel position="bottom-center" className="flex items-center gap-2">
      <div className="flex items-center gap-1 rounded-full border border-[var(--border)] bg-[var(--card)] p-1 shadow-md">
        <ToolbarButton
          active={mode === 'select'}
          onClick={() => onModeChange('select')}
          label="Select"
        >
          Select
        </ToolbarButton>
        <ToolbarButton active={mode === 'pan'} onClick={() => onModeChange('pan')} label="Pan">
          Pan
        </ToolbarButton>
        <ToolbarButton onClick={() => fitView()} label="Fit view">
          Fit
        </ToolbarButton>
        <ToolbarButton
          active={showGrid}
          onClick={() => onShowGridChange(!showGrid)}
          label="Toggle grid"
        >
          Grid
        </ToolbarButton>
      </div>

      <div className="flex items-center gap-1 rounded-full border border-[var(--border)] bg-[var(--card)] px-1 py-1 shadow-md">
        <ToolbarButton onClick={() => zoomOut()} label="Zoom out">
          −
        </ToolbarButton>
        <span className="min-w-10 text-center text-xs tabular-nums text-[var(--muted-foreground)]">
          {zoomPercent}%
        </span>
        <ToolbarButton onClick={() => zoomIn()} label="Zoom in">
          +
        </ToolbarButton>
      </div>
    </Panel>
  );
}
