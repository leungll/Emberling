import { Panel, useReactFlow, useViewport } from '@xyflow/react';

import { cn } from '@/lib/utils';

export type InteractionMode = 'select' | 'pan';

interface CanvasToolbarProps {
  mode: InteractionMode;
  onModeChange: (mode: InteractionMode) => void;
  showGrid: boolean;
  onShowGridChange: (show: boolean) => void;
  history?: CanvasHistoryControls;
}

/** Undo/Redo of unsaved Definition edits, owned by the Edit page (04 §2.5). */
export interface CanvasHistoryControls {
  canUndo: boolean;
  canRedo: boolean;
  onUndo: () => void;
  onRedo: () => void;
}

function ToolbarButton({
  active,
  disabled,
  onClick,
  label,
  children,
}: {
  active?: boolean;
  disabled?: boolean;
  onClick: () => void;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      aria-pressed={active}
      title={label}
      className={cn(
        'flex h-8 min-w-8 items-center justify-center rounded-md px-2 text-xs font-medium text-[var(--muted-foreground)] hover:bg-[var(--accent)] hover:text-[var(--accent-foreground)]',
        active && 'bg-[var(--accent)] text-[var(--accent-foreground)]',
        'disabled:pointer-events-none disabled:opacity-40',
      )}
    >
      {children}
    </button>
  );
}

/**
 * Floating Canvas toolbar (04 §2.5 lists Select, Pan, Fit View, Zoom, Undo and Redo among
 * the MVP Canvas operations; Grid is optional). Select/Pan, Fit, Grid and zoom wrap React
 * Flow's public API. Undo/Redo follow Grid in the same group, where the Edit mock places
 * them; they are rendered only when the page supplies its edit history, and each is
 * disabled while its stack is empty.
 */
export function CanvasToolbar({
  mode,
  onModeChange,
  showGrid,
  onShowGridChange,
  history,
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
        {history ? (
          <>
            <ToolbarButton disabled={!history.canUndo} onClick={history.onUndo} label="Undo">
              Undo
            </ToolbarButton>
            <ToolbarButton disabled={!history.canRedo} onClick={history.onRedo} label="Redo">
              Redo
            </ToolbarButton>
          </>
        ) : null}
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
