import { ChevronRight } from 'lucide-react';
import { Fragment, type ReactNode } from 'react';
import { Link } from 'react-router';

import { TimeZonePicker } from '@/components/prefs/TimeZonePicker';
import { useCopy } from '@/lib/copy';

export interface Crumb {
  label: string;
  /** Omitted for the current page, the last crumb. */
  to?: string;
}

interface PageHeaderProps {
  /** The page title, or a breadcrumb ending in the current page. */
  title: string | readonly Crumb[];
  /** The page's primary action, before the preferences. */
  actions?: ReactNode;
}

/** A thin rule between header groups. */
export function HeaderSeparator() {
  return <span aria-hidden="true" className="mx-1 h-icon-md w-px shrink-0 bg-border" />;
}

/**
 * The list pages' header: 48 px, the title on the left aligned with the page content, the
 * primary action and the time zone on the right.
 */
export function PageHeader({ title, actions }: PageHeaderProps) {
  const copy = useCopy();
  return (
    <header
      data-ui-header
      className="sticky top-0 z-header flex h-header shrink-0 items-center border-b border-border bg-surface pr-4 pl-6"
    >
      {/* Lines the title up with the page content, a 1280 px column centred in the page
          (32 px in from its edge), while the preferences stay 16 px from the window edge. */}
      <span
        aria-hidden="true"
        style={{ width: 'max(0px, calc(50% - 612px))' }}
        className="shrink-0"
      />
      <div className="flex min-w-0 flex-1 items-center gap-3">
        {typeof title === 'string' ? (
          <h1 className="m-0 min-w-0 text-md font-semibold wrap-anywhere">{title}</h1>
        ) : (
          <nav aria-label={copy.nav.breadcrumb} className="min-w-0">
            <ol className="m-0 flex list-none items-center gap-1.5 p-0 text-md text-fg-muted">
              {title.map((c, i) => (
                <Fragment key={`${c.label}-${String(i)}`}>
                  {i > 0 ? (
                    <li aria-hidden="true" className="flex shrink-0">
                      <ChevronRight className="size-icon-sm" />
                    </li>
                  ) : null}
                  <li className="min-w-0">
                    {c.to ? (
                      <Link
                        to={c.to}
                        className="inline-flex h-control-xs items-center rounded-sm hover:text-fg"
                      >
                        {c.label}
                      </Link>
                    ) : (
                      <h1
                        aria-current="page"
                        className="m-0 text-md font-semibold wrap-anywhere text-fg"
                      >
                        {c.label}
                      </h1>
                    )}
                  </li>
                </Fragment>
              ))}
            </ol>
          </nav>
        )}
        <span className="flex-1" />
        {actions ? (
          <>
            <div className="flex items-center gap-2">{actions}</div>
            <HeaderSeparator />
          </>
        ) : null}
        <TimeZonePicker />
      </div>
    </header>
  );
}
