import type { ReactNode } from 'react';
import { Link } from 'react-router';

import { buttonVariants } from '@/components/ui/button-variants';
import { cn } from '@/lib/utils';

interface PageStateProps {
  title: string;
  /** One plain sentence saying what happened or what to do next. */
  body?: string;
  /** An identifier or error code, shown in mono under the body. */
  detail?: string;
  /** The primary action, such as Retry. The link back to Definitions is always present. */
  action?: ReactNode;
}

/**
 * A whole-page message for a route that has nothing to render: an unknown address, a
 * missing Run or Definition, or a load that failed. It always offers the way back to the
 * Definitions list, so no page is a dead end, and it never shows stale or editable data.
 */
export function PageState({ title, body, detail, action }: PageStateProps) {
  return (
    <main className="flex h-screen items-center justify-center bg-bg px-4 text-fg">
      <div style={{ maxWidth: 448 }} className="flex flex-col items-center gap-3 text-center">
        <h1 className="text-md font-semibold">{title}</h1>
        {body ? <p className="text-md text-fg-muted">{body}</p> : null}
        {detail ? (
          <p className="font-mono text-xs break-all text-fg-muted select-all">{detail}</p>
        ) : null}
        <div className="flex items-center gap-2 pt-2">
          {action}
          <Link to="/" className={cn(buttonVariants({ variant: action ? 'outline' : 'default' }))}>
            Back to Definitions
          </Link>
        </div>
      </div>
    </main>
  );
}
