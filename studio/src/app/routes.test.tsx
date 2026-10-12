import { render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { RouteErrorPage } from './RouteErrorPage';
import { routes } from './routes';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('routes', () => {
  it('shows a not-found page with a way back for an unknown address', async () => {
    const router = createMemoryRouter(routes, { initialEntries: ['/no/such/page'] });
    render(<RouterProvider router={router} />);

    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to Definitions' })).toHaveAttribute('href', '/');
    expect(screen.queryByText(/Hey developer/)).toBeNull();
  });

  it('replaces a page that throws while rendering with a calm error page', async () => {
    // React reports the caught render error on the console; it is expected here.
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    function Broken(): never {
      throw new Error('render failed');
    }
    const router = createMemoryRouter(
      [{ path: '/', element: <Broken />, errorElement: <RouteErrorPage /> }],
      { initialEntries: ['/'] },
    );
    render(<RouterProvider router={router} />);

    expect(
      await screen.findByRole('heading', { name: "This page couldn't be shown" }),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to Definitions' })).toHaveAttribute('href', '/');
    expect(screen.queryByText(/Hey developer/)).toBeNull();
  });

  it('gives every page the shared error page', () => {
    const [shell] = routes;
    expect(shell?.errorElement).toBeDefined();
    expect(shell?.children?.map((route) => route.path)).toEqual([
      '/',
      '/studio/new',
      '/studio/:workflowId',
      '/runs/:runId',
      '*',
    ]);
  });
});
