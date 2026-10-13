import { X } from 'lucide-react';
import { Dialog as RDialog } from 'radix-ui';
import { useLayoutEffect, useRef, type KeyboardEvent, type ReactNode } from 'react';

import { IconButton } from './button';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

export type DialogSize = 'sm' | 'md' | 'lg';

const WIDTH: Record<DialogSize, number> = { sm: 440, md: 560, lg: 720 };

interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  /** One optional line under the title. */
  description?: ReactNode;
  children: ReactNode;
  /** Secondary actions first, the primary action last. */
  footer?: ReactNode;
  size?: DialogSize;
  /** ⌘↵ or Ctrl+↵ inside the dialog calls this (the primary action). */
  onSubmit?: () => void;
  className?: string;
}

const FIELD = 'input:not([type=hidden]):not([disabled]), textarea:not([disabled]), [role=combobox]';

/**
 * Initial focus: the first invalid field, else the first field, else the primary button
 * (marked data-dialog-primary), else the dialog itself.
 */
function focusInitial(root: HTMLElement | null): void {
  if (!root) return;
  const target =
    root.querySelector<HTMLElement>('[aria-invalid="true"]') ??
    root.querySelector<HTMLElement>(FIELD) ??
    root.querySelector<HTMLElement>('[data-dialog-primary]') ??
    root;
  target.focus();
  // A primary button that is disabled while the dialog loads cannot take focus; the dialog
  // itself takes it so focus never stays on the page behind.
  if (document.activeElement !== target) root.focus();
}

/**
 * The one dialog. Radix traps Tab inside, makes the page behind inert, closes on Escape and
 * returns focus to the trigger. A click outside does nothing: a dialog closes only through
 * its own controls or Escape.
 */
export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = 'md',
  onSubmit,
  className,
}: DialogProps) {
  const copy = useCopy();
  const contentRef = useRef<HTMLDivElement>(null);
  // Whatever had focus when the dialog opened gets it back when it closes.
  const returnTo = useRef<HTMLElement | null>(null);
  useLayoutEffect(() => {
    if (open && document.activeElement instanceof HTMLElement) {
      returnTo.current = document.activeElement;
    }
  }, [open]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (onSubmit && e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      onSubmit();
    }
  };

  return (
    <RDialog.Root
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <RDialog.Portal>
        <RDialog.Overlay className="fixed inset-0 z-dialog flex items-start justify-center overflow-y-auto bg-scrim px-4 pt-16 pb-4">
          <RDialog.Content
            ref={contentRef}
            onOpenAutoFocus={(e) => {
              e.preventDefault();
              focusInitial(contentRef.current);
            }}
            onCloseAutoFocus={(e) => {
              const target = returnTo.current;
              if (target?.isConnected) {
                e.preventDefault();
                target.focus();
              }
            }}
            onPointerDownOutside={(e) => {
              e.preventDefault();
            }}
            onInteractOutside={(e) => {
              e.preventDefault();
            }}
            onKeyDown={onKeyDown}
            {...(description ? {} : { 'aria-describedby': undefined })}
            tabIndex={-1}
            style={{ maxWidth: WIDTH[size], maxHeight: 'calc(100vh - 128px)' }}
            className={cn(
              'flex w-full flex-col rounded-xl border border-border bg-surface-raised text-fg shadow-3 focus-visible:outline-none',
              className,
            )}
          >
            <div className="flex items-start gap-3 py-4 pr-4 pl-5">
              <div className="min-w-0 flex-1">
                <RDialog.Title className="m-0 text-lg font-semibold">{title}</RDialog.Title>
                {description ? (
                  <RDialog.Description asChild>
                    <div className="mt-1 text-sm text-fg-muted">{description}</div>
                  </RDialog.Description>
                ) : null}
              </div>
              <RDialog.Close asChild>
                <IconButton label={copy.close}>
                  <X aria-hidden="true" />
                </IconButton>
              </RDialog.Close>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-4">{children}</div>
            {footer ? (
              <div className="flex items-center justify-end gap-2 border-t border-border-subtle px-5 py-4 [&>button]:h-control-lg">
                {footer}
              </div>
            ) : null}
          </RDialog.Content>
        </RDialog.Overlay>
      </RDialog.Portal>
    </RDialog.Root>
  );
}
