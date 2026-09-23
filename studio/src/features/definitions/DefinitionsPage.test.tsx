import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
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
    // The Last Run cell states both status and time (04 §1.1): the run's own createdAt is
    // rendered under the status pill, with the absolute timestamp available on hover.
    const lastRunTime = screen.getByTestId('last-run-time-wf_123');
    expect(lastRunTime).toHaveAttribute('dateTime', '2026-08-03T12:10:00Z');
    expect(lastRunTime).toHaveAttribute('title', '2026-08-03 12:10:00Z');
    expect(lastRunTime.textContent).not.toBe('');
    expect(lastRunTime.textContent).not.toBe('—');
    expect(screen.getByText('Never run')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open Last Run' })).toHaveAttribute(
      'href',
      '/runs/run_123',
    );
  });
});

/** Shows where the router went and the draft identity it carried, without rendering Studio. */
function DraftProbe() {
  const location = useLocation();
  return <pre data-testid="draft-probe">{JSON.stringify(location.state)}</pre>;
}

describe('DefinitionsPage — New Definition', () => {
  it('opens a new unsaved draft in the editor with the entered name and description', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { items: [] }));
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <Routes>
          <Route path="/" element={<DefinitionsPage />} />
          <Route path="/studio/new" element={<DraftProbe />} />
        </Routes>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole('button', { name: '+ New Definition' }));
    const dialog = screen.getByRole('dialog', { name: 'New Definition' });
    const create = screen.getByRole('button', { name: 'Create' });
    expect(create).toBeDisabled();

    await user.type(screen.getByLabelText(/Name/), 'Document Processing');
    await user.type(screen.getByLabelText(/Description/), 'Summarise a document');
    await user.click(create);

    expect(dialog).not.toBeInTheDocument();
    expect(JSON.parse(screen.getByTestId('draft-probe').textContent ?? 'null')).toEqual({
      name: 'Document Processing',
      description: 'Summarise a document',
    });
    // POST /definitions only happens on the first Save: the Backend validates before it
    // creates version 1, so an empty graph cannot be persisted here.
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
