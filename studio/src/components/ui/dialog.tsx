import { X } from 'lucide-react';
import type { ReactNode } from 'react';

import { Button } from './button';
import { cn } from '@/lib/utils';

interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
}

/**
 * Minimal modal built on native semantics. It renders nothing when closed, so tests and
 * the accessibility tree only ever see one dialog at a time.
 */
export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  className,
}: DialogProps) {
  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={cn(
          'w-full max-w-lg rounded-lg border border-[var(--border)] bg-[var(--card)] text-[var(--card-foreground)] shadow-lg',
          className,
        )}
      >
        <div className="flex items-start justify-between gap-4 border-b border-[var(--border)] px-4 py-3">
          <div>
            <h2 className="text-[16px] font-semibold">{title}</h2>
            {description ? (
              <div className="mt-1 text-[13px] text-[var(--muted-foreground)]">{description}</div>
            ) : null}
          </div>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close dialog">
            <X className="h-4 w-4" />
          </Button>
        </div>
        <div className="px-4 py-3">{children}</div>
        {footer ? (
          <div className="flex justify-end gap-2 border-t border-[var(--border)] px-4 py-3">
            {footer}
          </div>
        ) : null}
      </div>
    </div>
  );
}
