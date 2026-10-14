import { Popover as RPopover } from 'radix-ui';
import type { CSSProperties, ReactNode } from 'react';

import { POPUP_SURFACE } from './menu';
import { cn } from '@/lib/utils';

/**
 * Popovers (filters, the issue list, the time zone): open on click, focus the first
 * focusable element, close on Escape, an outside click, or Tab leaving the popover, and
 * return focus to the trigger.
 */
export const Popover = RPopover.Root;

/** Use with asChild around the trigger button. */
export const PopoverTrigger = RPopover.Trigger;

interface PopoverContentProps {
  children: ReactNode;
  label: string;
  align?: 'start' | 'center' | 'end';
  side?: 'top' | 'bottom';
  className?: string;
  style?: CSSProperties;
  /** Focus target on open; defaults to the first focusable element. */
  onOpenAutoFocus?: (e: Event) => void;
  /** Called when Tab moves focus out of the popover. */
  onTabOut?: () => void;
  /** Runs as the popover closes, before focus returns to the trigger; may move focus elsewhere. */
  onCloseAutoFocus?: (e: Event) => void;
}

export function PopoverContent({
  children,
  label,
  align = 'start',
  side = 'bottom',
  className,
  style,
  onOpenAutoFocus,
  onTabOut,
  onCloseAutoFocus,
}: PopoverContentProps) {
  return (
    <RPopover.Portal>
      <RPopover.Content
        aria-label={label}
        align={align}
        side={side}
        sideOffset={4}
        collisionPadding={8}
        onOpenAutoFocus={onOpenAutoFocus}
        onCloseAutoFocus={onCloseAutoFocus}
        onFocusOutside={() => onTabOut?.()}
        style={style}
        className={cn(POPUP_SURFACE, 'p-1 text-sm', className)}
      >
        {children}
      </RPopover.Content>
    </RPopover.Portal>
  );
}
