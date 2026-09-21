import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { DefinitionsPage } from './DefinitionsPage';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function renderPage() {
  render(
    <MemoryRouter>
      <DefinitionsPage />
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('DefinitionsPage', () => {
  it('shows the ApiError code when the list request fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(503, {
          error: { code: 'DEPENDENCY_UNAVAILABLE', message: 'PostgreSQL is unavailable' },
        }),
      ),
    );

    renderPage();

    expect(await screen.findByTestId('error-code')).toHaveTextContent('DEPENDENCY_UNAVAILABLE');
    expect(screen.getByRole('alert')).toHaveTextContent('PostgreSQL is unavailable');
  });

  it('shows the empty state when no definition exists', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { items: [] })));

    renderPage();

    expect(await screen.findByText('No definitions yet')).toBeInTheDocument();
  });

  it('renders a row with version, last run status and a never-run definition', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(200, {
          items: [
            {
              workflowId: 'wf_123',
              name: 'AIGC Media Generation',
              description: 'Generate an image and caption',
              latestVersion: 4,
              updatedAt: '2026-08-03T12:00:00Z',
              lastRun: {
                id: 'run_123',
                status: 'COMPLETED',
                createdAt: '2026-08-03T12:10:00Z',
              },
            },
            {
              workflowId: 'wf_456',
              name: 'Document Processing',
              description: '',
              latestVersion: 1,
              updatedAt: '2026-08-03T09:00:00Z',
              lastRun: null,
            },
          ],
        }),
      ),
    );

    renderPage();

    expect(await screen.findByText('AIGC Media Generation')).toBeInTheDocument();
    expect(screen.getByText('v4')).toBeInTheDocument();
    expect(screen.getByText('Completed')).toBeInTheDocument();
    expect(screen.getByText('Never run')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open Last Run' })).toHaveAttribute(
      'href',
      '/runs/run_123',
    );
  });
});
