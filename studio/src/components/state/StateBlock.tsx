import { CircleAlert, Inbox, Search, SearchX, type LucideIcon } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router';

import { TechnicalDetails } from '@/components/facts/FactList';
import { buttonVariants } from '@/components/ui/button-variants';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

export type StateKind = 'empty' | 'nomatch' | 'error' | 'notfound';

const ICON: Record<StateKind, LucideIcon> = {
  empty: Inbox,
  nomatch: Search,
  error: CircleAlert,
  notfound: SearchX,
};

export type StateAction =
  | { label: string; to: string; icon?: ReactNode }
  | { label: string; onClick: () => void; icon?: ReactNode; primary?: boolean };

interface StateBlockProps {
  kind: StateKind;
  /** One sentence. */
  title: string;
  /** At most one more sentence. */
  body?: string;
  /** A mono line, such as "503 DEPENDENCY_UNAVAILABLE" or a full ID. */
  code?: string;
  action?: StateAction;
  /** Raw details for an error, collapsed under Technical details. */
  details?: ReactNode;
  /** Fills the page below the header and uses a page heading. */
  page?: boolean;
  className?: string;
}

/**
 * Empty, no-match, error and not-found states: icon, one title, at most one sentence and
 * one action. An empty state's action may be the page's primary action.
 */
export function StateBlock({
  kind,
  title,
  body,
  code,
  action,
  details,
  page,
  className,
}: StateBlockProps) {
  const Icon = ICON[kind];
  const Title = page ? 'h1' : 'p';
  const actionClass = cn(
    buttonVariants({
      variant: action && 'primary' in action && action.primary ? 'primary' : 'secondary',
    }),
    'mt-2',
  );
  return (
    <div
      role={kind === 'error' ? 'alert' : 'status'}
      className={cn(
        'flex flex-col items-center gap-2 px-6 py-12 text-center',
        page && 'min-h-full justify-center pb-16',
        className,
      )}
    >
      <Icon aria-hidden="true" className="mb-1 size-icon-lg text-fg-muted" />
      <Title style={{ maxWidth: '56ch' }} className="m-0 text-md font-medium text-fg">
        {title}
      </Title>
      {body ? (
        <p style={{ maxWidth: '56ch' }} className="m-0 text-sm text-fg-muted">
          {body}
        </p>
      ) : null}
      {code ? <p className="m-0 font-mono text-xs wrap-anywhere text-fg-muted">{code}</p> : null}
      {action ? (
        'to' in action ? (
          <Link to={action.to} className={cn(actionClass, 'no-underline')}>
            {action.icon}
            {action.label}
          </Link>
        ) : (
          <button type="button" className={actionClass} onClick={action.onClick}>
            {action.icon}
            {action.label}
          </button>
        )
      ) : null}
      {details ? (
        <div className="mt-2 w-full text-left" style={{ maxWidth: '56ch' }}>
          <TechnicalDetails storageKey="state">{details}</TechnicalDetails>
        </div>
      ) : null}
    </div>
  );
}

/** Skeletons appear only after this delay, so a fast load never flashes them. */
export const SKELETON_DELAY_MS = 150;

function useDelayed(ms: number): boolean {
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const id = window.setTimeout(() => {
      setShown(true);
    }, ms);
    return () => {
      window.clearTimeout(id);
    };
  }, [ms]);
  return shown;
}

/** A loading placeholder shaped like the final layout: table rows or lines of text. */
export function Skeleton({
  kind,
  count = 3,
  className,
}: {
  kind: 'rows' | 'text';
  count?: number;
  className?: string;
}) {
  const copy = useCopy();
  const shown = useDelayed(SKELETON_DELAY_MS);
  const bar = 'block h-3 rounded-sm bg-surface-hover animate-skeleton';
  return (
    <div aria-busy="true" aria-label={copy.loading} role="status" className={className}>
      {shown
        ? Array.from({ length: count }, (_, i) =>
            kind === 'rows' ? (
              <div
                key={i}
                className="flex h-row-table items-center gap-4 border-b border-border-subtle px-3"
              >
                <span className={bar} style={{ width: 96 }} />
                <span className={bar} style={{ width: `${40 + ((i * 13) % 30)}%` }} />
                <span className={bar} style={{ width: 72 }} />
              </div>
            ) : (
              <span
                key={i}
                className={cn(bar, 'my-1')}
                style={{ width: `${60 + ((i * 17) % 35)}%` }}
              />
            ),
          )
        : null}
    </div>
  );
}
