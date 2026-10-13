import { Check } from 'lucide-react';
import { Toast } from 'radix-ui';

import { Button } from './button';
import { dismissToast, toastDuration, useToast } from '@/lib/toast';

/**
 * The toast region: bottom right, one toast at a time. Radix announces it politely and
 * pauses the timer while the pointer or focus is on it.
 */
export function Toaster() {
  const message = useToast();
  return (
    <Toast.Provider swipeDirection="right">
      {message ? (
        <Toast.Root
          key={message.id}
          duration={toastDuration(message)}
          onOpenChange={(open) => {
            if (!open) dismissToast(message.id);
          }}
          style={{ maxWidth: 'min(360px, calc(100vw - 32px))' }}
          className="flex w-max items-center gap-2 rounded-lg border border-border bg-surface-raised px-3 py-2 text-sm text-fg shadow-2"
        >
          {message.done ? (
            <Check className="size-icon-sm shrink-0 text-fg-muted" aria-hidden="true" />
          ) : null}
          <Toast.Description className="min-w-0 wrap-anywhere">{message.text}</Toast.Description>
          {message.action ? (
            <Toast.Action asChild altText={message.action.label}>
              <Button
                variant="ghost"
                size="sm"
                className="text-accent-fg"
                onClick={message.action.onAction}
              >
                {message.action.label}
              </Button>
            </Toast.Action>
          ) : null}
        </Toast.Root>
      ) : null}
      <Toast.Viewport className="fixed right-4 bottom-4 z-toast m-0 flex list-none flex-col p-0 outline-none" />
    </Toast.Provider>
  );
}
