import type { KeyboardEvent, PointerEvent } from 'react';

import { cn } from '@/lib/utils';

interface SplitterProps {
  /** The line's direction: a horizontal line resizes heights, a vertical one widths. */
  orientation: 'horizontal' | 'vertical';
  label: string;
  value: number;
  min: number;
  max: number;
  onDrag: (e: PointerEvent<HTMLDivElement>) => void;
  onKey: (e: KeyboardEvent<HTMLDivElement>) => void;
  /** Double-click returns to the default size. */
  onReset: () => void;
  /** Placement of the drag strip in its container. */
  className?: string;
}

/**
 * A resize line between two panes. The pointer drags a thin strip on the border; the
 * keyboard reaches a separator that stays out of view until it has focus, then covers the
 * strip with the focus ring and takes the arrow keys. The strip is decorative, so the page
 * offers one control per resize to assistive technology.
 */
export function Splitter({
  orientation,
  label,
  value,
  min,
  max,
  onDrag,
  onKey,
  onReset,
  className,
}: SplitterProps) {
  const horizontal = orientation === 'horizontal';
  return (
    <div
      onPointerDown={onDrag}
      onDoubleClick={onReset}
      className={cn(
        'group relative z-canvas-ui shrink-0',
        horizontal ? 'h-1 cursor-row-resize' : 'w-1 cursor-col-resize',
        className,
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          'absolute bg-border group-hover:bg-focus',
          horizontal
            ? 'inset-x-0 top-0.5 h-px group-hover:top-0 group-hover:h-1'
            : 'inset-y-0 left-0.5 w-px group-hover:left-0 group-hover:w-1',
        )}
      />
      <div
        role="separator"
        aria-orientation={orientation}
        aria-label={label}
        aria-valuenow={Math.round(value)}
        aria-valuemin={min}
        aria-valuemax={max}
        tabIndex={0}
        onKeyDown={onKey}
        className="absolute top-0 left-0 size-px overflow-hidden focus-visible:inset-0 focus-visible:size-auto focus-visible:bg-focus focus-visible:outline-offset-0"
      />
    </div>
  );
}
