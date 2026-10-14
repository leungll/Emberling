import { Tooltip as RTooltip } from 'radix-ui';
import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

/** Hover opens a tooltip after 400 ms; keyboard navigation onto the trigger opens it at once. */
export const TOOLTIP_DELAY_MS = 400;

interface TooltipProps {
  /** One sentence or the complete value; may end with a kbd. Never the only place for
   *  information a task needs. */
  content: ReactNode;
  /** A single focusable element (the trigger). */
  children: ReactNode;
  side?: 'top' | 'right' | 'bottom' | 'left';
  align?: 'start' | 'center' | 'end';
  className?: string;
}

/** Keys whose default action or roving-focus handler moves focus. */
const NAVIGATION_KEYS = new Set([
  'Tab',
  'ArrowUp',
  'ArrowDown',
  'ArrowLeft',
  'ArrowRight',
  'Home',
  'End',
]);

// Focus counts as the viewer's keyboard focus only while a navigation key is down. Focus set
// in code, such as a dialog's initial focus or focus returned after Enter or Escape, opens no
// tooltip: an open tooltip would take the next Escape meant for the dialog.
let navigating = false;
if (typeof document !== 'undefined') {
  document.addEventListener(
    'keydown',
    (e) => {
      navigating = NAVIGATION_KEYS.has(e.key);
    },
    true,
  );
  for (const type of ['keyup', 'pointerdown'] as const) {
    document.addEventListener(
      type,
      () => {
        navigating = false;
      },
      true,
    );
  }
}

const surface =
  'z-popover rounded-lg border border-border bg-surface-raised px-3 py-2 text-sm text-fg shadow-1';

/**
 * A tooltip on its child. The tooltip itself never takes focus. Each tooltip carries its
 * own provider, so a component with a tooltip renders anywhere, including tests.
 */
export function Tooltip({
  content,
  children,
  side = 'top',
  align = 'center',
  className,
}: TooltipProps) {
  if (content === null || content === undefined || content === '') return <>{children}</>;
  return (
    <RTooltip.Provider delayDuration={TOOLTIP_DELAY_MS}>
      <RTooltip.Root>
        <RTooltip.Trigger
          asChild
          onFocus={(e) => {
            // Radix skips its own open-on-focus when this handler prevents the default.
            if (!navigating) e.preventDefault();
          }}
        >
          {children}
        </RTooltip.Trigger>
        <RTooltip.Portal>
          <RTooltip.Content
            side={side}
            align={align}
            sideOffset={6}
            collisionPadding={8}
            style={{ maxWidth: 320 }}
            className={cn(surface, 'wrap-anywhere', className)}
          >
            {content}
          </RTooltip.Content>
        </RTooltip.Portal>
      </RTooltip.Root>
    </RTooltip.Provider>
  );
}

/** A tooltip's body and the raw value under it (mono, muted). */
export function TooltipMeta({ body, meta }: { body: ReactNode; meta?: ReactNode }) {
  return (
    <>
      <span className="block text-fg-secondary">{body}</span>
      {meta ? <span className="mt-2 block font-mono text-xs text-fg-muted">{meta}</span> : null}
    </>
  );
}
