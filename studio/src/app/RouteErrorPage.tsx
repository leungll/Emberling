import { ArrowLeft } from 'lucide-react';
import { isRouteErrorResponse, useRouteError } from 'react-router';

import { ListShell } from '@/components/shell/ListShell';
import { NotFoundView } from '@/components/shell/NotFoundView';
import { StateBlock } from '@/components/state/StateBlock';
import { useCopy } from '@/lib/copy';
import { useDocumentTitle } from '@/lib/use-document-title';

/**
 * Replaces the router's developer error screen. A page that throws while rendering is
 * swapped for this calm page with the way back to Runs. It stands alone, without the
 * sidebar, because the shell itself may be what failed.
 */
export function RouteErrorPage() {
  const error = useRouteError();
  const copy = useCopy();
  useDocumentTitle(copy.pageTitles.error);
  if (isRouteErrorResponse(error) && error.status === 404) {
    return (
      <ListShell>
        <NotFoundView kind="page" />
      </ListShell>
    );
  }
  return (
    <main className="flex min-h-screen flex-col bg-bg text-fg">
      <StateBlock
        page
        kind="error"
        title={copy.states.pageError.title}
        body={copy.states.pageError.body}
        action={{
          label: copy.notFound.page.action,
          to: '/runs',
          icon: <ArrowLeft aria-hidden="true" className="size-icon-sm" />,
        }}
        className="flex-1 justify-center"
      />
    </main>
  );
}
