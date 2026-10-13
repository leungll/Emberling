import { DropdownMenu } from 'radix-ui';
import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

/**
 * Action menus. Radix opens them on click, Enter, Space and ↓, focuses the first item,
 * moves with ↑/↓, Home/End and typeahead, closes on Escape or an outside click and
 * returns focus to the trigger. The width follows the content.
 */
export function Menu(props: Omit<DropdownMenu.DropdownMenuProps, 'modal'>) {
  // Not modal: the page stays in the accessibility tree; an outside click or Tab away
  // closes the menu.
  return <DropdownMenu.Root modal={false} {...props} />;
}

/** Use with asChild around the trigger button. */
export const MenuTrigger = DropdownMenu.Trigger;

export const POPUP_SURFACE =
  'z-popover rounded-lg border border-border bg-surface-raised text-fg shadow-1';

export function MenuContent({
  children,
  align = 'end',
  side = 'bottom',
  label,
  onCloseAutoFocus,
}: {
  children: ReactNode;
  align?: 'start' | 'center' | 'end';
  side?: 'top' | 'bottom';
  label?: string;
  /** Runs once the menu has closed, before focus returns to the trigger; an item that
   *  opens a dialog waits for this so the dialog's focus is not taken back. */
  onCloseAutoFocus?: (e: Event) => void;
}) {
  return (
    <DropdownMenu.Portal>
      <DropdownMenu.Content
        align={align}
        side={side}
        sideOffset={4}
        collisionPadding={8}
        aria-label={label}
        onCloseAutoFocus={onCloseAutoFocus}
        style={{
          minWidth: 'max(var(--radix-dropdown-menu-trigger-width), 180px)',
          maxWidth: 420,
          maxHeight: 'min(60vh, 480px)',
          scrollbarGutter: 'stable',
        }}
        className={cn(POPUP_SURFACE, 'w-max overflow-y-auto p-1')}
      >
        {children}
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  );
}

interface MenuItemProps {
  children: ReactNode;
  onSelect?: () => void;
  icon?: ReactNode;
  shortcut?: string;
  danger?: boolean;
  disabled?: boolean;
}

export function MenuItem({ children, onSelect, icon, shortcut, danger, disabled }: MenuItemProps) {
  return (
    <DropdownMenu.Item
      disabled={disabled}
      onSelect={onSelect}
      className={cn(
        'flex h-control-sm w-full cursor-default items-center gap-2 rounded-md px-2 text-left text-sm whitespace-nowrap outline-none select-none',
        'data-highlighted:bg-surface-hover data-highlighted:text-fg data-disabled:text-fg-placeholder',
        danger ? 'text-danger-fg' : 'text-fg-secondary',
        '[&_svg]:size-icon-sm',
        danger ? '' : '[&_svg]:text-fg-muted',
      )}
    >
      {icon}
      <span className="min-w-0 flex-1">{children}</span>
      {shortcut ? <span className="ml-auto pl-6 text-xs text-fg-muted">{shortcut}</span> : null}
    </DropdownMenu.Item>
  );
}

/** A menu item that is a link (keeps ⌘/Ctrl-click to open in a new tab). */
export function MenuLinkItem({ children, icon }: { children: ReactNode; icon?: ReactNode }) {
  return (
    <DropdownMenu.Item
      asChild
      className="flex h-control-sm w-full cursor-default items-center gap-2 rounded-md px-2 text-left text-sm whitespace-nowrap text-fg-secondary no-underline outline-none select-none data-highlighted:bg-surface-hover data-highlighted:text-fg [&_svg]:size-icon-sm [&_svg]:text-fg-muted"
    >
      {icon ? (
        <span className="contents">
          {icon}
          {children}
        </span>
      ) : (
        children
      )}
    </DropdownMenu.Item>
  );
}

export function MenuSeparator() {
  return <DropdownMenu.Separator className="my-1 h-px bg-border-subtle" />;
}

export function MenuLabel({ children }: { children: ReactNode }) {
  return (
    <DropdownMenu.Label className="px-2 pt-2 pb-1 text-xs font-medium text-fg-muted">
      {children}
    </DropdownMenu.Label>
  );
}
