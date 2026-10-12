import { isRouteErrorResponse, useRouteError } from 'react-router';

import { PageState } from '@/components/PageState';

/** Studio's page for an address that matches no route. */
export function NotFoundPage() {
  return <PageState title="Page not found" body="There is nothing at this address." />;
}

/**
 * Replaces the router's developer error screen. A page that throws while rendering is
 * swapped for this calm page, which keeps the way back to the Definitions list.
 */
export function RouteErrorPage() {
  const error = useRouteError();
  if (isRouteErrorResponse(error) && error.status === 404) return <NotFoundPage />;
  return (
    <PageState
      title="This page couldn't be shown"
      body="Something went wrong while opening it. Go back and try again."
    />
  );
}
